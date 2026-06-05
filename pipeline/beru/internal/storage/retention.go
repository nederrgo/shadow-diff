package storage

import (
	"context"
	"database/sql"
)

// Prune deletes traces and mismatches older than the retention window.
func (db *DB) Prune(ctx context.Context) error {
	cutoff := db.retentionDays
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type runDelta struct {
		id          int64
		total       int
		mismatches  int
	}
	var deltas []runDelta
	rows, err := tx.QueryContext(ctx, `
SELECT shadow_test_id,
       COUNT(*) AS total,
       SUM(CASE WHEN status = 'MISMATCH' THEN 1 ELSE 0 END) AS mismatches
FROM traces
WHERE timestamp < datetime('now', printf('-%d days', ?))
GROUP BY shadow_test_id`, cutoff)
	if err != nil {
		return err
	}
	for rows.Next() {
		var d runDelta
		if err := rows.Scan(&d.id, &d.total, &d.mismatches); err != nil {
			rows.Close()
			return err
		}
		deltas = append(deltas, d)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
DELETE FROM traces WHERE timestamp < datetime('now', printf('-%d days', ?))`, cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM mismatches WHERE trace_id NOT IN (SELECT trace_id FROM traces)`); err != nil {
		return err
	}
	for _, d := range deltas {
		if err := db.decrementRunCounters(ctx, tx, d.id, d.total, d.mismatches); err != nil {
			return err
		}
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

// execRetention is used by tests to verify orphan mismatch cleanup.
func (db *DB) countTraces(ctx context.Context) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM traces`).Scan(&n)
	return n, err
}

func (db *DB) countMismatches(ctx context.Context) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM mismatches`).Scan(&n)
	return n, err
}

// insertOldTraceForTest inserts a trace with an artificial old timestamp.
func (db *DB) insertOldTraceForTest(ctx context.Context, shadowTestID int64, traceID, daysAgo string) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO traces (shadow_test_id, trace_id, protocol, status, timestamp)
VALUES (?, ?, 'ingress', 'MATCH', datetime('now', ?))`,
		shadowTestID, traceID, daysAgo)
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
