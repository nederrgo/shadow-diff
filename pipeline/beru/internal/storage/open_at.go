package storage

import (
	"context"
	"log/slog"
)

// OpenAt opens a database at an explicit path (for tests).
func OpenAt(log *slog.Logger, path string) (*DB, error) {
	return openAt(log, path, false)
}

func openAt(log *slog.Logger, path string, startRetention bool) (*DB, error) {
	if log == nil {
		log = slog.Default()
	}
	sqlDB, err := openSQL(path)
	if err != nil {
		return nil, err
	}
	db := &DB{
		sql:             sqlDB,
		log:             log,
		retentionDays:   retentionDaysFromEnv(),
		defaultTestName: shadowTestNameFromEnv(),
	}
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	if _, err := db.ensureShadowTest(context.Background(), db.defaultTestName); err != nil {
		sqlDB.Close()
		return nil, err
	}
	if startRetention {
		go db.retentionLoop()
	}
	log.Info("SQLite storage ready", "path", path, "retention_days", db.retentionDays)
	return db, nil
}
