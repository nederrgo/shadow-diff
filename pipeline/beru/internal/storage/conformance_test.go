package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shadow-diff/beru/internal/roles"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

func TestPostgresConformance(t *testing.T) {
	store := newPostgresBackend(t)
	runTraceRepositoryConformance(t, store)
	runRunStoreConformance(t, store)
}

// newPostgresBackend needs a throwaway database: it drops every Beru object
// before migrating so each run starts clean.
func newPostgresBackend(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("BERU_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BERU_TEST_POSTGRES_DSN unset; set it to a throwaway database to run Postgres conformance")
	}
	cfg := configFromDSN(t, dsn)

	raw, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
DROP TABLE IF EXISTS diff_reports, traces, replay_executions, shadow_sessions, noise_filters,
                     shadow_tests, verdicts, raw_reports, schema_migrations CASCADE;
DROP TYPE IF EXISTS verdict_kind;`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	raw.Close()

	t.Setenv("SESSION_ID", "conformance-session")
	t.Setenv("REPLAY_EXECUTION_ID", "exec-conformance")
	t.Setenv("SHADOW_NAMESPACE", "shadow-default-conformance")
	t.Setenv("SHADOW_MODE", "record")

	store, err := OpenPostgres(slog.Default(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func configFromDSN(t *testing.T, dsn string) PostgresConfig {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("BERU_TEST_POSTGRES_DSN is not a URL: %v", err)
	}
	password, _ := u.User.Password()
	cfg := PostgresConfig{
		Host:     u.Hostname(),
		User:     u.User.Username(),
		Password: password,
		Name:     strings.TrimPrefix(u.Path, "/"),
		Port:     u.Port(),
		SSLMode:  u.Query().Get("sslmode"),
	}
	if cfg.Port == "" {
		cfg.Port = defaultPostgresPort
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = defaultPostgresSSLMode
	}
	return cfg
}

func runTraceRepositoryConformance(t *testing.T, repo v2storage.TraceRepository) {
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)

	t.Run("AppendReport builds an ordered timeline", func(t *testing.T) {
		trace := "conf-timeline"
		history, err := repo.AppendReport(ctx, report(trace, roles.ControlA, "mongodb",
			v2storage.DirectionEgress, "mongodb:insert:orders", `{"n":1}`, "", t0))
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 1 {
			t.Fatalf("history len = %d, want 1", len(history))
		}

		if _, err := repo.AppendReport(ctx, report(trace, roles.Candidate, "mongodb",
			v2storage.DirectionEgress, "mongodb:insert:orders", `{"n":2}`, "", t0.Add(2*time.Second))); err != nil {
			t.Fatal(err)
		}
		// Inserted last but captured first: ordering must be by captured_at.
		history, err = repo.AppendReport(ctx, report(trace, roles.ControlB, "mongodb",
			v2storage.DirectionEgress, "mongodb:insert:orders", `{"n":3}`, "", t0.Add(-time.Second)))
		if err != nil {
			t.Fatal(err)
		}
		gotRoles := []string{history[0].ShadowRole, history[1].ShadowRole, history[2].ShadowRole}
		wantRoles := []string{roles.ControlB, roles.ControlA, roles.Candidate}
		if !reflect.DeepEqual(gotRoles, wantRoles) {
			t.Fatalf("order = %v, want %v", gotRoles, wantRoles)
		}
		if !history[1].CapturedAt.Equal(t0) {
			t.Fatalf("captured_at = %v, want %v", history[1].CapturedAt, t0)
		}
		if string(history[0].PayloadBytes) != `{"n":3}` {
			t.Fatalf("payload = %q, want {\"n\":3}", history[0].PayloadBytes)
		}
	})

	t.Run("ListReports filters by protocol", func(t *testing.T) {
		trace := "conf-protocol"
		for _, p := range []struct{ protocol, sig string }{
			{"http", "http:GET:/health"},
			{"mongodb", "mongodb:find:users"},
		} {
			if _, err := repo.AppendReport(ctx, report(trace, roles.ControlA, p.protocol,
				v2storage.DirectionEgress, p.sig, `{}`, "", t0)); err != nil {
				t.Fatal(err)
			}
		}
		got, err := repo.ListReports(ctx, trace, "mongodb")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Protocol != "mongodb" {
			t.Fatalf("filtered reports = %+v, want one mongodb row", got)
		}
	})

	t.Run("status codes round-trip", func(t *testing.T) {
		trace := "conf-status"
		if _, err := repo.AppendReport(ctx, report(trace, roles.ControlA, "http",
			v2storage.DirectionIngress, "http:GET:/x", `{}`, "404", t0)); err != nil {
			t.Fatal(err)
		}
		got, err := repo.ListReports(ctx, trace, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].StatusCode != "404" {
			t.Fatalf("got %+v, want status_code 404", got)
		}
	})

	t.Run("SaveDiffVerdict upserts", func(t *testing.T) {
		trace := "conf-verdict"
		if err := repo.SaveDiffVerdict(ctx, trace, &v2storage.VerdictState{
			Status:    v2storage.StatusMatch,
			UpdatedAt: t0,
		}); err != nil {
			t.Fatal(err)
		}
		got, err := repo.GetVerdict(ctx, trace)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != v2storage.StatusMatch || got.HasCountRegression {
			t.Fatalf("initial verdict = %+v, want MATCH / false", got)
		}
		if !got.UpdatedAt.Equal(t0) {
			t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, t0)
		}

		details := v2storage.VerdictDetails{
			Flags: []string{v2storage.FlagMismatchPayload},
			Steps: []v2storage.VerdictStep{{
				Kind:      v2storage.FlagMismatchPayload,
				Protocol:  "mongodb",
				Signature: "mongodb:insert:orders",
				NoisePath: "created_at",
			}},
		}
		encoded, err := json.Marshal(details)
		if err != nil {
			t.Fatal(err)
		}
		t1 := t0.Add(time.Minute)
		if err := repo.SaveDiffVerdict(ctx, trace, &v2storage.VerdictState{
			Status:             v2storage.StatusMismatch,
			HasCountRegression: true,
			SummaryDetails:     string(encoded),
			UpdatedAt:          t1,
		}); err != nil {
			t.Fatal(err)
		}
		got, err = repo.GetVerdict(ctx, trace)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != v2storage.StatusMismatch || !got.HasCountRegression {
			t.Fatalf("updated verdict = %+v", got)
		}
		if !got.UpdatedAt.Equal(t1) {
			t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, t1)
		}
		// Compared semantically: Postgres JSONB reformats the text it stores.
		var roundTripped v2storage.VerdictDetails
		if err := json.Unmarshal([]byte(got.SummaryDetails), &roundTripped); err != nil {
			t.Fatalf("summary_details is not JSON: %v (%q)", err, got.SummaryDetails)
		}
		if !reflect.DeepEqual(roundTripped, details) {
			t.Fatalf("summary_details = %+v, want %+v", roundTripped, details)
		}
		if !reflect.DeepEqual(got.Flags, details.Flags) {
			t.Fatalf("flags = %v, want %v", got.Flags, details.Flags)
		}
	})

	t.Run("GetVerdict is nil for an unknown trace", func(t *testing.T) {
		got, err := repo.GetVerdict(ctx, "conf-missing")
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("verdict = %+v, want nil", got)
		}
	})

	t.Run("ListStaleIncompleteTraces finds missing roles", func(t *testing.T) {
		old := time.Now().UTC().Add(-30 * time.Second)
		fresh := time.Now().UTC()
		if _, err := repo.AppendReport(ctx, report("conf-stale", roles.ControlA, "mongodb",
			v2storage.DirectionEgress, "mongodb:x:y", `{}`, "", old)); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.AppendReport(ctx, report("conf-fresh", roles.ControlA, "mongodb",
			v2storage.DirectionEgress, "mongodb:x:y", `{}`, "", fresh)); err != nil {
			t.Fatal(err)
		}
		// All three roles present: complete, so never stale however old it is.
		for _, role := range roles.All {
			if _, err := repo.AppendReport(ctx, report("conf-complete", role, "mongodb",
				v2storage.DirectionEgress, "mongodb:x:y", `{}`, "", old)); err != nil {
				t.Fatal(err)
			}
		}
		// Foreign shadow test + already-verdicted incomplete must not be reaped
		// (would re-stamp SESSION_ID onto another beru-local's UI session).
		if _, err := repo.AppendReport(ctx, reportForTest("conf-foreign", "other-test",
			roles.ControlA, "mongodb", v2storage.DirectionEgress, "mongodb:x:y", `{}`, "", old)); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.AppendReport(ctx, report("conf-verdicted", roles.ControlA, "mongodb",
			v2storage.DirectionEgress, "mongodb:x:y", `{}`, "", old)); err != nil {
			t.Fatal(err)
		}
		if err := repo.SaveDiffVerdict(ctx, "conf-verdicted", &v2storage.VerdictState{
			Status:    v2storage.StatusWaitingForRoles,
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}

		stale, err := repo.ListStaleIncompleteTraces(ctx, time.Now().UTC().Add(-10*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		seen := make(map[string]bool, len(stale))
		for _, s := range stale {
			seen[s.TraceID] = true
		}
		if !seen["conf-stale"] {
			t.Fatalf("stale = %+v, want conf-stale included", stale)
		}
		if seen["conf-fresh"] {
			t.Fatal("fresh trace reported as stale")
		}
		if seen["conf-complete"] {
			t.Fatal("complete trace reported as stale")
		}
		if seen["conf-foreign"] {
			t.Fatal("foreign shadow test reported as stale")
		}
		if seen["conf-verdicted"] {
			t.Fatal("already-verdicted incomplete reported as stale")
		}
	})

	t.Run("ListTraceGroups scopes to a shadow test", func(t *testing.T) {
		if _, err := repo.AppendReport(ctx, reportForTest("conf-group", "conformance-test",
			roles.ControlA, "http", v2storage.DirectionIngress, "http:GET:/g", `{}`, "200", t0)); err != nil {
			t.Fatal(err)
		}
		groups, err := repo.ListTraceGroups(ctx, "conformance-test", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(groups) != 1 || groups[0].TraceID != "conf-group" || groups[0].Protocol != "http" {
			t.Fatalf("groups = %+v, want one conf-group/http row", groups)
		}
		if _, err := time.Parse(time.RFC3339Nano, groups[0].LastCapturedAt); err != nil {
			t.Fatalf("last_captured_at %q is not RFC3339Nano: %v", groups[0].LastCapturedAt, err)
		}
	})
}

func runRunStoreConformance(t *testing.T, runs RunStore) {
	ctx := context.Background()

	t.Run("EnsureShadowTest is idempotent", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			if err := runs.EnsureShadowTest(ctx, "conf-run"); err != nil {
				t.Fatal(err)
			}
		}
		list, err := runs.ListShadowTests(ctx, 50)
		if err != nil {
			t.Fatal(err)
		}
		var found int
		var id int64
		for _, st := range list {
			if st.Name == "conf-run" {
				found++
				id = st.ID
			}
		}
		if found != 1 {
			t.Fatalf("conf-run rows = %d, want 1", found)
		}
		got, err := runs.GetShadowTest(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "conf-run" {
			t.Fatalf("GetShadowTest = %+v, want conf-run", got)
		}
		if got.StartTime == "" {
			t.Fatal("StartTime is empty")
		}
	})

	t.Run("noise filters deduplicate", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			if err := runs.AddNoiseFilter(ctx, "conf-noise", "response.timestamp"); err != nil {
				t.Fatal(err)
			}
		}
		if err := runs.AddNoiseFilter(ctx, "conf-noise", "response.request_id"); err != nil {
			t.Fatal(err)
		}
		list, err := runs.ListNoiseFilters(ctx, "conf-noise")
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 {
			t.Fatalf("filters = %v, want 2", list)
		}
		paths, err := runs.NoisePathsForTest(ctx, "conf-noise")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := paths["response.timestamp"]; !ok || len(paths) != 2 {
			t.Fatalf("paths = %v, want both entries", paths)
		}
	})

	t.Run("DefaultShadowTestName is never empty", func(t *testing.T) {
		if runs.DefaultShadowTestName() == "" {
			t.Fatal("default shadow test name is empty")
		}
	})
}

func report(traceID, role, protocol string, dir v2storage.PayloadDirection,
	signature, payload, statusCode string, at time.Time) *v2storage.RawReport {
	return reportForTest(traceID, "default", role, protocol, dir, signature, payload, statusCode, at)
}

func reportForTest(traceID, shadowTest, role, protocol string, dir v2storage.PayloadDirection,
	signature, payload, statusCode string, at time.Time) *v2storage.RawReport {
	return &v2storage.RawReport{
		TraceID:        traceID,
		ShadowRole:     role,
		ShadowTestName: shadowTest,
		Protocol:       protocol,
		Direction:      dir,
		Signature:      signature,
		StatusCode:     statusCode,
		PayloadBytes:   []byte(payload),
		CapturedAt:     at,
		IngestID:       testIngestSeq.Add(1),
	}
}

var testIngestSeq atomic.Uint64
