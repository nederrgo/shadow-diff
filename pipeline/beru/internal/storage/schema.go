package storage

import "strings"

const schemaDDL = `
CREATE TABLE IF NOT EXISTS shadow_tests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  start_time TEXT NOT NULL DEFAULT (datetime('now')),
  total_traces INTEGER NOT NULL DEFAULT 0,
  mismatch_count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS noise_filters (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  shadow_test_name TEXT NOT NULL,
  path TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(shadow_test_name, path)
);
`

func (db *DB) migrate() error {
	if _, err := db.sql.Exec(schemaDDL); err != nil {
		return err
	}
	// ponytail: drop legacy dashboard tables after v2 migration
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS mismatches`,
		`DROP TABLE IF EXISTS egress_payloads`,
		`DROP TABLE IF EXISTS traces`,
	} {
		if _, err := db.sql.Exec(stmt); err != nil {
			return err
		}
	}
	_, err := db.sql.Exec(`ALTER TABLE raw_reports ADD COLUMN shadow_test_name TEXT NOT NULL DEFAULT ''`)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		// raw_reports owned by v2 migrate; ignore if table missing on fresh legacy-only path
		if !strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return err
		}
	}
	return nil
}
