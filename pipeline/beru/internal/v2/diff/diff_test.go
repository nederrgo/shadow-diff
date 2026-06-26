package diff

import (
	"strings"
	"testing"
	"time"

	"github.com/shadow-diff/beru/internal/v2/storage"
)

func TestEvaluateTraceHistory_outOfOrderProtocols_match(t *testing.T) {
	t0 := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Second)

	history := []storage.RawReport{
		{
			ShadowRole:   "control-a",
			Protocol:     "mongodb",
			Signature:    "mongodb:find:orders",
			PayloadBytes: []byte(`{"q":1}`),
			CapturedAt:   t0,
		},
		{
			ShadowRole:   "control-a",
			Protocol:     "rabbitmq",
			Signature:    "rabbitmq:publish:order.created",
			PayloadBytes: []byte(`{"id":1}`),
			CapturedAt:   t1,
		},
		{
			ShadowRole:   "candidate",
			Protocol:     "rabbitmq",
			Signature:    "rabbitmq:publish:order.created",
			PayloadBytes: []byte(`{"id":1}`),
			CapturedAt:   t0,
		},
		{
			ShadowRole:   "candidate",
			Protocol:     "mongodb",
			Signature:    "mongodb:find:orders",
			PayloadBytes: []byte(`{"q":1}`),
			CapturedAt:   t1,
		},
	}

	verdict := EvaluateTraceHistory(history)
	if verdict.Status != "MATCH" {
		t.Fatalf("status = %q, want MATCH; details = %q", verdict.Status, verdict.SummaryDetails)
	}
	if verdict.HasCountRegression {
		t.Fatal("expected no count regression")
	}
}

func TestEvaluateTraceHistory_countRegression(t *testing.T) {
	t0 := time.Date(2026, 6, 25, 11, 0, 0, 0, time.UTC)
	sig := "rabbitmq:publish:order.created"

	history := []storage.RawReport{
		{
			ShadowRole:   "control-a",
			Protocol:     "rabbitmq",
			Signature:    sig,
			PayloadBytes: []byte(`{"id":1}`),
			CapturedAt:   t0,
		},
		{
			ShadowRole:   "candidate",
			Protocol:     "rabbitmq",
			Signature:    sig,
			PayloadBytes: []byte(`{"id":1}`),
			CapturedAt:   t0,
		},
		{
			ShadowRole:   "candidate",
			Protocol:     "rabbitmq",
			Signature:    sig,
			PayloadBytes: []byte(`{"id":1}`),
			CapturedAt:   t0.Add(time.Millisecond),
		},
	}

	verdict := EvaluateTraceHistory(history)
	if verdict.Status != "MISMATCH" {
		t.Fatalf("status = %q, want MISMATCH", verdict.Status)
	}
	if !verdict.HasCountRegression {
		t.Fatal("expected HasCountRegression = true")
	}
	if !strings.Contains(verdict.SummaryDetails, "count regression") {
		t.Fatalf("summary = %q, want count regression detail", verdict.SummaryDetails)
	}
	if !strings.Contains(verdict.SummaryDetails, sig) {
		t.Fatalf("summary = %q, want signature %q", verdict.SummaryDetails, sig)
	}
}

func TestEvaluateTraceHistory_payloadMismatch(t *testing.T) {
	t0 := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	sig := "mongodb:insert:orders"

	history := []storage.RawReport{
		{
			ShadowRole:   "control-a",
			Protocol:     "mongodb",
			Signature:    sig,
			PayloadBytes: []byte(`{"v":1}`),
			CapturedAt:   t0,
		},
		{
			ShadowRole:   "candidate",
			Protocol:     "mongodb",
			Signature:    sig,
			PayloadBytes: []byte(`{"v":2}`),
			CapturedAt:   t0,
		},
	}

	verdict := EvaluateTraceHistory(history)
	if verdict.Status != "MISMATCH" {
		t.Fatalf("status = %q, want MISMATCH", verdict.Status)
	}
	if verdict.HasCountRegression {
		t.Fatal("expected HasCountRegression = false")
	}
	if !strings.Contains(verdict.SummaryDetails, "payload mismatch") {
		t.Fatalf("summary = %q, want payload mismatch detail", verdict.SummaryDetails)
	}
}
