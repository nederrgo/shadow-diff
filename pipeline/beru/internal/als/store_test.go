package als

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	accesslogv3 "github.com/envoyproxy/go-control-plane/envoy/data/accesslog/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"github.com/shadow-diff/beru/internal/ingest"
	"github.com/shadow-diff/beru/internal/roles"
	"google.golang.org/protobuf/types/known/structpb"
)

func testLogBuffer() (*bytes.Buffer, *slog.Logger) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return &buf, log
}

func mongoTCPEntry(role string, queryJSON string) *accesslogv3.TCPAccessLogEntry {
	st, _ := structpb.NewStruct(map[string]any{
		"request": queryJSON,
	})
	return &accesslogv3.TCPAccessLogEntry{
		CommonProperties: &accesslogv3.AccessLogCommon{
			StreamId:   "conn-42",
			CustomTags: map[string]string{tagShadowRole: role},
			Metadata: &corev3.Metadata{
				FilterMetadata: map[string]*structpb.Struct{
					mongoFilterMetadata: st,
				},
			},
		},
	}
}

func mongoTCPEntryWithOps(role string) *accesslogv3.TCPAccessLogEntry {
	st, _ := structpb.NewStruct(map[string]any{
		"test.items": []any{"insert"},
	})
	return &accesslogv3.TCPAccessLogEntry{
		CommonProperties: &accesslogv3.AccessLogCommon{
			StreamId:   "conn-42",
			CustomTags: map[string]string{tagShadowRole: role},
			Metadata: &corev3.Metadata{
				FilterMetadata: map[string]*structpb.Struct{
					mongoFilterMetadata: st,
				},
			},
		},
	}
}

func TestStore_UpsertByConnectionID(t *testing.T) {
	buf, log := testLogBuffer()
	cfg := ingest.Config{TraceTTL: 30 * time.Second, MaxPendingTraces: 100, SweepInterval: time.Hour}
	store := NewStore(log, cfg)

	q1 := `{"ops":"partial"}`
	q2 := `{"ops":"final"}`
	for _, role := range roles.All {
		e1 := mongoTCPEntry(role, q1)
		e1.CommonProperties.StreamId = "conn-1"
		store.Handle(role, "", e1)
		e2 := mongoTCPEntry(role, q2)
		e2.CommonProperties.StreamId = "conn-1"
		store.Handle(role, "", e2)
	}
	store.NotifyIngressComplete("trace-upsert")
	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(buf.String(), "No egress regression for Trace trace-upsert (mongodb)") {
		t.Fatalf("expected single upserted entry per role to diff, got: %s", buf.String())
	}
}

func TestStore_SkipIntermediateLogEntry(t *testing.T) {
	buf, log := testLogBuffer()
	cfg := ingest.Config{TraceTTL: 30 * time.Second, MaxPendingTraces: 100, SweepInterval: time.Hour}
	store := NewStore(log, cfg)

	q := `{"op":"insert"}`
	for _, role := range roles.All {
		store.Handle(role, "", &accesslogv3.TCPAccessLogEntry{
			CommonProperties: &accesslogv3.AccessLogCommon{
				IntermediateLogEntry: true,
				CustomTags:           map[string]string{tagShadowRole: role},
			},
		})
	}
	store.NotifyIngressComplete("trace-intermediate")
	if strings.Contains(buf.String(), "egress regression") {
		t.Fatalf("intermediate-only entries must not trigger diff: %s", buf.String())
	}

	for _, role := range roles.All {
		store.Handle(role, "", mongoTCPEntry(role, q))
	}
	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(buf.String(), "No egress regression for Trace trace-intermediate (mongodb)") {
		t.Fatalf("expected diff after final entries, got: %s", buf.String())
	}
}

func TestStore_ConnectionBytesFallback(t *testing.T) {
	buf, log := testLogBuffer()
	cfg := ingest.Config{TraceTTL: 30 * time.Second, MaxPendingTraces: 100, SweepInterval: time.Hour}
	store := NewStore(log, cfg)

	for _, role := range roles.All {
		store.Handle(role, "", &accesslogv3.TCPAccessLogEntry{
			CommonProperties: &accesslogv3.AccessLogCommon{
				StreamId: "conn-1",
			},
			ConnectionProperties: &accesslogv3.ConnectionProperties{
				ReceivedBytes: 120,
				SentBytes:     240,
			},
		})
	}
	store.NotifyIngressComplete("trace-bytes")
	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(buf.String(), "No egress regression for Trace trace-bytes (mongodb)") {
		t.Fatalf("expected mongo egress success log, got: %s", buf.String())
	}
}

