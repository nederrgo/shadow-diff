package http

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shadow-diff/igris/internal/config"
	"github.com/shadow-diff/igris/internal/core"
	"github.com/shadow-diff/igris/internal/driver"
	"github.com/shadow-diff/igris/internal/payload"
	"github.com/shadow-diff/trace"
)

const testMaxBodySize = 512 * 1024

// testMaxConcurrency disables the concurrency gate for tests that don't exercise it.
const testMaxConcurrency = -1

func testConfig(targets ...*httptest.Server) config.Config {
	return config.Config{
		ControlAURL:    targets[0].URL,
		ControlBURL:    targets[1].URL,
		CandidateURL:   targets[2].URL,
		ControlAAddr:   "127.0.0.1",
		ControlBAddr:   "127.0.0.1",
		CandidateAddr:  "127.0.0.1",
		WorkerPoolSize: 4,
		MaxTCPConns:    16,
		TCPDialTimeout: time.Second,
		TCPIdleTimeout: time.Minute,
	}
}

func TestTransformPreservesInboundTraceparent(t *testing.T) {
	t.Parallel()
	d := New(testMaxBodySize, testMaxConcurrency)
	inbound := "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(trace.HeaderTraceparent, inbound)
	sess := &Session{Request: req, Body: nil}
	meta, err := d.ParseMetadata(sess)
	if err != nil {
		t.Fatal(err)
	}
	if meta.TraceID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("trace id = %q", meta.TraceID)
	}
	msg, err := d.Transform(sess, meta)
	if err != nil {
		t.Fatal(err)
	}
	hm := msg.(*message)
	if got := hm.headers.Get(trace.HeaderTraceparent); got != inbound {
		t.Fatalf("traceparent = %q, want %q", got, inbound)
	}
}

func TestParseMetadataFromTraceparentOnly(t *testing.T) {
	t.Parallel()
	d := New(testMaxBodySize, testMaxConcurrency)
	inbound := "01-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(trace.HeaderTraceparent, inbound)
	meta, err := d.ParseMetadata(&Session{Request: req})
	if err != nil {
		t.Fatal(err)
	}
	if meta.TraceID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("trace id = %q", meta.TraceID)
	}
	if meta.Traceparent != inbound {
		t.Fatalf("traceparent = %q, want literal preserved", meta.Traceparent)
	}
	msg, err := d.Transform(&Session{Request: req}, meta)
	if err != nil {
		t.Fatal(err)
	}
	hm := msg.(*message)
	if got := hm.headers.Get(trace.HeaderTraceparent); got != inbound {
		t.Fatalf("traceparent = %q, want %q", got, inbound)
	}
}

func TestParseMetadataRejectsMissingTraceparent(t *testing.T) {
	t.Parallel()
	d := New(testMaxBodySize, testMaxConcurrency)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	_, err := d.ParseMetadata(&Session{Request: req, Body: nil})
	if err == nil {
		t.Fatal("expected error for missing traceparent")
	}
}

func TestMulticastCloneFidelityAndTraceOnAllTargets(t *testing.T) {
	t.Parallel()

	type captured struct {
		method      string
		requestURI  string
		body        string
		traceparent string
	}
	var mu sync.Mutex
	captures := make([]captured, 0, 3)

	servers := make([]*httptest.Server, 3)
	for i := range servers {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			captures = append(captures, captured{
				method:      r.Method,
				requestURI:  r.URL.RequestURI(),
				body:        string(b),
				traceparent: r.Header.Get(trace.HeaderTraceparent),
			})
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
		servers[i] = srv
		defer srv.Close()
	}

	cfg := testConfig(servers...)
	hub := core.NewHub(cfg, slog.Default())

	rec := httptest.NewRecorder()
	inboundTP := "00-" + strings.Repeat("f", 32) + "-" + strings.Repeat("a", 16) + "-01"
	req := httptest.NewRequest(http.MethodPost, "/api/items?q=1", bytes.NewReader([]byte(`{"ok":true}`)))
	req.Header.Set(trace.HeaderTraceparent, inboundTP)
	body, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err := hub.HandleAtomic(New(testMaxBodySize, testMaxConcurrency), &Session{Request: req, Body: body, Writer: rec}); err != nil {
		t.Fatal(err)
	}
	hub.WaitPendingAtomic()

	mu.Lock()
	defer mu.Unlock()
	if len(captures) != 3 {
		t.Fatalf("got %d captures, want 3", len(captures))
	}
	for _, c := range captures {
		if c.traceparent != inboundTP {
			t.Fatalf("traceparent = %q, want %q", c.traceparent, inboundTP)
		}
		if c.method != http.MethodPost {
			t.Fatalf("method = %q", c.method)
		}
		if c.requestURI != "/api/items?q=1" {
			t.Fatalf("uri = %q", c.requestURI)
		}
	}
}

