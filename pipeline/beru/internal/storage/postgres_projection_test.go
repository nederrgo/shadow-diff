package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shadow-diff/beru/internal/roles"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

// The projection is Postgres-only: the tables Tusk reads are rebuilt on every
// SaveDiffVerdict from raw_reports plus the verdict.
func TestPostgresProjection(t *testing.T) {
	store := newPostgresBackend(t)
	ctx := context.Background()
	trace := "proj-trace"
	t0 := time.Now().UTC().Truncate(time.Millisecond)

	// HTTP ingress for all three roles: candidate returns 500 where controls agree on 200.
	for _, r := range []struct {
		role, status string
	}{
		{roles.ControlA, "200"},
		{roles.ControlB, "200"},
		{roles.Candidate, "500"},
	} {
		if _, err := store.AppendReport(ctx, report(trace, r.role, "http",
			v2storage.DirectionIngress, "http:POST:/v1/orders", `{"ok":true}`, r.status, t0)); err != nil {
			t.Fatal(err)
		}
	}
	// One egress bucket, with a non-JSON candidate payload to exercise the wrap.
	for _, r := range []struct{ role, payload string }{
		{roles.ControlA, `{"insert":"orders","n":1}`},
		{roles.ControlB, `{"insert":"orders","n":1}`},
		{roles.Candidate, `not json at all`},
	} {
		if _, err := store.AppendReport(ctx, report(trace, r.role, "mongodb",
			v2storage.DirectionEgress, "mongodb:insert:orders", r.payload, "", t0.Add(time.Second))); err != nil {
			t.Fatal(err)
		}
	}

	details := v2storage.VerdictDetails{
		Flags: []string{v2storage.FlagMismatchPayload},
		Steps: []v2storage.VerdictStep{{
			Kind:      v2storage.FlagMismatchPayload,
			Protocol:  "mongodb",
			Signature: "mongodb:insert:orders",
			Detail:    "n differs",
		}},
		Baseline: &v2storage.BaselineFailure{Reason: "none"},
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDiffVerdict(ctx, trace, &v2storage.VerdictState{
		Status:             v2storage.StatusMismatch,
		HasCountRegression: true,
		SummaryDetails:     string(encoded),
		UpdatedAt:          t0,
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("traces row carries the request line and per-role status", func(t *testing.T) {
		var (
			sessionID, method, path    string
			statusA, statusB, statusC  string
			verdict, shadowTest        string
		)
		if err := store.db.QueryRowContext(ctx, `
SELECT session_id, shadow_test_name, method, path,
       status_code_a, status_code_b, status_code_candidate, verdict
FROM traces WHERE trace_id = $1`, trace,
		).Scan(&sessionID, &shadowTest, &method, &path, &statusA, &statusB, &statusC, &verdict); err != nil {
			t.Fatal(err)
		}
		if method != "POST" || path != "/v1/orders" {
			t.Fatalf("method/path = %q %q, want POST /v1/orders", method, path)
		}
		if statusA != "200" || statusB != "200" || statusC != "500" {
			t.Fatalf("status codes = %q/%q/%q, want 200/200/500", statusA, statusB, statusC)
		}
		if verdict != v2storage.StatusMismatch {
			t.Fatalf("verdict = %q, want MISMATCH", verdict)
		}
		if sessionID != "conformance-session" {
			t.Fatalf("session_id = %q, want conformance-session", sessionID)
		}
		if shadowTest != "default" {
			t.Fatalf("shadow_test_name = %q, want default", shadowTest)
		}
	})

	t.Run("one diff_reports row per signature bucket", func(t *testing.T) {
		rows, err := store.db.QueryContext(ctx, `
SELECT signature, source_type FROM diff_reports WHERE trace_id = $1 ORDER BY signature`, trace)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var sig, src string
			if err := rows.Scan(&sig, &src); err != nil {
				t.Fatal(err)
			}
			got[sig] = src
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"http:POST:/v1/orders":   "http/ingress",
			"mongodb:insert:orders": "mongodb/egress",
		}
		if len(got) != len(want) {
			t.Fatalf("buckets = %v, want %v", got, want)
		}
		for sig, src := range want {
			if got[sig] != src {
				t.Fatalf("source_type[%s] = %q, want %q", sig, got[sig], src)
			}
		}
	})

	t.Run("payloads and diffs land as JSONB", func(t *testing.T) {
		var a, b, candidate, noise, regression []byte
		if err := store.db.QueryRowContext(ctx, `
SELECT control_a_payload, control_b_payload, candidate_payload, noise_diff, regression_diff
FROM diff_reports WHERE trace_id = $1 AND signature = $2`, trace, "mongodb:insert:orders",
		).Scan(&a, &b, &candidate, &noise, &regression); err != nil {
			t.Fatal(err)
		}
		var controlA map[string]any
		if err := json.Unmarshal(a, &controlA); err != nil {
			t.Fatalf("control_a_payload is not JSON: %v", err)
		}
		if controlA["insert"] != "orders" {
			t.Fatalf("control_a_payload = %v, want insert=orders", controlA)
		}
		if string(b) == "" {
			t.Fatal("control_b_payload is empty")
		}
		// The candidate payload was not JSON; it must survive wrapped, not dropped.
		var wrapped map[string]string
		if err := json.Unmarshal(candidate, &wrapped); err != nil {
			t.Fatalf("candidate_payload is not JSON: %v", err)
		}
		if wrapped["_raw"] != "not json at all" {
			t.Fatalf("candidate_payload = %v, want _raw wrapper", wrapped)
		}

		var gotSteps []v2storage.VerdictStep
		if err := json.Unmarshal(regression, &gotSteps); err != nil {
			t.Fatalf("regression_diff is not JSON: %v", err)
		}
		if len(gotSteps) != 1 || gotSteps[0].Detail != "n differs" {
			t.Fatalf("regression_diff = %+v, want the mongodb step", gotSteps)
		}
		var gotBaseline v2storage.BaselineFailure
		if err := json.Unmarshal(noise, &gotBaseline); err != nil {
			t.Fatalf("noise_diff is not JSON: %v", err)
		}
		if gotBaseline.Reason != "none" {
			t.Fatalf("noise_diff = %+v, want reason none", gotBaseline)
		}
	})

	t.Run("the http bucket gets no regression steps", func(t *testing.T) {
		var regression []byte
		if err := store.db.QueryRowContext(ctx, `
SELECT regression_diff FROM diff_reports WHERE trace_id = $1 AND signature = $2`,
			trace, "http:POST:/v1/orders").Scan(&regression); err != nil {
			t.Fatal(err)
		}
		if regression != nil {
			t.Fatalf("regression_diff = %s, want NULL for a bucket with no steps", regression)
		}
	})

	t.Run("re-saving updates in place", func(t *testing.T) {
		if err := store.SaveDiffVerdict(ctx, trace, &v2storage.VerdictState{
			Status:    v2storage.StatusMatch,
			UpdatedAt: t0.Add(time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM diff_reports WHERE trace_id = $1`, trace).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("diff_reports rows = %d, want 2 (updated, not duplicated)", n)
		}
		var verdict string
		if err := store.db.QueryRowContext(ctx,
			`SELECT verdict FROM traces WHERE trace_id = $1`, trace).Scan(&verdict); err != nil {
			t.Fatal(err)
		}
		if verdict != v2storage.StatusMatch {
			t.Fatalf("verdict = %q, want MATCH after re-save", verdict)
		}
	})

	t.Run("verdicts carries the shadow test name", func(t *testing.T) {
		// Many beru-local pods share one database; without this column,
		// "every verdict for ShadowTest X" needs a join through raw_reports.
		var shadowTest string
		if err := store.db.QueryRowContext(ctx,
			`SELECT shadow_test_name FROM verdicts WHERE trace_id = $1`, trace).Scan(&shadowTest); err != nil {
			t.Fatal(err)
		}
		if shadowTest != "default" {
			t.Fatalf("verdicts.shadow_test_name = %q, want default", shadowTest)
		}
	})

	t.Run("the session row is recorded", func(t *testing.T) {
		var namespace, mode, shadowTest string
		if err := store.db.QueryRowContext(ctx, `
SELECT shadow_test_name, namespace, mode FROM shadow_sessions WHERE session_id = $1`,
			"conformance-session").Scan(&shadowTest, &namespace, &mode); err != nil {
			t.Fatal(err)
		}
		if namespace != "shadow-default-conformance" || mode != "record" {
			t.Fatalf("session = %q/%q, want shadow-default-conformance/record", namespace, mode)
		}
	})
}
