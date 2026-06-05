package storage

import (
	"context"
	"database/sql"
)

// NoisePathsForTest returns configured ignore paths for a shadow test name.
func (db *DB) NoisePathsForTest(ctx context.Context, shadowTestName string) (map[string]struct{}, error) {
	if shadowTestName == "" {
		shadowTestName = db.DefaultShadowTestName()
	}
	rows, err := db.sql.QueryContext(ctx, `
SELECT path FROM noise_filters WHERE shadow_test_name = ?`, shadowTestName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		out[path] = struct{}{}
	}
	return out, rows.Err()
}

// AddNoiseFilter inserts an ignore path for a shadow test.
func (db *DB) AddNoiseFilter(ctx context.Context, shadowTestName, path string) error {
	if shadowTestName == "" {
		shadowTestName = db.DefaultShadowTestName()
	}
	_, err := db.sql.ExecContext(ctx, `
INSERT OR IGNORE INTO noise_filters (shadow_test_name, path) VALUES (?, ?)`,
		shadowTestName, path)
	return err
}

// ListNoiseFilters returns filters for a shadow test name.
func (db *DB) ListNoiseFilters(ctx context.Context, shadowTestName string) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT path FROM noise_filters WHERE shadow_test_name = ? ORDER BY created_at`, shadowTestName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// noiseFilterExists checks whether a path is already ignored.
func (db *DB) noiseFilterExists(ctx context.Context, shadowTestName, path string) (bool, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `
SELECT COUNT(1) FROM noise_filters WHERE shadow_test_name = ? AND path = ?`,
		shadowTestName, path).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return n > 0, err
}
