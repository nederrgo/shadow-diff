package diff

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"github.com/shadow-diff/beru/internal/v2/storage"
)

func evalOpts(t0 time.Time) EvalOptions {
	return EvalOptions{Timeout: 10 * time.Second, Now: t0.Add(time.Second)}
}

func triple(roleA, roleB, roleC storage.RawReport) []storage.RawReport {
	return []storage.RawReport{roleA, roleB, roleC}
}

func mongoReport(role, sig string, payload []byte, t0 time.Time) storage.RawReport {
	return storage.RawReport{
		ShadowRole:   role,
		Protocol:     "mongodb",
		Direction:    storage.DirectionEgress,
		Signature:    sig,
		PayloadBytes: payload,
		CapturedAt:   t0,
	}
}

func rabbitmqReport(role, sig string, payload []byte, t0 time.Time) storage.RawReport {
	return storage.RawReport{
		ShadowRole:   role,
		Protocol:     "rabbitmq",
		Direction:    storage.DirectionEgress,
		Signature:    sig,
		PayloadBytes: payload,
		CapturedAt:   t0,
	}
}

func httpIngress(role, sig, status string, payload []byte, t0 time.Time) storage.RawReport {
	return storage.RawReport{
		ShadowRole:   role,
		Protocol:     "http",
		Direction:    storage.DirectionIngress,
		Signature:    sig,
		StatusCode:   status,
		PayloadBytes: payload,
		CapturedAt:   t0,
	}
}

func parseDetails(t *testing.T, summary string) storage.VerdictDetails {
	t.Helper()
	var d storage.VerdictDetails
	if err := json.Unmarshal([]byte(summary), &d); err != nil {
		t.Fatalf("unmarshal details: %v (%q)", err, summary)
	}
	return d
}

func flagPresent(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

func TestEvaluateTraceHistory_outOfOrderProtocols_match(t *testing.T) {
	t0 := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Second)

	history := []storage.RawReport{
		mongoReport("control-a", "mongodb:find:orders", []byte(`{"q":1}`), t0),
		rabbitmqReport("control-a", "rabbitmq:publish:order.created", []byte(`{"id":1}`), t1),
		mongoReport("control-b", "mongodb:find:orders", []byte(`{"q":1}`), t0),
		rabbitmqReport("control-b", "rabbitmq:publish:order.created", []byte(`{"id":1}`), t1),
		rabbitmqReport("candidate", "rabbitmq:publish:order.created", []byte(`{"id":1}`), t0),
		mongoReport("candidate", "mongodb:find:orders", []byte(`{"q":1}`), t1),
	}

	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMatch {
		t.Fatalf("status = %v, want MATCH", verdict)
	}
}

func TestEvaluateTraceHistory_countRegression(t *testing.T) {
	t0 := time.Date(2026, 6, 25, 11, 0, 0, 0, time.UTC)
	sig := "rabbitmq:publish:order.created"
	payload := []byte(`{"id":1}`)

	history := []storage.RawReport{
		rabbitmqReport("control-a", sig, payload, t0),
		rabbitmqReport("control-b", sig, payload, t0),
		rabbitmqReport("candidate", sig, payload, t0),
		rabbitmqReport("candidate", sig, payload, t0.Add(time.Millisecond)),
	}

	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMismatch {
		t.Fatalf("status = %v, want MISMATCH", verdict)
	}
	if !verdict.HasCountRegression {
		t.Fatal("expected HasCountRegression = true")
	}
	details := parseDetails(t, verdict.SummaryDetails)
	if !flagPresent(details.Flags, storage.FlagMismatchCount) {
		t.Fatalf("flags = %v, want MISMATCH_COUNT", details.Flags)
	}
}

func TestEvaluateTraceHistory_payloadMismatch(t *testing.T) {
	t0 := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	sig := "mongodb:insert:orders"

	history := triple(
		mongoReport("control-a", sig, []byte(`{"v":1}`), t0),
		mongoReport("control-b", sig, []byte(`{"v":1}`), t0),
		mongoReport("candidate", sig, []byte(`{"v":2}`), t0),
	)

	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMismatch {
		t.Fatalf("status = %v, want MISMATCH", verdict)
	}
	details := parseDetails(t, verdict.SummaryDetails)
	if !flagPresent(details.Flags, storage.FlagMismatchPayload) {
		t.Fatalf("flags = %v, want MISMATCH_PAYLOAD", details.Flags)
	}
}

