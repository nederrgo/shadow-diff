package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/shadow-diff/beru/internal/storage"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

type memRuns struct {
	mu      sync.Mutex
	filters map[string]map[string]struct{}
	tests   []storage.ShadowTest
}

func newMemRuns() *memRuns {
	return &memRuns{
		filters: map[string]map[string]struct{}{},
		tests:   []storage.ShadowTest{{ID: 1, Name: "default", StartTime: "now"}},
	}
}

func (m *memRuns) EnsureShadowTest(context.Context, string) error { return nil }
func (m *memRuns) DefaultShadowTestName() string                  { return "default" }
func (m *memRuns) ListShadowTests(context.Context, int) ([]storage.ShadowTest, error) {
	return append([]storage.ShadowTest(nil), m.tests...), nil
}
func (m *memRuns) GetShadowTest(_ context.Context, id int64) (storage.ShadowTest, error) {
	for _, st := range m.tests {
		if st.ID == id {
			return st, nil
		}
	}
	return storage.ShadowTest{}, context.Canceled
}
func (m *memRuns) NoisePathsForTest(_ context.Context, name string) (map[string]struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]struct{}{}
	for p := range m.filters[name] {
		out[p] = struct{}{}
	}
	return out, nil
}
func (m *memRuns) AddNoiseFilter(_ context.Context, name, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.filters[name] == nil {
		m.filters[name] = map[string]struct{}{}
	}
	m.filters[name][path] = struct{}{}
	return nil
}
func (m *memRuns) ListNoiseFilters(_ context.Context, name string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for p := range m.filters[name] {
		out = append(out, p)
	}
	return out, nil
}

type memTraces struct {
	mu      sync.Mutex
	reports map[string][]v2storage.RawReport
}

func newMemTraces() *memTraces {
	return &memTraces{reports: map[string][]v2storage.RawReport{}}
}

