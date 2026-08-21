package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

type egressRouteRecorder struct {
	routed    atomic.Bool
	last      atomic.Pointer[v2storage.RawReport]
	appendErr error
}

func (r *egressRouteRecorder) AppendReport(ctx context.Context, report *v2storage.RawReport) ([]v2storage.RawReport, error) {
	if r.appendErr != nil {
		return nil, r.appendErr
	}
	r.last.Store(report)
	r.routed.Store(true)
	return []v2storage.RawReport{*report}, nil
}

func (r *egressRouteRecorder) SaveDiffVerdict(ctx context.Context, traceID string, verdict *v2storage.VerdictState) error {
	return nil
}

func (r *egressRouteRecorder) ListReports(ctx context.Context, traceID, protocol string) ([]v2storage.RawReport, error) {
	return nil, nil
}

func (r *egressRouteRecorder) ListTraceGroups(ctx context.Context, shadowTestName string, limit int) ([]v2storage.TraceGroup, error) {
	return nil, nil
}

func (r *egressRouteRecorder) GetVerdict(ctx context.Context, traceID string) (*v2storage.VerdictState, error) {
	return nil, nil
}

func (r *egressRouteRecorder) ListStaleIncompleteTraces(ctx context.Context, olderThan time.Time) ([]v2storage.StaleIncompleteTrace, error) {
	return nil, nil
}

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handleHealthz(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "ok" {
		t.Fatalf("expected status ok, got %q", out.Status)
	}
}