func TestEvaluateTraceHistory_wireHTTPMatch(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	payload := []byte(`{"request":"{}","response":"{}"}`)
	sig := "http:POST:/v1/charges"
	history := triple(
		httpIngress("control-a", sig, "200", payload, t0),
		httpIngress("control-b", sig, "200", payload, t0),
		httpIngress("candidate", sig, "200", payload, t0),
	)
	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMatch {
		t.Fatalf("status = %v, want MATCH", verdict)
	}
}

func TestEvaluateTraceHistory_controlBNoiseCancelsPayloadMismatch(t *testing.T) {
	t0 := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	sig := "mongodb:insert:items"
	history := triple(
		mongoReport("control-a", sig, []byte(`{"_id":"aaa","data":"same"}`), t0),
		mongoReport("control-b", sig, []byte(`{"_id":"bbb","data":"same"}`), t0),
		mongoReport("candidate", sig, []byte(`{"_id":"ccc","data":"same"}`), t0),
	)
	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMatch {
		t.Fatalf("status = %v, want MATCH; details = %v", verdict, verdict)
	}
}

func TestEvaluateTraceHistory_controlBNoiseCancelsPayloadMismatch_nonMongo(t *testing.T) {
	t0 := time.Date(2026, 7, 6, 11, 0, 0, 0, time.UTC)
	sig := "rabbitmq:publish:events"
	history := []storage.RawReport{
		rabbitmqReport("control-a", sig, []byte(`{"ts":1000,"v":1}`), t0),
		rabbitmqReport("control-b", sig, []byte(`{"ts":2000,"v":1}`), t0),
		rabbitmqReport("candidate", sig, []byte(`{"ts":3000,"v":1}`), t0),
	}
	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMatch {
		t.Fatalf("status = %v, want MATCH", verdict)
	}
}

func TestEvaluateTraceHistory_realRegressionNotCancelledByNoise(t *testing.T) {
	t0 := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	sig := "rabbitmq:publish:orders"
	history := []storage.RawReport{
		rabbitmqReport("control-a", sig, []byte(`{"v":1}`), t0),
		rabbitmqReport("control-b", sig, []byte(`{"v":1}`), t0),
		rabbitmqReport("candidate", sig, []byte(`{"v":2}`), t0),
	}
	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMismatch {
		t.Fatalf("status = %v, want MISMATCH", verdict)
	}
}

func TestEvaluateTraceHistory_userNoiseSuppressesFieldRegression(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	sig := "rabbitmq:publish:orders"
	history := []storage.RawReport{
		rabbitmqReport("control-a", sig, []byte(`{"price":10,"v":1}`), t0),
		rabbitmqReport("control-b", sig, []byte(`{"price":10,"v":1}`), t0),
		rabbitmqReport("candidate", sig, []byte(`{"price":99,"v":1}`), t0),
	}
	userNoise := map[string]struct{}{"price": {}}
	verdict := EvaluateTraceHistory(history, userNoise, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMatch {
		t.Fatalf("status = %v, want MATCH", verdict)
	}
}

func TestEvaluateTraceHistory_controlCountMismatchVoidsBaseline(t *testing.T) {
	// Formerly noise-band count cancel — baseline now requires A/B count equality.
	t0 := time.Date(2026, 7, 6, 13, 0, 0, 0, time.UTC)
	sig := "rabbitmq:publish:events"
	payload := []byte(`{"id":1}`)
	history := []storage.RawReport{
		rabbitmqReport("control-a", sig, payload, t0),
		rabbitmqReport("control-b", sig, payload, t0),
		rabbitmqReport("control-b", sig, payload, t0.Add(time.Millisecond)),
		rabbitmqReport("candidate", sig, payload, t0),
		rabbitmqReport("candidate", sig, payload, t0.Add(time.Millisecond)),
	}
	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusVoidedBaselineDivergence {
		t.Fatalf("status = %v, want VOIDED_BASELINE_DIVERGENCE", verdict)
	}
}

func TestEvaluateTraceHistory_controlStatusMismatchVoids(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 14, 0, 0, 0, time.UTC)
	sig := "http:GET:/items"
	payload := []byte(`{}`)
	history := triple(
		httpIngress("control-a", sig, "200", payload, t0),
		httpIngress("control-b", sig, "500", payload, t0),
		httpIngress("candidate", sig, "200", payload, t0),
	)
	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusVoidedBaselineDivergence {
		t.Fatalf("status = %v, want VOIDED_BASELINE_DIVERGENCE", verdict)
	}
	details := parseDetails(t, verdict.SummaryDetails)
	if details.Baseline == nil || details.Baseline.Reason != "status_code_mismatch" {
		t.Fatalf("baseline = %+v", details.Baseline)
	}
}

