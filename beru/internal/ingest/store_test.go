package ingest

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	"github.com/shadow-diff/beru/internal/roles"
)

func TestStore_completesOnThreeRoles(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := Config{TraceTTL: time.Minute, MaxPendingTraces: 100, SweepInterval: time.Hour}
	s := NewStore(log, cfg)

	body := []byte(`{"ok":true}`)
	for _, role := range roles.All {
		s.Handle(&beruv1.TrafficReport{
			TraceId:   "trace-1",
			Role:      role,
			Direction: beruv1.Direction_INGRESS,
			Payload:   &beruv1.Payload{Body: body, ContentType: "application/json"},
		})
	}
	time.Sleep(100 * time.Millisecond)
	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("No regression")) && !bytes.Contains([]byte(out), []byte("Regression found")) {
		t.Fatalf("expected diff log, got: %s", out)
	}
}

func TestStore_evictsOldestWhenCapReached(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := Config{TraceTTL: time.Hour, MaxPendingTraces: 2, SweepInterval: time.Hour}
	s := NewStore(log, cfg)

	body := []byte(`{"ok":true}`)
	for _, id := range []string{"trace-1", "trace-2", "trace-3"} {
		s.Handle(&beruv1.TrafficReport{
			TraceId:   id,
			Role:      roles.ControlA,
			Direction: beruv1.Direction_INGRESS,
			Payload:   &beruv1.Payload{Body: body, ContentType: "application/json"},
		})
	}

	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("Ingest Map Full - Evicting Trace trace-1")) {
		t.Fatalf("expected cap eviction log for trace-1, got: %s", out)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pending["trace-1"]; ok {
		t.Fatal("trace-1 should have been evicted")
	}
	if _, ok := s.pending["trace-3"]; !ok {
		t.Fatal("trace-3 should remain in pending map")
	}
}

func TestStore_timeoutLogsMissingRole(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := Config{TraceTTL: 50 * time.Millisecond, MaxPendingTraces: 100, SweepInterval: 20 * time.Millisecond}
	s := NewStore(log, cfg)

	s.Handle(&beruv1.TrafficReport{
		TraceId:   "trace-timeout",
		Role:      roles.ControlA,
		Direction: beruv1.Direction_INGRESS,
		Payload:   &beruv1.Payload{Body: []byte(`{}`), ContentType: "application/json"},
	})
	time.Sleep(200 * time.Millisecond)
	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("Timed out waiting for Trace trace-timeout")) {
		t.Fatalf("expected timeout log, got: %s", out)
	}
	if !bytes.Contains([]byte(out), []byte("missing")) || !bytes.Contains([]byte(out), []byte("candidate")) {
		t.Fatalf("expected missing candidate in log: %s", out)
	}
}
