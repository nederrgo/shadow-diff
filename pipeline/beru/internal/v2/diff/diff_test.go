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

func TestEvaluateTraceHistory_wireHTTPMatch(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	payload := []byte(`{"request":"{}","response":"{}"}`)
	sig := "http:POST:/v1/charges"
	history := []storage.RawReport{
		{TraceID: "abc", ShadowRole: "control-a", Protocol: "http", Signature: sig, PayloadBytes: payload, CapturedAt: t0},
		{TraceID: "abc", ShadowRole: "candidate", Protocol: "http", Signature: sig, PayloadBytes: payload, CapturedAt: t0},
	}
	verdict := EvaluateTraceHistory(history)
	if verdict.Status != "MATCH" {
		t.Fatalf("status = %q, want MATCH", verdict.Status)
	}
}

// TestEvaluateTraceHistory_controlBNoiseCancelsPayloadMismatch verifies that a field
// differing across ALL three roles is treated as noise and not reported as a regression.
func TestEvaluateTraceHistory_controlBNoiseCancelsPayloadMismatch(t *testing.T) {
	t0 := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	sig := "mongodb:insert:items"
	history := []storage.RawReport{
		// control-a and control-b both have the same _id field differ (noise)
		{ShadowRole: "control-a", Protocol: "mongodb", Signature: sig, PayloadBytes: []byte(`{"_id":"aaa","data":"same"}`), CapturedAt: t0},
		{ShadowRole: "control-b", Protocol: "mongodb", Signature: sig, PayloadBytes: []byte(`{"_id":"bbb","data":"same"}`), CapturedAt: t0},
		// candidate has a different _id too — same noise, not a regression
		{ShadowRole: "candidate", Protocol: "mongodb", Signature: sig, PayloadBytes: []byte(`{"_id":"ccc","data":"same"}`), CapturedAt: t0},
	}
	verdict := EvaluateTraceHistory(history)
	// mongoPayloadsEqual already strips _id, so this should be MATCH either way,
	// but the diff-of-diffs noise cancellation must also handle non-mongo protocols.
	if verdict.Status != "MATCH" {
		t.Fatalf("status = %q, want MATCH (noise should be cancelled); details = %q", verdict.Status, verdict.SummaryDetails)
	}
}

// TestEvaluateTraceHistory_controlBNoiseCancelsPayloadMismatch_nonMongo verifies noise
// cancellation for a non-MongoDB protocol where field stripping does not apply.
func TestEvaluateTraceHistory_controlBNoiseCancelsPayloadMismatch_nonMongo(t *testing.T) {
	t0 := time.Date(2026, 7, 6, 11, 0, 0, 0, time.UTC)
	sig := "rabbitmq:publish:events"
	history := []storage.RawReport{
		// A timestamp field naturally differs across all roles — pure noise.
		{ShadowRole: "control-a", Protocol: "rabbitmq", Signature: sig, PayloadBytes: []byte(`{"ts":1000,"v":1}`), CapturedAt: t0},
		{ShadowRole: "control-b", Protocol: "rabbitmq", Signature: sig, PayloadBytes: []byte(`{"ts":2000,"v":1}`), CapturedAt: t0},
		{ShadowRole: "candidate", Protocol: "rabbitmq", Signature: sig, PayloadBytes: []byte(`{"ts":3000,"v":1}`), CapturedAt: t0},
	}
	verdict := EvaluateTraceHistory(history)
	if verdict.Status != "MATCH" {
		t.Fatalf("status = %q, want MATCH (noise cancelled by control-b); details = %q", verdict.Status, verdict.SummaryDetails)
	}
}

// TestEvaluateTraceHistory_realRegressionNotCancelledByNoise verifies that a payload
// difference between control-a and candidate is still reported when control-b matches
// control-a (i.e. the field is deterministic, so candidate is genuinely different).
func TestEvaluateTraceHistory_realRegressionNotCancelledByNoise(t *testing.T) {
	t0 := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	sig := "rabbitmq:publish:orders"
	history := []storage.RawReport{
		{ShadowRole: "control-a", Protocol: "rabbitmq", Signature: sig, PayloadBytes: []byte(`{"v":1}`), CapturedAt: t0},
		{ShadowRole: "control-b", Protocol: "rabbitmq", Signature: sig, PayloadBytes: []byte(`{"v":1}`), CapturedAt: t0},
		// candidate uses v:2 — control-b is identical to control-a, so this is a real regression
		{ShadowRole: "candidate", Protocol: "rabbitmq", Signature: sig, PayloadBytes: []byte(`{"v":2}`), CapturedAt: t0},
	}
	verdict := EvaluateTraceHistory(history)
	if verdict.Status != "MISMATCH" {
		t.Fatalf("status = %q, want MISMATCH (real regression must not be cancelled)", verdict.Status)
	}
	if !strings.Contains(verdict.SummaryDetails, "payload mismatch") {
		t.Fatalf("summary = %q, want payload mismatch detail", verdict.SummaryDetails)
	}
}

// TestEvaluateTraceHistory_controlBNoiseCancelsCountDelta verifies that a count increase
// already present in control-b is not re-reported as a regression for candidate.
func TestEvaluateTraceHistory_controlBNoiseCancelsCountDelta(t *testing.T) {
	t0 := time.Date(2026, 7, 6, 13, 0, 0, 0, time.UTC)
	sig := "rabbitmq:publish:events"
	history := []storage.RawReport{
		// control-a: 1 op
		{ShadowRole: "control-a", Protocol: "rabbitmq", Signature: sig, PayloadBytes: []byte(`{"id":1}`), CapturedAt: t0},
		// control-b: 2 ops — noisy count
		{ShadowRole: "control-b", Protocol: "rabbitmq", Signature: sig, PayloadBytes: []byte(`{"id":1}`), CapturedAt: t0},
		{ShadowRole: "control-b", Protocol: "rabbitmq", Signature: sig, PayloadBytes: []byte(`{"id":1}`), CapturedAt: t0.Add(time.Millisecond)},
		// candidate: 2 ops — same count as control-b noise band → NOT a count regression
		{ShadowRole: "candidate", Protocol: "rabbitmq", Signature: sig, PayloadBytes: []byte(`{"id":1}`), CapturedAt: t0},
		{ShadowRole: "candidate", Protocol: "rabbitmq", Signature: sig, PayloadBytes: []byte(`{"id":1}`), CapturedAt: t0.Add(time.Millisecond)},
	}
	verdict := EvaluateTraceHistory(history)
	if verdict.HasCountRegression {
		t.Fatalf("expected no count regression (candidate count matches control-b noise band); details = %q", verdict.SummaryDetails)
	}
	if verdict.Status != "MATCH" {
		t.Fatalf("status = %q, want MATCH; details = %q", verdict.Status, verdict.SummaryDetails)
	}
}