func TestEvaluateTraceHistory_intentionalErrorPath_candidateMismatch(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 14, 10, 0, 0, time.UTC)
	sig := "http:GET:/missing"
	payload := []byte(`{"error":"not found"}`)
	history := triple(
		httpIngress("control-a", sig, "400", payload, t0),
		httpIngress("control-b", sig, "400", payload, t0),
		httpIngress("candidate", sig, "200", []byte(`{"ok":true}`), t0),
	)
	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMismatch {
		t.Fatalf("status = %v, want MISMATCH", verdict)
	}
	details := parseDetails(t, verdict.SummaryDetails)
	if !flagPresent(details.Flags, storage.FlagMismatchPayload) {
		t.Fatalf("flags = %v, want MISMATCH_PAYLOAD", details.Flags)
	}
	foundStatus := false
	for _, s := range details.Steps {
		if s.Reason == storage.ReasonStatusCode {
			foundStatus = true
		}
	}
	if !foundStatus {
		t.Fatalf("steps = %+v, want STATUS_CODE step", details.Steps)
	}
}

func TestEvaluateTraceHistory_compoundPayloadAndCount(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 15, 0, 0, 0, time.UTC)
	sig := "mongodb:insert:orders"
	history := []storage.RawReport{
		mongoReport("control-a", sig, []byte(`{"price":10}`), t0),
		mongoReport("control-b", sig, []byte(`{"price":10}`), t0),
		mongoReport("candidate", sig, []byte(`{"price":20}`), t0),
		mongoReport("candidate", sig, []byte(`{"price":1}`), t0.Add(time.Millisecond)),
	}
	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMismatch {
		t.Fatalf("status = %v, want MISMATCH", verdict)
	}
	details := parseDetails(t, verdict.SummaryDetails)
	if !flagPresent(details.Flags, storage.FlagMismatchPayload) || !flagPresent(details.Flags, storage.FlagMismatchCount) {
		t.Fatalf("flags = %v, want both PAYLOAD and COUNT", details.Flags)
	}
	var havePayload, haveCount bool
	for _, s := range details.Steps {
		if s.Kind == storage.FlagMismatchPayload {
			havePayload = true
		}
		if s.Kind == storage.FlagMismatchCount && s.Reason == storage.ReasonUnexpectedExtraEgress {
			haveCount = true
		}
	}
	if !havePayload || !haveCount {
		t.Fatalf("steps = %+v, want payload + count", details.Steps)
	}
}

func TestEvaluateTraceHistory_waitingForRoles(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 16, 0, 0, 0, time.UTC)
	sig := "mongodb:find:x"
	history := []storage.RawReport{
		mongoReport("control-a", sig, []byte(`{}`), t0),
		mongoReport("control-b", sig, []byte(`{}`), t0),
	}

	within := EvaluateTraceHistory(history, nil, EvalOptions{Timeout: 10 * time.Second, Now: t0.Add(time.Second)})
	if within != nil {
		t.Fatalf("within timeout: got %+v, want nil", within)
	}

	timedOut := EvaluateTraceHistory(history, nil, EvalOptions{Timeout: 10 * time.Second, Now: t0.Add(11 * time.Second)})
	if timedOut == nil || timedOut.Status != storage.StatusWaitingForRoles {
		t.Fatalf("past timeout: got %+v, want WAITING_FOR_ROLES", timedOut)
	}
}

