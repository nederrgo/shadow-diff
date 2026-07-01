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

	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2report "github.com/shadow-diff/beru/internal/v2/report"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

type wireRouteRecorder struct {
	last atomic.Pointer[v2storage.RawReport]
}

func (r *wireRouteRecorder) AppendReport(_ context.Context, report *v2storage.RawReport) ([]v2storage.RawReport, error) {
	r.last.Store(report)
	return []v2storage.RawReport{*report}, nil
}

func (r *wireRouteRecorder) SaveDiffVerdict(_ context.Context, _ string, _ *v2storage.VerdictState) error {
	return nil
}

func (r *wireRouteRecorder) ListReports(_ context.Context, _, _ string) ([]v2storage.RawReport, error) {
	return nil, nil
}

func (r *wireRouteRecorder) ListTraceGroups(_ context.Context, _ string, _ int) ([]v2storage.TraceGroup, error) {
	return nil, nil
}

func (r *wireRouteRecorder) GetVerdict(_ context.Context, _ string) (*v2storage.VerdictState, error) {
	return nil, nil
}

func waitWireReport(t *testing.T, rec *wireRouteRecorder) *v2storage.RawReport {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := rec.last.Load(); got != nil {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected routed report")
	return nil
}

func TestHandleWireIngest_http(t *testing.T) {
	var rec wireRouteRecorder
	router := v2engine.NewTraceRouter(1, &rec, nil)
	s := &Server{Log: slog.Default(), Router: router}

	body, _ := json.Marshal(v2report.NetworkEventEnvelope{
		TraceID:            "4bf92f3577b34da6a3ce929d0e0e4736",
		PodRole:            "control-a",
		Protocol:           "http",
		Direction:          "egress",
		RawRequestPayload:  `{}`,
		RawResponsePayload: `{}`,
		Metadata:           `{"method":"POST","path":"/v1/charges"}`,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/wire", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleWireIngest(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	got := waitWireReport(t, &rec)
	if got.Signature != "http:POST:/v1/charges" {
		t.Fatalf("signature = %q", got.Signature)
	}
}

func TestHandleWireIngest_mongodb(t *testing.T) {
	var rec wireRouteRecorder
	router := v2engine.NewTraceRouter(1, &rec, nil)
	s := &Server{Log: slog.Default(), Router: router}

	body, _ := json.Marshal(v2report.NetworkEventEnvelope{
		TraceID:           "4bf92f3577b34da6a3ce929d0e0e4736",
		PodRole:           "candidate",
		Protocol:          "mongodb",
		RawRequestPayload: `{"insert":"orders","documents":[{"id":1}]}`,
		Metadata:          `{"command":"insert","collection":"orders"}`,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/wire", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleWireIngest(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status %d", rr.Code)
	}
	got := waitWireReport(t, &rec)
	if got.Signature != "mongodb:insert:orders" {
		t.Fatalf("got %+v", got)
	}
}
