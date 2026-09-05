package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shadow-diff/beru/internal/engine"
	"github.com/shadow-diff/beru/internal/model"
	"github.com/shadow-diff/beru/internal/roles"
	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type routeRecorder struct {
	done      atomic.Bool
	appendErr error
}

func (r *routeRecorder) AppendReport(ctx context.Context, report *model.RawReport) ([]model.RawReport, error) {
	if r.appendErr != nil {
		return nil, r.appendErr
	}
	r.done.Store(true)
	return []model.RawReport{*report}, nil
}

func (r *routeRecorder) SaveDiffVerdict(ctx context.Context, traceID string, verdict *model.VerdictState) error {
	return nil
}

func (r *routeRecorder) ListReports(ctx context.Context, traceID, protocol string) ([]model.RawReport, error) {
	return nil, nil
}

func (r *routeRecorder) ListTraceGroups(ctx context.Context, shadowTestName string, limit int) ([]model.TraceGroup, error) {
	return nil, nil
}

func (r *routeRecorder) GetVerdict(ctx context.Context, traceID string) (*model.VerdictState, error) {
	return nil, nil
}

func (r *routeRecorder) ListStaleIncompleteTraces(ctx context.Context, olderThan time.Time) ([]model.StaleIncompleteTrace, error) {
	return nil, nil
}

func TestReportTraffic(t *testing.T) {
	rec := &routeRecorder{}
	router := engine.NewTraceRouter(rec, nil)
	srv := &TrafficReporter{Router: router}
	_, err := srv.ReportTraffic(context.Background(), &beruv1.ReportTrafficRequest{
		Report: &beruv1.TrafficReport{
			TraceId:   "t1",
			Role:      roles.Candidate,
			Direction: beruv1.Direction_INGRESS,
			Payload: &beruv1.Payload{
				Body:        []byte(`{"x":1}`),
				ContentType: "application/json",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rec.done.Load() {
		t.Fatal("expected append before ReportTraffic returns")
	}
}

func TestReportTraffic_appendFailure(t *testing.T) {
	rec := &routeRecorder{appendErr: errors.New("wal write failed")}
	router := engine.NewTraceRouter(rec, nil)
	srv := &TrafficReporter{Router: router}
	_, err := srv.ReportTraffic(context.Background(), &beruv1.ReportTrafficRequest{
		Report: &beruv1.TrafficReport{
			TraceId:   "t1",
			Role:      roles.Candidate,
			Direction: beruv1.Direction_INGRESS,
			Payload: &beruv1.Payload{
				Body:        []byte(`{"x":1}`),
				ContentType: "application/json",
			},
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %v, want Unavailable", status.Code(err))
	}
}