func TestEvaluateTraceHistory_lateArrivalOverridesWaiting(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 16, 30, 0, 0, time.UTC)
	sig := "mongodb:find:x"
	payload := []byte(`{"q":1}`)
	incomplete := []storage.RawReport{
		mongoReport("control-a", sig, payload, t0),
		mongoReport("control-b", sig, payload, t0),
	}
	waiting := EvaluateTraceHistory(incomplete, nil, EvalOptions{Timeout: time.Second, Now: t0.Add(2 * time.Second)})
	if waiting == nil || waiting.Status != storage.StatusWaitingForRoles {
		t.Fatalf("want WAITING_FOR_ROLES, got %+v", waiting)
	}

	complete := append(incomplete, mongoReport("candidate", sig, payload, t0.Add(3*time.Second)))
	verdict := EvaluateTraceHistory(complete, nil, EvalOptions{Timeout: time.Second, Now: t0.Add(4 * time.Second)})
	if verdict == nil || verdict.Status != storage.StatusMatch {
		t.Fatalf("late arrival should MATCH, got %+v", verdict)
	}
}

func TestEvaluateTraceHistory_userNoiseOnCompoundKeepsCountOnly(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 17, 0, 0, 0, time.UTC)
	sig := "mongodb:insert:orders"
	history := []storage.RawReport{
		mongoReport("control-a", sig, []byte(`{"price":10}`), t0),
		mongoReport("control-b", sig, []byte(`{"price":10}`), t0),
		mongoReport("candidate", sig, []byte(`{"price":20}`), t0),
		mongoReport("candidate", sig, []byte(`{"price":1}`), t0.Add(time.Millisecond)),
	}
	userNoise := map[string]struct{}{"price": {}}
	verdict := EvaluateTraceHistory(history, userNoise, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMismatch {
		t.Fatalf("status = %v, want MISMATCH (count still flags)", verdict)
	}
	details := parseDetails(t, verdict.SummaryDetails)
	if flagPresent(details.Flags, storage.FlagMismatchPayload) {
		t.Fatalf("payload should be suppressed by noise; flags=%v", details.Flags)
	}
	if !flagPresent(details.Flags, storage.FlagMismatchCount) {
		t.Fatalf("flags = %v, want MISMATCH_COUNT", details.Flags)
	}
}

func TestEvaluateTraceHistory_nonHTTPEmptyStatusDoesNotVoid(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 18, 0, 0, 0, time.UTC)
	sig := "mongodb:insert:orders"
	history := triple(
		mongoReport("control-a", sig, []byte(`{"v":1}`), t0),
		mongoReport("control-b", sig, []byte(`{"v":1}`), t0),
		mongoReport("candidate", sig, []byte(`{"v":1}`), t0),
	)
	// StatusCode left empty on all — must MATCH, not void.
	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMatch {
		t.Fatalf("status = %v, want MATCH", verdict)
	}
}

func TestEvaluateTraceHistory_detailsContainSignature(t *testing.T) {
	t0 := time.Now().UTC()
	sig := "rabbitmq:publish:x"
	history := []storage.RawReport{
		rabbitmqReport("control-a", sig, []byte(`{}`), t0),
		rabbitmqReport("control-b", sig, []byte(`{}`), t0),
		rabbitmqReport("candidate", sig, []byte(`{}`), t0),
		rabbitmqReport("candidate", "rabbitmq:publish:extra", []byte(`{}`), t0),
	}
	verdict := EvaluateTraceHistory(history, nil, evalOpts(t0))
	if verdict == nil || verdict.Status != storage.StatusMismatch {
		t.Fatalf("want MISMATCH, got %+v", verdict)
	}
	if !strings.Contains(verdict.SummaryDetails, storage.FlagMismatchSignature) {
		t.Fatalf("details missing signature flag: %s", verdict.SummaryDetails)
	}
}
