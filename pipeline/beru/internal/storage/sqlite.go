package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultDBPath      = "/var/lib/beru/shadow_diff.db"
	fallbackDBPath     = "./shadow_diff.db"
	defaultRetention   = 7
	defaultShadowTest  = "default"
	retentionInterval  = time.Hour
)

// DB wraps a SQLite database for shadow diff persistence.
type DB struct {
	sql            *sql.DB
	log            *slog.Logger
	retentionDays  int
	defaultTestName string
}

// Open connects to SQLite, runs migrations, and starts the retention worker.
func Open(log *slog.Logger) (*DB, error) {
	if log == nil {
		log = slog.Default()
	}
	path, err := resolveDBPath(log)
	if err != nil {
		return nil, err
	}
	return openAt(log, path, true)
}

func openSQL(path string) (*sql.DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
	}
	for _, p := range pragmas {
		if _, err := sqlDB.Exec(p); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("pragma %q: %w", p, err)
		}
	}
	return sqlDB, nil
}

// Close shuts down the database connection.
func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

// DefaultShadowTestName returns the configured default shadow test name.
func (db *DB) DefaultShadowTestName() string {
	if db.defaultTestName == "" {
		return defaultShadowTest
	}
	return db.defaultTestName
}

func resolveDBPath(log *slog.Logger) (string, error) {
	if p := os.Getenv("BERU_DB_PATH"); p != "" {
		return ensureParentDir(p)
	}
	path, err := ensureParentDir(defaultDBPath)
	if err == nil {
		return path, nil
	}
	log.Info("Could not use default database path, falling back to local file",
		"path", defaultDBPath, "err", err)
	return fallbackDBPath, nil
}

func ensureParentDir(path string) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

func retentionDaysFromEnv() int {
	if v := os.Getenv("BERU_DB_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultRetention
}

func shadowTestNameFromEnv() string {
	if v := os.Getenv("BERU_SHADOW_TEST_NAME"); v != "" {
		return v
	}
	return defaultShadowTest
}

func (db *DB) retentionLoop() {
	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()
	for range ticker.C {
		if err := db.Prune(context.Background()); err != nil {
			db.log.Error("Retention prune failed", "err", err)
		}
	}
}
