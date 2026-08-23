package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shadow-diff/beru/internal/roles"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

// Projection failure must abort the verdict write so SoT and UI stay aligned.
func TestPostgres_projectionFailureRollsBackVerdict(t *testing.T) {
	dsn := os.Getenv("BERU_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BERU_TEST_POSTGRES_DSN unset; set it to a throwaway database to run Postgres projection hard-fail test")
	}

	t.Run("SaveDiffVerdict", func(t *testing.T) {
		store := newPostgresBackend(t)
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Millisecond)
		trace := "proj-fail-save"
		for _, r := range []struct{ role, payload string }{
			{roles.ControlA, `{"n":1}`},
			{roles.ControlB, `{"n":1}`},
			{roles.Candidate, `{"n":1}`},
		} {
			if _, err := store.AppendReport(ctx, report(trace, r.role, "mongodb",
				v2storage.DirectionEgress, "mongodb:insert:orders", r.payload, "", now)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.db.ExecContext(ctx, `DROP TABLE traces`); err != nil {
			t.Fatal(err)
		}

		err := store.SaveDiffVerdict(ctx, trace, &v2storage.VerdictState{
			Status:     v2storage.StatusMatch,
			UpdatedAt:  now,
		})
		if err == nil {
			t.Fatal("SaveDiffVerdict: want error when traces table missing")
		}

		verdict, err := store.GetVerdict(ctx, trace)
		if err != nil {
			t.Fatal(err)
		}
		if verdict != nil {
			t.Fatalf("verdict row persisted after projection failure: %+v", verdict)
		}
	})

	t.Run("flushReportsAndEvaluate", func(t *testing.T) {
		store := newPostgresBackend(t)
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Millisecond)
		trace := "proj-fail-flush"
		batch := []v2storage.RawReport{
			*reportWithIngestID(trace, roles.ControlA, "mongodb", v2storage.DirectionEgress,
				"mongodb:insert:orders", `{"n":1}`, "", now, 3001),
			*reportWithIngestID(trace, roles.ControlB, "mongodb", v2storage.DirectionEgress,
				"mongodb:insert:orders", `{"n":1}`, "", now.Add(time.Millisecond), 3002),
			*reportWithIngestID(trace, roles.Candidate, "mongodb", v2storage.DirectionEgress,
				"mongodb:insert:orders", `{"n":1}`, "", now.Add(2*time.Millisecond), 3003),
		}
		if _, err := store.db.ExecContext(ctx, `DROP TABLE traces`); err != nil {
			t.Fatal(err)
		}

		err := store.flushReportsAndEvaluate(ctx, batch, nil, 10*time.Second)
		if err == nil {
			t.Fatal("flushReportsAndEvaluate: want error when traces table missing")
		}

		count, err := countRawReports(ctx, store, trace)
		if err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("raw_reports count = %d after failed flush, want 0 (rolled back)", count)
		}
		verdict, err := store.GetVerdict(ctx, trace)
		if err != nil {
			t.Fatal(err)
		}
		if verdict != nil {
			t.Fatalf("verdict row persisted after failed flush: %+v", verdict)
		}
	})
}