func TestStore_StreamRoleFromLogName(t *testing.T) {
	buf, log := testLogBuffer()
	cfg := ingest.Config{TraceTTL: 30 * time.Second, MaxPendingTraces: 100, SweepInterval: time.Hour}
	store := NewStore(log, cfg)

	for _, role := range roles.All {
		entry := mongoTCPEntryWithOps(role)
		entry.CommonProperties.CustomTags = nil
		store.Handle(role, "", entry)
	}
	store.NotifyIngressComplete("trace-stream-role")
	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(buf.String(), "No egress regression for Trace trace-stream-role (mongodb)") {
		t.Fatalf("expected mongo egress success log, got: %s", buf.String())
	}
}

func TestStore_DynamicMetadataOpsFormat(t *testing.T) {
	buf, log := testLogBuffer()
	cfg := ingest.Config{TraceTTL: 30 * time.Second, MaxPendingTraces: 100, SweepInterval: time.Hour}
	store := NewStore(log, cfg)

	for _, role := range roles.All {
		store.Handle(role, "", mongoTCPEntryWithOps(role))
	}
	store.NotifyIngressComplete("trace-ops")
	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(buf.String(), "No egress regression for Trace trace-ops (mongodb)") {
		t.Fatalf("expected mongo egress success log, got: %s", buf.String())
	}
}

func TestStore_NoDiffBeforeIngressComplete(t *testing.T) {
	buf, log := testLogBuffer()
	cfg := ingest.Config{TraceTTL: 30 * time.Second, MaxPendingTraces: 100, SweepInterval: time.Hour}
	store := NewStore(log, cfg)

	q := `{"op":"insert","doc":{"k":"v"}}`
	for _, role := range roles.All {
		store.Handle(role, "", mongoTCPEntry(role, q))
	}
	if strings.Contains(buf.String(), "egress regression") {
		t.Fatalf("unexpected diff before ingress complete: %s", buf.String())
	}

	store.NotifyIngressComplete("trace-1")
	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(buf.String(), "No egress regression for Trace trace-1 (mongodb)") {
		t.Fatalf("expected mongo egress success log, got: %s", buf.String())
	}
}

func TestStore_DiffAfterLateALS(t *testing.T) {
	buf, log := testLogBuffer()
	cfg := ingest.Config{TraceTTL: 30 * time.Second, MaxPendingTraces: 100, SweepInterval: time.Hour}
	store := NewStore(log, cfg)

	store.NotifyIngressComplete("trace-late")
	q := `{"op":"insert","doc":{"n":1}}`
	store.Handle(roles.ControlA, "", mongoTCPEntry(roles.ControlA, q))
	store.Handle(roles.ControlB, "", mongoTCPEntry(roles.ControlB, q))
	store.Handle(roles.Candidate, "", mongoTCPEntry(roles.Candidate, q))

	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(buf.String(), "No egress regression for Trace trace-late (mongodb)") {
		t.Fatalf("expected diff after late ALS, got: %s", buf.String())
	}
}

func TestStore_EgressRegression(t *testing.T) {
	buf, log := testLogBuffer()
	cfg := ingest.Config{TraceTTL: 30 * time.Second, MaxPendingTraces: 100, SweepInterval: time.Hour}
	store := NewStore(log, cfg)

	store.NotifyIngressComplete("trace-bad")
	store.Handle(roles.ControlA, "", mongoTCPEntry(roles.ControlA, `{"v":1}`))
	store.Handle(roles.ControlB, "", mongoTCPEntry(roles.ControlB, `{"v":1}`))
	store.Handle(roles.Candidate, "", mongoTCPEntry(roles.Candidate, `{"v":2}`))

	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(buf.String(), "Egress regression for Trace trace-bad (mongodb)") {
		t.Fatalf("expected egress regression, got: %s", buf.String())
	}
}
