package storage

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestRetention_pruneOldTraces(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	runID, err := db.ensureShadowTest(ctx, "retention-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.insertOldTraceForTest(ctx, runID, "old-trace", "-10 days"); err != nil {
		t.Fatal(err)
	}
	if err := db.insertOldTraceForTest(ctx, runID, "new-trace", "-1 days"); err != nil {
		t.Fatal(err)
	}
	db.retentionDays = 7
	if err := db.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := db.countTraces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("traces after prune = %d", n)
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

func TestOpenAt_createsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local.db")
	db, err := OpenAt(slog.Default(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
