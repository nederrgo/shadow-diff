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

// EnsureShadowTest creates the shadow test run row if missing.
func (db *DB) EnsureShadowTest(ctx context.Context, name string) error {
	_, err := db.ensureShadowTest(ctx, name)
	return err
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
