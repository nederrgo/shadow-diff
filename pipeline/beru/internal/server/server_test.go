package server

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	"github.com/shadow-diff/beru/internal/ingest"
	"github.com/shadow-diff/beru/internal/roles"
)

func TestReportTraffic(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	store := ingest.NewStore(log, ingest.Config{TraceTTL: time.Minute, MaxPendingTraces: 100, SweepInterval: time.Hour})

	srv := &TrafficReporter{Log: log, Store: store}
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
	time.Sleep(50 * time.Millisecond)
}