func TestMulticastPreservesInboundTraceparentLiteral(t *testing.T) {
	t.Parallel()
	inbound := "01-cccccccccccccccccccccccccccccccc-dddddddddddddddd-01"
	var mu sync.Mutex
	var traceparents []string
	servers := make([]*httptest.Server, 3)
	for i := range servers {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			traceparents = append(traceparents, r.Header.Get(trace.HeaderTraceparent))
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
		servers[i] = srv
		defer srv.Close()
	}
	hub := core.NewHub(testConfig(servers...), slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set(trace.HeaderTraceparent, inbound)
	rec := httptest.NewRecorder()
	if err := hub.HandleAtomic(New(testMaxBodySize, testMaxConcurrency), &Session{Request: req, Writer: rec}); err != nil {
		t.Fatal(err)
	}
	hub.WaitPendingAtomic()
	mu.Lock()
	defer mu.Unlock()
	if len(traceparents) != 3 {
		t.Fatalf("got %d backends", len(traceparents))
	}
	for i, tp := range traceparents {
		if tp != inbound {
			t.Fatalf("backend %d traceparent = %q, want %q", i, tp, inbound)
		}
	}
}

func TestTransformDeletesDuplicateCasedTraceHeaders(t *testing.T) {
	t.Parallel()
	d := New(testMaxBodySize, testMaxConcurrency)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header["Traceparent"] = []string{"01-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"}
	req.Header["traceparent"] = []string{"01-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"}
	meta, err := d.ParseMetadata(&Session{Request: req})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := d.Transform(&Session{Request: req}, meta)
	if err != nil {
		t.Fatal(err)
	}
	hm := msg.(*message)
	if len(hm.headers.Values(trace.HeaderTraceparent)) != 1 {
		t.Fatalf("want one traceparent, got %v", hm.headers.Values(trace.HeaderTraceparent))
	}
}

func TestTransformRedactsHeaders(t *testing.T) {
	t.Parallel()
	d := New(testMaxBodySize, testMaxConcurrency)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(trace.HeaderTraceparent, "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "a=b")
	req.Header.Set("X-Keep", "yes")
	sess := &Session{Request: req}
	meta, err := d.ParseMetadata(sess)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := d.Transform(sess, meta)
	if err != nil {
		t.Fatal(err)
	}
	hm := msg.(*message)
	for _, h := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if hm.headers.Get(h) != "" {
			t.Fatalf("outbound has %q", h)
		}
	}
	if hm.headers.Get("X-Keep") != "yes" {
		t.Fatal("X-Keep stripped")
	}
	if req.Header.Get("Authorization") != "Bearer secret" {
		t.Fatal("Transform mutated original request headers")
	}
}

func TestHandlerReturns202WithTrace(t *testing.T) {
	t.Parallel()

	backendTrace := make(chan string, 3)
	mkBackend := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backendTrace <- r.Header.Get(trace.HeaderTraceparent)
			w.WriteHeader(http.StatusOK)
		}))
	}
	backend := mkBackend()
	defer backend.Close()
	s2 := mkBackend()
	defer s2.Close()
	s3 := mkBackend()
	defer s3.Close()

	cfg := config.Config{
		ControlAURL:    backend.URL,
		ControlBURL:    s2.URL,
		CandidateURL:   s3.URL,
		ControlAAddr:   "127.0.0.1",
		ControlBAddr:   "127.0.0.1",
		CandidateAddr:  "127.0.0.1",
		WorkerPoolSize: 2,
		MaxTCPConns:    8,
		TCPDialTimeout: time.Second,
		TCPIdleTimeout: time.Minute,
	}
	hub := core.NewHub(cfg, slog.Default())
	d := New(testMaxBodySize, testMaxConcurrency)
	d.Client = backend.Client()

	mux := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		sess := &Session{Request: r, Body: body, Writer: w}
		_ = hub.HandleAtomic(d, sess)
	}))
	defer mux.Close()

	inboundTP := "00-" + strings.Repeat("e", 32) + "-" + strings.Repeat("b", 16) + "-01"
	req, err := http.NewRequest(http.MethodGet, mux.URL+"/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(trace.HeaderTraceparent, inboundTP)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d", resp.StatusCode)
	}
	respTP := resp.Header.Get(trace.HeaderTraceparent)
	if respTP != inboundTP {
		t.Fatalf("response traceparent %q, want %q", respTP, inboundTP)
	}
	hub.WaitPendingAtomic()
	for i := 0; i < 3; i++ {
		select {
		case got := <-backendTrace:
			if got != inboundTP {
				t.Fatalf("backend traceparent %q != inbound %q", got, inboundTP)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for backend %d", i)
		}
	}
}

func TestHandlerRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	hits := make(chan struct{}, 3)
	mkBackend := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits <- struct{}{}
			w.WriteHeader(http.StatusOK)
		}))
	}
	s1, s2, s3 := mkBackend(), mkBackend(), mkBackend()
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()

	const maxBody = 1024
	cfg := config.Config{
		ControlAURL:    s1.URL,
		ControlBURL:    s2.URL,
		CandidateURL:   s3.URL,
		ControlAAddr:   "127.0.0.1",
		ControlBAddr:   "127.0.0.1",
		CandidateAddr:  "127.0.0.1",
		WorkerPoolSize: 2,
		MaxTCPConns:    8,
		TCPDialTimeout: time.Second,
		TCPIdleTimeout: time.Minute,
	}
	hub := core.NewHub(cfg, slog.Default())
	d := New(maxBody, testMaxConcurrency)
	d.Client = s1.Client()

	srv := httptest.NewServer(d.handler(hub))
	defer srv.Close()

	oversized := bytes.Repeat([]byte("x"), maxBody+1)
	resp, err := http.Post(srv.URL+"/", "application/json", bytes.NewReader(oversized))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", resp.StatusCode)
	}

	select {
	case <-hits:
		t.Fatal("backend received request for oversized body")
	default:
	}
}

// blockingHandler is a driver.Handler stub that blocks inside HandleAtomic until
// released, so a test can hold the concurrency gate open for a controlled window.
type blockingHandler struct {
	calls   int32
	entered chan struct{}
	release chan struct{}
}

func (b *blockingHandler) HandleAtomic(d driver.AtomicDriver, sess driver.Session) error {
	atomic.AddInt32(&b.calls, 1)
	close(b.entered)
	<-b.release
	return nil
}

func (b *blockingHandler) RelayTCP(ctx context.Context, src net.Conn, listenPort int) {}

func TestHandlerShedsOverCapacity(t *testing.T) {
	t.Parallel()

	h := &blockingHandler{entered: make(chan struct{}), release: make(chan struct{})}
	d := New(testMaxBodySize, 1)
	srv := httptest.NewServer(d.handler(h))
	defer srv.Close()

	firstDone := make(chan *http.Response, 1)
	firstErr := make(chan error, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/")
		if err != nil {
			firstErr <- err
			return
		}
		firstDone <- resp
	}()

	select {
	case <-h.entered:
	case err := <-firstErr:
		t.Fatalf("first request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first request to enter handler")
	}

	// Second request must be shed immediately — before ResolveContext or HandleAtomic.
	resp2, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", resp2.StatusCode)
	}

	close(h.release)
	select {
	case resp1 := <-firstDone:
		defer resp1.Body.Close()
	case err := <-firstErr:
		t.Fatalf("first request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first request to complete")
	}

	if got := atomic.LoadInt32(&h.calls); got != 1 {
		t.Fatalf("HandleAtomic called %d times, want 1", got)
	}
}

func TestDispatchUsesDetachedContext(t *testing.T) {
	t.Parallel()

	received := make(chan struct{}, 3)
	servers := make([]*httptest.Server, 3)
	for i := range servers {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received <- struct{}{}
			w.WriteHeader(http.StatusOK)
		}))
		servers[i] = srv
		defer srv.Close()
	}

	msg := &message{
		method:     http.MethodGet,
		requestURI: "/test",
		headers:    http.Header{trace.HeaderTraceparent: []string{"00-" + strings.Repeat("a", 32) + "-" + strings.Repeat("b", 16) + "-01"}},
		client:     servers[0].Client(),
	}
	targets := []payload.Target{
		{Name: "a", BaseURL: servers[0].URL},
		{Name: "b", BaseURL: servers[1].URL},
		{Name: "c", BaseURL: servers[2].URL},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = ctx
	results := msg.Dispatch(context.Background(), targets)
	if len(results) != 3 {
		t.Fatalf("got %d results", len(results))
	}
	for i := 0; i < 3; i++ {
		select {
		case <-received:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d received", i)
		}
	}
}
