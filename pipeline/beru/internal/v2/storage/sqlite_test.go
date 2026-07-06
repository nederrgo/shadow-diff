package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func testRepo(t *testing.T) *SQLiteRepository {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v2.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })

	repo, err := NewSQLiteRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestAppendReport_growsTimelineInChronologicalOrder(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	traceID := "trace-abc"
	t0 := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Second)
	t2 := t0.Add(2 * time.Second)

	reportA := &RawReport{
		TraceID:      traceID,
		ShadowRole:   "control-a",
		Protocol:     "mongodb",
		Direction:    DirectionEgress,
		Signature:    "mongodb:insert:orders",
		PayloadBytes: []byte(`{"n":1}`),
		CapturedAt:   t0,
	}
	history, err := repo.AppendReport(ctx, reportA)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	if history[0].Signature != reportA.Signature {
		t.Fatalf("history[0].Signature = %q, want %q", history[0].Signature, reportA.Signature)
	}

	reportB := &RawReport{
		TraceID:      traceID,
		ShadowRole:   "candidate",
		Protocol:     "mongodb",
		Direction:    DirectionEgress,
		Signature:    "mongodb:insert:orders",
		PayloadBytes: []byte(`{"n":2}`),
		CapturedAt:   t1,
	}
	history, err = repo.AppendReport(ctx, reportB)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	if history[0].ShadowRole != "control-a" || history[1].ShadowRole != "candidate" {
		t.Fatalf("history order = [%q, %q], want [control-a, candidate]", history[0].ShadowRole, history[1].ShadowRole)
	}

	reportC := &RawReport{
		TraceID:      traceID,
		ShadowRole:   "control-b",
		Protocol:     "http",
		Direction:    DirectionIngress,
		Signature:    "http:GET:/health",
		PayloadBytes: []byte(`{}`),
		CapturedAt:   t0.Add(-time.Second),
	}
	history, err = repo.AppendReport(ctx, reportC)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3", len(history))
	}
	if history[0].ShadowRole != "control-b" {
		t.Fatalf("history[0].ShadowRole = %q, want control-b", history[0].ShadowRole)
	}
	if !history[1].CapturedAt.Equal(t0) || !history[2].CapturedAt.Equal(t1) {
		t.Fatalf("unexpected captured_at order: %v, %v, %v", history[0].CapturedAt, history[1].CapturedAt, history[2].CapturedAt)
	}

	reportD := &RawReport{
		TraceID:      traceID,
		ShadowRole:   "late-same-time",
		Protocol:     "rabbitmq",
		Direction:    DirectionEgress,
		Signature:    "rabbitmq:publish:order.created",
		PayloadBytes: []byte(`{"id":1}`),
		CapturedAt:   t2,
	}
	reportE := &RawReport{
		TraceID:      traceID,
		ShadowRole:   "also-same-time",
		Protocol:     "rabbitmq",
		Direction:    DirectionEgress,
		Signature:    "rabbitmq:publish:order.created",
		PayloadBytes: []byte(`{"id":2}`),
		CapturedAt:   t2,
	}
	if _, err := repo.AppendReport(ctx, reportD); err != nil {
		t.Fatal(err)
	}
	history, err = repo.AppendReport(ctx, reportE)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 5 {
		t.Fatalf("history len = %d, want 5", len(history))
	}
	if history[3].ShadowRole != "late-same-time" || history[4].ShadowRole != "also-same-time" {
		t.Fatalf("tie-break order = [%q, %q], want [late-same-time, also-same-time]", history[3].ShadowRole, history[4].ShadowRole)
	}
}

func TestSaveDiffVerdict_upserts(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	traceID := "trace-verdict"
	t0 := time.Date(2026, 6, 25, 13, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	if err := repo.SaveDiffVerdict(ctx, traceID, &VerdictState{
		Status:             "MATCH",
		HasCountRegression: false,
		SummaryDetails:     "",
		UpdatedAt:          t0,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := readVerdict(ctx, repo.db, traceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "MATCH" || got.HasCountRegression || got.SummaryDetails != "" {
		t.Fatalf("initial verdict = %+v, want MATCH/false/empty", got)
	}
	if !got.UpdatedAt.Equal(t0) {
		t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, t0)
	}

	if err := repo.SaveDiffVerdict(ctx, traceID, &VerdictState{
		Status:             "MISMATCH",
		HasCountRegression: true,
		SummaryDetails:     "extra step on mongodb:insert:orders",
		UpdatedAt:          t1,
	}); err != nil {
		t.Fatal(err)
	}

	got, err = readVerdict(ctx, repo.db, traceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "MISMATCH" || !got.HasCountRegression || got.SummaryDetails != "extra step on mongodb:insert:orders" {
		t.Fatalf("updated verdict = %+v", got)
	}
	if !got.UpdatedAt.Equal(t1) {
		t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, t1)
	}

	var count int
	if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM verdicts WHERE trace_id = ?`, traceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("verdict row count = %d, want 1", count)
	}
}

func readVerdict(ctx context.Context, db *sql.DB, traceID string) (VerdictState, error) {
	var (
		got        VerdictState
		regression int
		updated    string
	)
	err := db.QueryRowContext(ctx, `
SELECT status, has_count_regression, summary_details, updated_at
FROM verdicts WHERE trace_id = ?`, traceID).Scan(&got.Status, &regression, &got.SummaryDetails, &updated)
	if err != nil {
		return VerdictState{}, err
	}
	got.HasCountRegression = regression != 0
	got.UpdatedAt, err = parseTime(updated)
	return got, err
}
