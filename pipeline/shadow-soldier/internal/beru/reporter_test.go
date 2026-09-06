package beru

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shadow-diff/beruclient"
	"github.com/shadow-diff/shadow-soldier/internal/parsers"
)

const traceA = "4bf92f3577b34da6a3ce929d0e0e4736"
const traceB = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type Report = beruclient.Report

var NewClient = beruclient.NewClient

const egressDiffPath = beruclient.EgressDiffPath

func report(trace, target string) parsers.QueryReport {
	return parsers.QueryReport{
		TraceID:   trace,
		Protocol:  parsers.ProtocolPostgres,
		Operation: "select",
		Target:    target,
		RawQuery:  "SELECT * FROM " + target,
	}
}

// recordingBeru captures the bodies posted to /api/v1/egress/diff.
func recordingBeru(t *testing.T, status func() int) (*httptest.Server, func() []Report) {
	t.Helper()
	var mu sync.Mutex
	var got []Report
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != egressDiffPath {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		code := http.StatusAccepted
		if status != nil {
			code = status()
		}
		if code >= 200 && code < 300 {
			var rep Report
			if err := json.Unmarshal(body, &rep); err != nil {
				t.Errorf("unmarshal: %v", err)
			}
			mu.Lock()
			got = append(got, rep)
			mu.Unlock()
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []Report {
		mu.Lock()
		defer mu.Unlock()
		return append([]Report(nil), got...)
	}
}

func TestReporterPostsReport(t *testing.T) {
	t.Parallel()
	srv, received := recordingBeru(t, nil)

	r := &Reporter{
		Client:         NewClient(srv.URL, time.Second),
		Role:           "candidate",
		ShadowTestName: "my-test",
		ShadowPod:      "my-test-candidate-abc",
	}
	r.Start()
	r.Enqueue(report(traceA, "users"))
	r.Stop()

	got := received()
	if len(got) != 1 {
		t.Fatalf("got %d reports, want 1", len(got))
	}
	if got[0].TraceID != traceA {
		t.Fatalf("TraceID = %q want %q", got[0].TraceID, traceA)
	}
	if got[0].Workload != "candidate" {
		t.Fatalf("Workload = %q want candidate", got[0].Workload)
	}
	if got[0].Signature != "postgresql:select:users" {
		t.Fatalf("Signature = %q", got[0].Signature)
	}
	if got[0].ShadowTestName != "my-test" {
		t.Fatalf("ShadowTestName = %q", got[0].ShadowTestName)
	}
	var payload map[string]any
	if err := json.Unmarshal(got[0].Payload, &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["target"] != "users" || payload["shadow_pod"] != "my-test-candidate-abc" {
		t.Fatalf("payload = %v", payload)
	}
}

// An untraced query cannot be correlated across roles, so it must be dropped and
// counted rather than posted.
func TestReporterDropsUntraced(t *testing.T) {
	t.Parallel()
	srv, received := recordingBeru(t, nil)

	r := &Reporter{Client: NewClient(srv.URL, time.Second), Role: "control-a"}
	r.Start()
	r.Enqueue(report("", "users"))
	r.Stop()

	if got := received(); len(got) != 0 {
		t.Fatalf("posted %d untraced reports, want 0", len(got))
	}
	_, _, untraced, _ := r.Stats()
	if untraced != 1 {
		t.Fatalf("untraced counter = %d want 1", untraced)
	}
}

// Enqueue must never block, and overflow must be counted rather than silent.
func TestReporterQueueFullDropsAndCounts(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	r := &Reporter{
		Client:    NewClient(srv.URL, 5*time.Second),
		Role:      "control-a",
		Workers:   1,
		QueueSize: 2,
	}
	r.Start()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 500 {
			r.Enqueue(report(traceA, "users"))
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Enqueue blocked — the database path would stall behind Beru")
	}

	close(release)
	r.Stop()

	_, dropped, _, _ := r.Stats()
	if dropped == 0 {
		t.Fatal("dropped counter = 0, want the overflow to be counted")
	}
}

// All reports for one trace must be posted by one worker, in order, or Beru's
// index-paired comparison lines up the wrong queries against each other.
func TestReporterKeepsPerTraceOrder(t *testing.T) {
	t.Parallel()
	srv, received := recordingBeru(t, nil)

	r := &Reporter{Client: NewClient(srv.URL, time.Second), Role: "control-a", Workers: 5}
	r.Start()
	targets := []string{"t0", "t1", "t2", "t3", "t4", "t5", "t6", "t7"}
	for _, target := range targets {
		r.Enqueue(report(traceA, target))
	}
	r.Stop()

	got := received()
	if len(got) != len(targets) {
		t.Fatalf("got %d reports, want %d", len(got), len(targets))
	}
	for i, want := range targets {
		if got[i].Signature != "postgresql:select:"+want {
			t.Fatalf("report %d = %q, want signature for %q — per-trace order was not preserved",
				i, got[i].Signature, want)
		}
	}
}

func TestReporterShardsByTraceID(t *testing.T) {
	t.Parallel()
	r := &Reporter{Workers: 5, QueueSize: 50}
	r.shards = make([]chan parsers.QueryReport, r.Workers)
	for i := range r.shards {
		r.shards[i] = make(chan parsers.QueryReport, 10)
	}
	r.Log = nil

	shardOf := func(trace string) int {
		before := make([]int, len(r.shards))
		for i, s := range r.shards {
			before[i] = len(s)
		}
		r.Enqueue(parsers.QueryReport{TraceID: trace, Protocol: parsers.ProtocolRedis, Operation: "get"})
		for i, s := range r.shards {
			if len(s) != before[i] {
				return i
			}
		}
		return -1
	}

	first := shardOf(traceA)
	if first < 0 {
		t.Fatal("Enqueue routed nowhere")
	}
	for range 5 {
		if got := shardOf(traceA); got != first {
			t.Fatalf("trace %s routed to shard %d then %d — sharding is not stable", traceA, first, got)
		}
	}
	// Not an assertion about which shard, only that the function distinguishes.
	_ = shardOf(traceB)
}

func TestReporterRetriesOn5xxThenSucceeds(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv, received := recordingBeru(t, func() int {
		if calls.Add(1) == 1 {
			return http.StatusServiceUnavailable
		}
		return http.StatusAccepted
	})

	r := &Reporter{Client: NewClient(srv.URL, time.Second), Role: "control-a", Workers: 1}
	r.Start()
	r.Enqueue(report(traceA, "users"))
	r.Stop()

	if got := received(); len(got) != 1 {
		t.Fatalf("got %d accepted reports, want 1 (calls=%d)", len(got), calls.Load())
	}
	sent, _, _, failed := r.Stats()
	if sent != 1 || failed != 0 {
		t.Fatalf("sent=%d failed=%d want 1/0", sent, failed)
	}
}

// A 4xx means the report itself is wrong; resending it would only produce the
// same rejection.
func TestReporterDoesNotRetry4xx(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv, _ := recordingBeru(t, func() int {
		calls.Add(1)
		return http.StatusBadRequest
	})

	r := &Reporter{Client: NewClient(srv.URL, time.Second), Role: "control-a", Workers: 1}
	r.Start()
	r.Enqueue(report(traceA, "users"))
	r.Stop()

	if got := calls.Load(); got != 1 {
		t.Fatalf("made %d calls, want 1 (no retry on 4xx)", got)
	}
	_, _, _, failed := r.Stats()
	if failed != 1 {
		t.Fatalf("failed counter = %d want 1", failed)
	}
}

// MongoDB command documents must reach Beru with their top-level keys intact so
// mongoPayloadsEqual can strip _id/lsid/comment/$db before diffing.
func TestBuildPayloadPassesMongoDocumentThrough(t *testing.T) {
	t.Parallel()
	doc := `{"insert":"orders","$db":"shop","lsid":{"id":"x"}}`
	got, err := BuildPayload(parsers.QueryReport{
		Protocol: parsers.ProtocolMongoDB,
		RawQuery: doc,
	}, "pod-1")
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	for _, key := range []string{"insert", "$db", "lsid"} {
		if _, ok := obj[key]; !ok {
			t.Fatalf("payload lost top-level key %q: %s", key, got)
		}
	}
}

func TestBuildPayloadWrapsNonMongo(t *testing.T) {
	t.Parallel()
	got, err := BuildPayload(parsers.QueryReport{
		Protocol:  parsers.ProtocolPostgres,
		Operation: "select",
		Target:    "users",
		RawQuery:  "SELECT 1",
		Params:    []string{"a"},
	}, "pod-1")
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if obj["operation"] != "select" || obj["target"] != "users" || obj["raw_query"] != "SELECT 1" {
		t.Fatalf("payload = %v", obj)
	}
}
