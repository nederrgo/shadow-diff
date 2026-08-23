package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shadow-diff/beru/internal/roles"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

func TestPostgres_flushRetryIsIdempotent(t *testing.T) {
	dsn := os.Getenv("BERU_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BERU_TEST_POSTGRES_DSN unset; set it to a throwaway database to run Postgres idempotency test")
	}

	store := newPostgresBackend(t)
	ctx := context.Background()
	trace := "idempotent-retry"
	now := time.Now().UTC().Truncate(time.Millisecond)
	payload := `{"n":1}`
	sig := "mongodb:insert:orders"

	batch := []v2storage.RawReport{
		*reportWithIngestID(trace, roles.ControlA, "mongodb", v2storage.DirectionEgress, sig, payload, "", now, 1001),
		*reportWithIngestID(trace, roles.ControlB, "mongodb", v2storage.DirectionEgress, sig, payload, "", now.Add(time.Millisecond), 1002),
		*reportWithIngestID(trace, roles.Candidate, "mongodb", v2storage.DirectionEgress, sig, payload, "", now.Add(2*time.Millisecond), 1003),
	}

	if err := store.flushReportsAndEvaluate(ctx, batch, nil, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	countAfterFirst, err := countRawReports(ctx, store, trace)
	if err != nil {
		t.Fatal(err)
	}
	if countAfterFirst != 3 {
		t.Fatalf("row count after first flush = %d, want 3", countAfterFirst)
	}

	verdictAfterFirst, err := store.GetVerdict(ctx, trace)
	if err != nil {
		t.Fatal(err)
	}
	if verdictAfterFirst == nil || verdictAfterFirst.Status != v2storage.StatusMatch {
		t.Fatalf("verdict after first flush = %+v, want MATCH", verdictAfterFirst)
	}

	// Simulate WAL delete failure: same ingest_ids flushed again.
	if err := store.flushReportsAndEvaluate(ctx, batch, nil, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	countAfterRetry, err := countRawReports(ctx, store, trace)
	if err != nil {
		t.Fatal(err)
	}
	if countAfterRetry != 3 {
		t.Fatalf("row count after retry = %d, want 3 (no duplicates)", countAfterRetry)
	}

	verdictAfterRetry, err := store.GetVerdict(ctx, trace)
	if err != nil {
		t.Fatal(err)
	}
	if verdictAfterRetry == nil || verdictAfterRetry.Status != v2storage.StatusMatch {
		t.Fatalf("verdict after retry = %+v, want MATCH", verdictAfterRetry)
	}
}

func TestPostgres_duplicatePayloadsDistinct(t *testing.T) {
	dsn := os.Getenv("BERU_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BERU_TEST_POSTGRES_DSN unset; set it to a throwaway database to run Postgres idempotency test")
	}

	store := newPostgresBackend(t)
	ctx := context.Background()
	trace := "duplicate-payload"
	now := time.Now().UTC().Truncate(time.Millisecond)
	payload := `{"same":true}`
	sig := "mongodb:insert:orders"

	batch := []v2storage.RawReport{
		*reportWithIngestID(trace, roles.ControlA, "mongodb", v2storage.DirectionEgress, sig, payload, "", now, 2001),
		*reportWithIngestID(trace, roles.ControlA, "mongodb", v2storage.DirectionEgress, sig, payload, "", now.Add(time.Millisecond), 2002),
	}

	if err := store.flushReportsAndEvaluate(ctx, batch, nil, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	count, err := countRawReports(ctx, store, trace)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("row count = %d, want 2 distinct rows for duplicate payloads", count)
	}
}

func reportWithIngestID(traceID, role, protocol string, dir v2storage.PayloadDirection,
	signature, payload, statusCode string, at time.Time, ingestID uint64) *v2storage.RawReport {
	rep := report(traceID, role, protocol, dir, signature, payload, statusCode, at)
	rep.IngestID = ingestID
	return rep
}

func countRawReports(ctx context.Context, store *PostgresStore, traceID string) (int, error) {
	var n int
	err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM raw_reports WHERE replay_execution_id = $1 AND trace_id = $2`,
		store.replayExecutionID, traceID,
	).Scan(&n)
	return n, err
}