func (m *memTraces) AppendReport(_ context.Context, report *v2storage.RawReport) ([]v2storage.RawReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reports[report.TraceID] = append(m.reports[report.TraceID], *report)
	return append([]v2storage.RawReport(nil), m.reports[report.TraceID]...), nil
}
func (m *memTraces) SaveDiffVerdict(context.Context, string, *v2storage.VerdictState) error {
	return nil
}
func (m *memTraces) ListReports(_ context.Context, traceID, protocol string) ([]v2storage.RawReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []v2storage.RawReport
	for _, r := range m.reports[traceID] {
		if protocol == "" || r.Protocol == protocol {
			out = append(out, r)
		}
	}
	return out, nil
}
func (m *memTraces) ListTraceGroups(_ context.Context, shadowTestName string, limit int) ([]v2storage.TraceGroup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type key struct{ tid, proto string }
	seen := map[key]time.Time{}
	for _, reps := range m.reports {
		for _, r := range reps {
			if shadowTestName != "" && r.ShadowTestName != "" && r.ShadowTestName != shadowTestName {
				continue
			}
			k := key{r.TraceID, r.Protocol}
			if t, ok := seen[k]; !ok || r.CapturedAt.After(t) {
				seen[k] = r.CapturedAt
			}
		}
	}
	var out []v2storage.TraceGroup
	for k, t := range seen {
		out = append(out, v2storage.TraceGroup{
			TraceID: k.tid, Protocol: k.proto, LastCapturedAt: t.UTC().Format(time.RFC3339Nano),
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
func (m *memTraces) GetVerdict(context.Context, string) (*v2storage.VerdictState, error) {
	return nil, nil
}
func (m *memTraces) ListStaleIncompleteTraces(context.Context, time.Time) ([]v2storage.StaleIncompleteTrace, error) {
	return nil, nil
}

func testHandler(t *testing.T) *Handler {
	t.Helper()
	h, err := NewHandler(newMemRuns(), newMemTraces(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func seedMongoMismatch(t *testing.T, h *Handler, traceID string) {
	t.Helper()
	ctx := t.Context()
	repo := h.Repo
	now := time.Now().UTC()
	for _, rep := range []v2storage.RawReport{
		{TraceID: traceID, ShadowRole: "control-a", ShadowTestName: "default", Protocol: "mongodb", Direction: v2storage.DirectionEgress, Signature: "mongodb:insert:orders", PayloadBytes: []byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "control-b", ShadowTestName: "default", Protocol: "mongodb", Direction: v2storage.DirectionEgress, Signature: "mongodb:insert:orders", PayloadBytes: []byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "candidate", ShadowTestName: "default", Protocol: "mongodb", Direction: v2storage.DirectionEgress, Signature: "mongodb:insert:orders", PayloadBytes: []byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "candidate", ShadowTestName: "default", Protocol: "mongodb", Direction: v2storage.DirectionEgress, Signature: "mongodb:insert:orders", PayloadBytes: []byte(`{"insert":"orders","documents":[{"audit":"n1"}]}`), CapturedAt: now.Add(time.Millisecond)},
	} {
		if _, err := repo.AppendReport(ctx, &rep); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAPIShadowTests(t *testing.T) {
	h := testHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shadow-tests", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestAPINoiseFilters(t *testing.T) {
	h := testHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	body, _ := json.Marshal(map[string]string{
		"shadow_test_name": "default",
		"path":             "timestamp",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/noise/filters", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDashboardIndex(t *testing.T) {
	h := testHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	h.handleIndex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte("Beru Dashboard")) {
		t.Fatal("missing dashboard title")
	}
}

func TestSaveAndViewTraceSequence(t *testing.T) {
	h := testHandler(t)
	traceID := "view-seq"
	seedMongoMismatch(t, h, traceID)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/traces/"+traceID+"?protocol=mongodb", nil)
	h.handleTrace(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte("Egress sequence")) {
		t.Fatal("missing egress sequence section")
	}
	if !bytes.Contains(body, []byte("Unexpected extra egress")) {
		t.Fatal("missing extra egress badge")
	}
	if !bytes.Contains(body, []byte("mongodb:insert:orders")) {
		t.Fatal("missing egress signature")
	}
}

func TestSaveAndViewTraceIngress(t *testing.T) {
	h := testHandler(t)
	ctx := t.Context()
	traceID := "view-trace"
	now := time.Now().UTC()
	for _, rep := range []v2storage.RawReport{
		{TraceID: traceID, ShadowRole: "control-a", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionIngress, Signature: "http:GET:/", PayloadBytes: []byte(`{"x":1}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "control-b", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionIngress, Signature: "http:GET:/", PayloadBytes: []byte(`{"x":1}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "candidate", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionIngress, Signature: "http:GET:/", PayloadBytes: []byte(`{"x":2}`), CapturedAt: now},
	} {
		if _, err := h.Repo.AppendReport(ctx, &rep); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/traces/"+traceID+"?protocol=http&direction=ingress", nil)
	h.handleTrace(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte("Ingress response")) {
		t.Fatal("missing ingress response section")
	}
}

func TestListTraceSummariesHTTPSplitByDirection(t *testing.T) {
	h := testHandler(t)
	ctx := t.Context()
	traceID := "http-split"
	now := time.Now().UTC()
	for _, rep := range []v2storage.RawReport{
		{TraceID: traceID, ShadowRole: "control-a", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionIngress, Signature: "http:POST:/work", PayloadBytes: []byte(`{"ok":true}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "control-b", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionIngress, Signature: "http:POST:/work", PayloadBytes: []byte(`{"ok":true}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "candidate", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionIngress, Signature: "http:POST:/work", PayloadBytes: []byte(`{"ok":true}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "control-a", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionEgress, Signature: "http:GET:/api", PayloadBytes: []byte(`{"out":1}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "control-b", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionEgress, Signature: "http:GET:/api", PayloadBytes: []byte(`{"out":1}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "candidate", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionEgress, Signature: "http:GET:/api", PayloadBytes: []byte(`{"out":1}`), CapturedAt: now},
	} {
		if _, err := h.Repo.AppendReport(ctx, &rep); err != nil {
			t.Fatal(err)
		}
	}

	summaries, err := listTraceSummaries(ctx, h.Repo, nil, "default", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var ingress, egress bool
	for _, s := range summaries {
		if s.TraceID != traceID || s.Protocol != "http" {
			continue
		}
		switch s.Direction {
		case v2storage.DirectionIngress:
			ingress = true
			if s.Signatures != "http:POST:/work" {
				t.Fatalf("ingress signatures = %q", s.Signatures)
			}
		case v2storage.DirectionEgress:
			egress = true
			if s.Signatures != "http:GET:/api" {
				t.Fatalf("egress signatures = %q", s.Signatures)
			}
		}
	}
	if !ingress || !egress {
		t.Fatalf("want ingress and egress rows, got ingress=%v egress=%v summaries=%+v", ingress, egress, summaries)
	}
}
