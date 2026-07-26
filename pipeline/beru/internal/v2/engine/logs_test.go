package engine

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/shadow-diff/beru/internal/v2/storage"
)

func TestMirrorLegacyLogs_httpEgressMatch(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	tid := "4bf92f3577b34da6a3ce929d0e0e4736"
	payload := []byte(`{"method":"GET","host":"h","path":"/dep/echo","status":200,"body":""}`)
	history := []storage.RawReport{
		httpEgressReport(tid, "control-a", "http:GET:/dep/echo", payload),
		httpEgressReport(tid, "control-b", "http:GET:/dep/echo", payload),
		httpEgressReport(tid, "candidate", "http:GET:/dep/echo", payload),
	}
	mirrorLegacyLogs(tid, history, &storage.VerdictState{Status: storage.StatusMatch})

	log := buf.String()
	want := "No egress regression for Trace " + tid + " (http)"
	if !strings.Contains(log, want) {
		t.Fatalf("missing %q in log:\n%s", want, log)
	}
	// Alone, egress must not emit the ingress match line.
	if strings.Contains(log, "msg=\"No regression for Trace "+tid+"\"") {
		t.Fatalf("unexpected ingress match log:\n%s", log)
	}
}

func TestMirrorLegacyLogs_httpIngressAndEgress(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	tid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	egressBody := []byte(`{"method":"GET","path":"/dep/echo","status":200,"body":""}`)
	ingressBody := []byte(`{"ok":true}`)
	history := []storage.RawReport{
		httpIngressReport(tid, "control-a", "http:GET:/egress/run", ingressBody),
		httpIngressReport(tid, "control-b", "http:GET:/egress/run", ingressBody),
		httpIngressReport(tid, "candidate", "http:GET:/egress/run", ingressBody),
		httpEgressReport(tid, "control-a", "http:GET:/dep/echo", egressBody),
		httpEgressReport(tid, "control-b", "http:GET:/dep/echo", egressBody),
		httpEgressReport(tid, "candidate", "http:GET:/dep/echo", egressBody),
	}
	mirrorLegacyLogs(tid, history, &storage.VerdictState{Status: storage.StatusMatch})

	log := buf.String()
	if !strings.Contains(log, "No regression for Trace "+tid) {
		t.Fatalf("missing ingress match log:\n%s", log)
	}
	if !strings.Contains(log, "No egress regression for Trace "+tid+" (http)") {
		t.Fatalf("missing egress match log:\n%s", log)
	}
}

func httpEgressReport(tid, role, sig string, payload []byte) storage.RawReport {
	return storage.RawReport{
		TraceID:      tid,
		ShadowRole:   role,
		Protocol:     "http",
		Direction:    storage.DirectionEgress,
		Signature:    sig,
		PayloadBytes: payload,
	}
}

func httpIngressReport(tid, role, sig string, payload []byte) storage.RawReport {
	return storage.RawReport{
		TraceID:      tid,
		ShadowRole:   role,
		Protocol:     "http",
		Direction:    storage.DirectionIngress,
		Signature:    sig,
		PayloadBytes: payload,
		StatusCode:   "200",
	}
}
