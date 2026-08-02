package storage

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shadow-diff/beru/internal/roles"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

// TestPostgres_concurrentFlushSameTrace simulates two Beru pods flushing the
// same trace_id at once. pg_advisory_xact_lock must serialize so the final
// timeline has all three roles and a coherent MATCH verdict.
func TestPostgres_concurrentFlushSameTrace(t *testing.T) {
	dsn := os.Getenv("BERU_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BERU_TEST_POSTGRES_DSN unset; set it to a throwaway database to run Postgres race test")
	}

	storeA := newPostgresBackend(t)
	storeB := openSecondPostgres(t, configFromDSN(t, dsn))

	const rounds = 20
	payload := `{"v":1}`
	sig := "mongodb:insert:orders"
	now := time.Now().UTC().Truncate(time.Millisecond)

	for i := 0; i < rounds; i++ {
		traceID := fmt.Sprintf("race-trace-%02d", i)
		repA := *report(traceID, roles.ControlA, "mongodb",
			v2storage.DirectionEgress, sig, payload, "", now)
		repB := *report(traceID, roles.ControlB, "mongodb",
			v2storage.DirectionEgress, sig, payload, "", now.Add(time.Millisecond))
		repC := *report(traceID, roles.Candidate, "mongodb",
			v2storage.DirectionEgress, sig, payload, "", now.Add(2*time.Millisecond))

		start := make(chan struct{})
		errCh := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			errCh <- storeA.flushReportsAndEvaluate(ctx, []v2storage.RawReport{repA}, nil, 10*time.Second)
		}()
		go func() {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			errCh <- storeB.flushReportsAndEvaluate(ctx, []v2storage.RawReport{repB, repC}, nil, 10*time.Second)
		}()

		close(start)
		wg.Wait()
		close(errCh)
		for err := range errCh {
			if err != nil {
				t.Fatalf("round %d flush: %v", i, err)
			}
		}

		ctx := context.Background()
		history, err := storeA.ListReports(ctx, traceID, "mongodb")
		if err != nil {
			t.Fatalf("round %d ListReports: %v", i, err)
		}
		if len(history) != 3 {
			t.Fatalf("round %d history len = %d, want 3", i, len(history))
		}
		seen := map[string]bool{}
		for _, r := range history {
			seen[r.ShadowRole] = true
		}
		for _, role := range []string{roles.ControlA, roles.ControlB, roles.Candidate} {
			if !seen[role] {
				t.Fatalf("round %d missing role %s in %+v", i, role, history)
			}
		}

		verdict, err := storeA.GetVerdict(ctx, traceID)
		if err != nil {
			t.Fatalf("round %d GetVerdict: %v", i, err)
		}
		if verdict == nil || verdict.Status != v2storage.StatusMatch {
			t.Fatalf("round %d verdict = %+v, want MATCH", i, verdict)
		}
	}
}

// openSecondPostgres opens another pool on an already-migrated database
// (second Beru process). Does not DROP schema.
func openSecondPostgres(t *testing.T, cfg PostgresConfig) *PostgresStore {
	t.Helper()
	store, err := OpenPostgres(slog.Default(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
