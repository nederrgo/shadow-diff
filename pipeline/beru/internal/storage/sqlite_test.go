package storage

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := OpenAt(slog.Default(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpen_migrateAndSeed(t *testing.T) {
	db := testDB(t)
	runs, err := db.ListShadowTests(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("expected default shadow test run")
	}
}