func TestHealthz_methodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	handleHealthz(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

func TestEgressDiff_acceptsReport(t *testing.T) {
	routeRec := &egressRouteRecorder{}
	s := &Server{Log: slog.Default(), Router: v2engine.NewTraceRouter(routeRec, nil)}

	payload := map[string]any{
		"trace_id": "abc123",
		"workload": "control-a",
		"protocol": "rabbitmq",
		"payload":  map[string]any{"order": 1},
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/egress/diff", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	s.handleEgressDiff(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if !routeRec.routed.Load() {
		t.Fatal("expected router to receive report before 202")
	}
}

func TestEgressDiff_appendFailureReturns503(t *testing.T) {
	routeRec := &egressRouteRecorder{appendErr: errors.New("wal write failed")}
	s := &Server{Log: slog.Default(), Router: v2engine.NewTraceRouter(routeRec, nil)}

	payload := map[string]any{
		"trace_id": "abc123",
		"workload": "control-a",
		"protocol": "rabbitmq",
		"payload":  map[string]any{"order": 1},
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/egress/diff", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	s.handleEgressDiff(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rr.Code)
	}
	if routeRec.routed.Load() {
		t.Fatal("must not mark routed on append failure")
	}
}

// postEgressDiff sends one report; Route is sync so the report is present on 202.
func postEgressDiff(t *testing.T, body map[string]any) *v2storage.RawReport {
	t.Helper()
	routeRec := &egressRouteRecorder{}
	s := &Server{Log: slog.Default(), Router: v2engine.NewTraceRouter(routeRec, nil)}

	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/egress/diff", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	s.handleEgressDiff(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	got := routeRec.last.Load()
	if got == nil {
		t.Fatal("expected router to receive report")
	}
	return got
}

// A producer that decoded the wire protocol itself is authoritative for the
// signature; Beru must store it verbatim rather than re-deriving one from the
// payload's key order.
func TestEgressDiff_usesSuppliedSignature(t *testing.T) {
	got := postEgressDiff(t, map[string]any{
		"trace_id":  "4bf92f3577b34da6a3ce929d0e0e4736",
		"workload":  "candidate",
		"protocol":  "postgresql",
		"signature": "postgresql:select:users",
		// raw_query sorts before "target", so derivation would key the signature
		// on the SQL text and change whenever a literal in it changes.
		"payload": map[string]any{"raw_query": "SELECT * FROM users", "target": "users"},
	})
	if got.Signature != "postgresql:select:users" {
		t.Fatalf("Signature = %q want postgresql:select:users", got.Signature)
	}
}

func TestEgressDiff_derivesSignatureWhenAbsent(t *testing.T) {
	got := postEgressDiff(t, map[string]any{
		"trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
		"workload": "control-a",
		"protocol": "mongodb",
		"payload":  map[string]any{"insert": "orders"},
	})
	if got.Signature != "mongodb:insert:orders" {
		t.Fatalf("Signature = %q want mongodb:insert:orders", got.Signature)
	}
}

type memTraceRepo struct {
	reports []v2storage.RawReport
	verdict *v2storage.VerdictState
}

func (m *memTraceRepo) AppendReport(context.Context, *v2storage.RawReport) ([]v2storage.RawReport, error) {
	return nil, nil
}
func (m *memTraceRepo) SaveDiffVerdict(context.Context, string, *v2storage.VerdictState) error {
	return nil
}
func (m *memTraceRepo) ListReports(_ context.Context, traceID, protocol string) ([]v2storage.RawReport, error) {
	var out []v2storage.RawReport
	for _, r := range m.reports {
		if r.TraceID == traceID && (protocol == "" || r.Protocol == protocol) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (m *memTraceRepo) ListTraceGroups(context.Context, string, int) ([]v2storage.TraceGroup, error) {
	return nil, nil
}
func (m *memTraceRepo) GetVerdict(context.Context, string) (*v2storage.VerdictState, error) {
	return m.verdict, nil
}
func (m *memTraceRepo) ListStaleIncompleteTraces(context.Context, time.Time) ([]v2storage.StaleIncompleteTrace, error) {
	return nil, nil
}

func TestGetTrace_returnsReportsAndVerdict(t *testing.T) {
	repo := &memTraceRepo{
		reports: []v2storage.RawReport{{
			TraceID: "t1", Protocol: "mongodb", Direction: v2storage.DirectionEgress,
			ShadowRole: "control-a", Signature: "mongodb:insert:orders",
		}},
		verdict: &v2storage.VerdictState{Status: v2storage.StatusMatch},
	}
	s := &Server{Log: slog.Default(), Repo: repo}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/traces/t1?protocol=mongodb", nil)
	rr := httptest.NewRecorder()
	s.handleGetTrace(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		TraceID  string                  `json:"trace_id"`
		Protocol string                  `json:"protocol"`
		Reports  []v2storage.RawReport   `json:"reports"`
		Verdict  *v2storage.VerdictState `json:"verdict"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.TraceID != "t1" || out.Protocol != "mongodb" || len(out.Reports) != 1 {
		t.Fatalf("unexpected response: %+v", out)
	}
	if out.Verdict == nil || out.Verdict.Status != v2storage.StatusMatch {
		t.Fatalf("verdict = %+v", out.Verdict)
	}
}

func TestGetTrace_filtersHTTPDirection(t *testing.T) {
	repo := &memTraceRepo{
		reports: []v2storage.RawReport{
			{TraceID: "t1", Protocol: "http", Direction: v2storage.DirectionIngress, ShadowRole: "control-a", Signature: "http:POST:/a"},
			{TraceID: "t1", Protocol: "http", Direction: v2storage.DirectionEgress, ShadowRole: "control-a", Signature: "http:GET:/ext"},
		},
	}
	s := &Server{Log: slog.Default(), Repo: repo}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/traces/t1?protocol=http&direction=egress", nil)
	rr := httptest.NewRecorder()
	s.handleGetTrace(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Direction string                `json:"direction"`
		Reports   []v2storage.RawReport `json:"reports"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Direction != "egress" || len(out.Reports) != 1 || out.Reports[0].Signature != "http:GET:/ext" {
		t.Fatalf("unexpected: %+v", out)
	}
}

func TestGetTrace_requiresProtocol(t *testing.T) {
	s := &Server{Log: slog.Default(), Repo: &memTraceRepo{}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/traces/t1", nil)
	rr := httptest.NewRecorder()
	s.handleGetTrace(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rr.Code)
	}
}

func TestSeedReports_acceptsBatch(t *testing.T) {
	routeRec := &egressRouteRecorder{}
	s := &Server{Log: slog.Default(), Router: v2engine.NewTraceRouter(routeRec, nil)}

	body := map[string]any{
		"reports": []map[string]any{
			{
				"trace_id":    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"shadow_role": "control-a",
				"protocol":    "mongodb",
				"direction":   "egress",
				"signature":   "mongodb:insert:orders",
				"payload":     map[string]any{"price": 10},
			},
			{
				"trace_id":    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"shadow_role": "control-b",
				"protocol":    "mongodb",
				"direction":   "egress",
				"signature":   "mongodb:insert:orders",
				"payload":     map[string]any{"price": 10},
			},
			{
				"trace_id":    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"shadow_role": "candidate",
				"protocol":    "mongodb",
				"direction":   "egress",
				"signature":   "mongodb:insert:orders",
				"payload":     map[string]any{"price": 20},
			},
		},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/debug/seed-reports", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	s.handleSeedReports(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if !routeRec.routed.Load() {
		t.Fatal("expected router to receive seeded report before 202")
	}
}
