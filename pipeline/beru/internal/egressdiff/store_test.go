package egressdiff

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestStore_threeReportsTriggersDiff(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := Config{TraceTTL: time.Minute, MaxPendingTraces: 100, SweepInterval: time.Hour, EgressWait: time.Second}
	store := NewStore(log, cfg)

	store.Handle(Report{TraceID: "t1", Workload: "control-a", Protocol: "rabbitmq", Payload: []byte(`{"v":1}`)})
	store.Handle(Report{TraceID: "t1", Workload: "control-b", Protocol: "rabbitmq", Payload: []byte(`{"v":1}`)})
	store.Handle(Report{TraceID: "t1", Workload: "candidate", Protocol: "rabbitmq", Payload: []byte(`{"v":1}`)})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "No egress regression for Trace t1 (rabbitmq)") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected success log, got: %s", buf.String())
}

func TestStore_waitTimeoutWithTwoReports(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := Config{TraceTTL: time.Minute, MaxPendingTraces: 100, SweepInterval: time.Hour, EgressWait: 50 * time.Millisecond}
	store := NewStore(log, cfg)

	store.Handle(Report{TraceID: "t2", Workload: "control-a", Protocol: "rabbitmq", Payload: []byte(`{"v":1}`)})
	store.Handle(Report{TraceID: "t2", Workload: "control-b", Protocol: "rabbitmq", Payload: []byte(`{"v":1}`)})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "missing candidate") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected timeout log, got: %s", buf.String())
}
