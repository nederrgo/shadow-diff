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

func TestStore_appendsMultipleSpansPerRole(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := Config{TraceTTL: time.Minute, MaxPendingTraces: 100, SweepInterval: time.Hour, EgressWait: time.Second}
	store := NewStore(log, cfg)

	insert := []byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`)
	extra := []byte(`{"insert":"orders","documents":[{"audit":"n1"}]}`)

	store.Handle(Report{TraceID: "t3", Workload: "control-a", Protocol: "mongodb", Payload: insert})
	store.Handle(Report{TraceID: "t3", Workload: "control-b", Protocol: "mongodb", Payload: insert})
	store.Handle(Report{TraceID: "t3", Workload: "candidate", Protocol: "mongodb", Payload: insert})
	store.Handle(Report{TraceID: "t3", Workload: "candidate", Protocol: "mongodb", Payload: extra})

	deadline := time.Now().Add(2 * time.Second)
	want := "Egress count regression for Trace t3 (mongodb): expected 1 query but got 2"
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected count regression log, got: %s", buf.String())
}

func TestStore_multiProtocolIsolation(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := Config{TraceTTL: time.Minute, MaxPendingTraces: 100, SweepInterval: time.Hour, EgressWait: time.Second}
	store := NewStore(log, cfg)

	mongo := []byte(`{"insert":"c","documents":[{"id":1}]}`)
	rmq := []byte(`{"v":1}`)

	for _, role := range []string{"control-a", "control-b", "candidate"} {
		store.Handle(Report{TraceID: "t4", Workload: role, Protocol: "mongodb", Payload: mongo})
		store.Handle(Report{TraceID: "t4", Workload: role, Protocol: "rabbitmq", Payload: rmq})
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		logs := buf.String()
		if strings.Contains(logs, "No egress regression for Trace t4 (mongodb)") &&
			strings.Contains(logs, "No egress regression for Trace t4 (rabbitmq)") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected both protocol success logs, got: %s", buf.String())
}
