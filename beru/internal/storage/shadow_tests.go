package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// ShadowTest is one shadow test run record.
type ShadowTest struct {
	ID             int64
	Name           string
	StartTime      string
	TotalTraces    int
	MismatchCount  int
	MatchRate      float64
}

func (db *DB) ensureShadowTest(ctx context.Context, name string) (int64, error) {
	if name == "" {
		name = defaultShadowTest
	}
	var id int64
	err := db.sql.QueryRowContext(ctx,
		`SELECT id FROM shadow_tests WHERE name = ? ORDER BY id DESC LIMIT 1`, name,
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := db.sql.ExecContext(ctx,
		`INSERT INTO shadow_tests (name) VALUES (?)`, name,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListShadowTests returns recent shadow test runs.
func (db *DB) ListShadowTests(ctx context.Context, limit int) ([]ShadowTest, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, name, start_time, total_traces, mismatch_count
FROM shadow_tests
ORDER BY id DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ShadowTest
	for rows.Next() {
		var st ShadowTest
		if err := rows.Scan(&st.ID, &st.Name, &st.StartTime, &st.TotalTraces, &st.MismatchCount); err != nil {
			return nil, err
		}
		if st.TotalTraces > 0 {
			st.MatchRate = float64(st.TotalTraces-st.MismatchCount) / float64(st.TotalTraces) * 100
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (db *DB) incrementRunCounters(ctx context.Context, tx *sql.Tx, shadowTestID int64, mismatch bool) error {
	if mismatch {
		_, err := tx.ExecContext(ctx, `
UPDATE shadow_tests
SET total_traces = total_traces + 1, mismatch_count = mismatch_count + 1
WHERE id = ?`, shadowTestID)
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE shadow_tests
SET total_traces = total_traces + 1
WHERE id = ?`, shadowTestID)
	return err
}

func (db *DB) decrementRunCounters(ctx context.Context, tx *sql.Tx, shadowTestID int64, total, mismatches int) error {
	if total == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
UPDATE shadow_tests
SET total_traces = CASE WHEN total_traces >= ? THEN total_traces - ? ELSE 0 END,
    mismatch_count = CASE WHEN mismatch_count >= ? THEN mismatch_count - ? ELSE 0 END
WHERE id = ?`, total, total, mismatches, mismatches, shadowTestID)
	return err
}

// GetShadowTest returns one run by id.
func (db *DB) GetShadowTest(ctx context.Context, id int64) (ShadowTest, error) {
	var st ShadowTest
	err := db.sql.QueryRowContext(ctx, `
SELECT id, name, start_time, total_traces, mismatch_count
FROM shadow_tests WHERE id = ?`, id,
	).Scan(&st.ID, &st.Name, &st.StartTime, &st.TotalTraces, &st.MismatchCount)
	if err != nil {
		return ShadowTest{}, fmt.Errorf("get shadow test: %w", err)
	}
	if st.TotalTraces > 0 {
		st.MatchRate = float64(st.TotalTraces-st.MismatchCount) / float64(st.TotalTraces) * 100
	}
	return st, nil
}
