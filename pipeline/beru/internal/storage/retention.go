package storage

import (
	"context"
	"database/sql"
)

// Prune deletes raw reports and orphan verdicts older than the retention window.
func (db *DB) Prune(ctx context.Context) error {
	cutoff := db.retentionDays
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
DELETE FROM raw_reports
WHERE captured_at < datetime('now', printf('-%d days', ?))`, cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM verdicts
WHERE trace_id NOT IN (SELECT DISTINCT trace_id FROM raw_reports)`); err != nil {
		return err
	}
	return tx.Commit()
}

// pruneForTest runs retention with explicit days (testing helper).
func (db *DB) pruneForTest(ctx context.Context, days int) error {
	prev := db.retentionDays
	db.retentionDays = days
	defer func() { db.retentionDays = prev }()
	return db.Prune(ctx)
}

func (db *DB) countRawReports(ctx context.Context) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM raw_reports`).Scan(&n)
	return n, err
}

func (db *DB) countVerdicts(ctx context.Context) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM verdicts`).Scan(&n)
	return n, err
}

// insertOldReportForTest inserts a report with an artificial old timestamp.
func (db *DB) insertOldReportForTest(ctx context.Context, traceID, daysAgo string) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO raw_reports (trace_id, shadow_role, shadow_test_name, protocol, direction, signature, payload_bytes, captured_at)
VALUES (?, 'control-a', 'default', 'http', 'ingress', 'http:GET:/', '{}', datetime('now', ?))`,
		traceID, daysAgo)
	return err
}

// withTx helper for tests.
func (db *DB) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
