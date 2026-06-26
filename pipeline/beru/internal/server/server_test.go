package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
	"github.com/shadow-diff/beru/internal/roles"
)

type routeRecorder struct {
	done atomic.Bool
}

func (r *routeRecorder) AppendReport(ctx context.Context, report *v2storage.RawReport) ([]v2storage.RawReport, error) {
	r.done.Store(true)
	return []v2storage.RawReport{*report}, nil
}

func (r *routeRecorder) SaveDiffVerdict(ctx context.Context, traceID string, verdict *v2storage.VerdictState) error {
	return nil
}

func (r *routeRecorder) ListReports(ctx context.Context, traceID, protocol string) ([]v2storage.RawReport, error) {
	return nil, nil
}

func (r *routeRecorder) ListTraceGroups(ctx context.Context, shadowTestName string, limit int) ([]v2storage.TraceGroup, error) {
	return nil, nil
}

func (r *routeRecorder) GetVerdict(ctx context.Context, traceID string) (*v2storage.VerdictState, error) {
	return nil, nil
}

func TestReportTraffic(t *testing.T) {
	rec := &routeRecorder{}
	router := v2engine.NewTraceRouter(1, rec, nil)
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
	deadline := time.After(2 * time.Second)
	for !rec.done.Load() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for router")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
