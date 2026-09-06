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

	"github.com/shadow-diff/beru/internal/engine"
	"github.com/shadow-diff/beru/internal/model"
	"github.com/shadow-diff/beru/internal/report"
)

type wireRouteRecorder struct {
	last atomic.Pointer[model.RawReport]
}

func (r *wireRouteRecorder) AppendReport(_ context.Context, report *model.RawReport) ([]model.RawReport, error) {
	r.last.Store(report)
	return []model.RawReport{*report}, nil
}

func (r *wireRouteRecorder) SaveDiffVerdict(_ context.Context, _ string, _ *model.VerdictState) error {
	return nil
}

func (r *wireRouteRecorder) ListReports(_ context.Context, _, _ string) ([]model.RawReport, error) {
	return nil, nil
}

func (r *wireRouteRecorder) ListTraceGroups(_ context.Context, _ string, _ int) ([]model.TraceGroup, error) {
	return nil, nil
}

func (r *wireRouteRecorder) GetVerdict(_ context.Context, _ string) (*model.VerdictState, error) {
	return nil, nil
}

func (r *wireRouteRecorder) ListStaleIncompleteTraces(_ context.Context, _ time.Time) ([]model.StaleIncompleteTrace, error) {
	return nil, nil
}

func TestHandleWireIngest_http(t *testing.T) {
	var rec wireRouteRecorder
	router := engine.NewTraceRouter(&rec, nil)
	s := &Server{Log: slog.Default(), Router: router}

	body, _ := json.Marshal(report.NetworkEventEnvelope{
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
	got := rec.last.Load()
	if got == nil {
		t.Fatal("expected routed report before 202")
	}
	if got.Signature != "http:POST:/v1/charges" {
		t.Fatalf("signature = %q", got.Signature)
	}
}

func TestHandleWireIngest_mongodb(t *testing.T) {
	var rec wireRouteRecorder
	router := engine.NewTraceRouter(&rec, nil)
	s := &Server{Log: slog.Default(), Router: router}

	body, _ := json.Marshal(report.NetworkEventEnvelope{
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
	got := rec.last.Load()
	if got == nil {
		t.Fatal("expected routed report before 202")
	}
	if got.Signature != "mongodb:insert:orders" {
		t.Fatalf("got %+v", got)
	}
}
