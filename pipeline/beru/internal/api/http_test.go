package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shadow-diff/beru/internal/replay"
	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

type egressRouteRecorder struct {
	routed atomic.Bool
}

func (r *egressRouteRecorder) AppendReport(ctx context.Context, report *v2storage.RawReport) ([]v2storage.RawReport, error) {
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

func TestSeedMock_roundTrip(t *testing.T) {
	mocks := replay.NewMockStore()
	s := &Server{Log: slog.Default(), Mocks: mocks}

	payload := map[string]any{
		"method": "POST",
		"host":   "api.example.com",
		"path":   "/v1/orders",
		"body":   map[string]any{"amount": 100, "timestamp": "ignore"},
		"ignore_paths": []string{"$.timestamp"},
		"response": map[string]any{
			"status":  200,
			"headers": map[string]string{"content-type": "application/json"},
			"body":    `{"ok":true}`,
		},
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/seed_mock", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	s.handleSeedMock(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Hash == "" {
		t.Fatal("expected hash in response")
	}
	if _, ok := mocks.Get(out.Hash); !ok {
		t.Fatal("mock not stored")
	}
}

func TestRecordEgress_roundTrip(t *testing.T) {
	mocks := replay.NewMockStore()
	s := &Server{Log: slog.Default(), Mocks: mocks}

	payload := map[string]any{
		"method": "POST",
		"host":   "httpbin.org",
		"path":   "/post",
		"body":   map[string]any{"e2e_record": 1},
		"response": map[string]any{
			"status":  200,
			"headers": map[string]string{"content-type": "application/json"},
			"body":    `{"recorded":true}`,
		},
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/record_egress", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	s.handleRecordEgress(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Hash == "" {
		t.Fatal("expected hash in response")
	}
	if _, ok := mocks.Get(out.Hash); !ok {
		t.Fatal("mock not stored")
	}
}

func TestEgressDiff_acceptsReport(t *testing.T) {
	routeRec := &egressRouteRecorder{}
	s := &Server{Log: slog.Default(), Router: v2engine.NewTraceRouter(1, routeRec, nil)}

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
	deadline := time.Now().Add(2 * time.Second)
	for !routeRec.routed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("expected router to receive report")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEgressDiff_rejectsInvalidWorkload(t *testing.T) {
	routeRec := &egressRouteRecorder{}
	s := &Server{Log: slog.Default(), Router: v2engine.NewTraceRouter(1, routeRec, nil)}

	payload := map[string]any{
		"trace_id": "abc123",
		"workload": "unknown",
		"protocol": "rabbitmq",
		"payload":  map[string]any{"order": 1},
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/egress/diff", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	s.handleEgressDiff(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
}
