package storage

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

func TestRetention_pruneOldReports(t *testing.T) {
	db := testDB(t)
	if _, err := v2storage.NewSQLiteRepository(db.SQL()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.insertOldReportForTest(ctx, "old-trace", "-10 days"); err != nil {
		t.Fatal(err)
	}
	if err := db.insertOldReportForTest(ctx, "new-trace", "-1 days"); err != nil {
		t.Fatal(err)
	}
	db.retentionDays = 7
	if err := db.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := db.countRawReports(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("raw_reports after prune = %d", n)
	}
}

func TestOpenAt_envRetention(t *testing.T) {
	t.Setenv("BERU_DB_RETENTION_DAYS", "14")
	dir := t.TempDir()
	db, err := OpenAt(slog.Default(), filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.retentionDays != 14 {
		t.Fatalf("retention = %d", db.retentionDays)
	}
}

func TestDefaultShadowTestName_env(t *testing.T) {
	t.Setenv("BERU_SHADOW_TEST_NAME", "my-shadow")
	dir := t.TempDir()
	db, err := OpenAt(slog.Default(), filepath.Join(dir, "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.DefaultShadowTestName() != "my-shadow" {
		t.Fatalf("name = %q", db.DefaultShadowTestName())
	}
}

func TestShadowTestNameFromMetadata(t *testing.T) {
	got := ShadowTestNameFromMetadata(map[string]string{"shadow_test_name": "cr"}, "fallback")
	if got != "cr" {
		t.Fatalf("got %q", got)
	}
	got = ShadowTestNameFromMetadata(nil, "fallback")
	if got != "fallback" {
		t.Fatalf("got %q", got)
	}
}
