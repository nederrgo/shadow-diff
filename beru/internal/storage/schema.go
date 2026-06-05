package storage

const schemaDDL = `
CREATE TABLE IF NOT EXISTS shadow_tests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  start_time TEXT NOT NULL DEFAULT (datetime('now')),
  total_traces INTEGER NOT NULL DEFAULT 0,
  mismatch_count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS traces (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  shadow_test_id INTEGER NOT NULL REFERENCES shadow_tests(id),
  trace_id TEXT NOT NULL,
  protocol TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('MATCH','MISMATCH')),
  timestamp TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_traces_shadow_test ON traces(shadow_test_id);
CREATE INDEX IF NOT EXISTS idx_traces_status ON traces(status);
CREATE INDEX IF NOT EXISTS idx_traces_timestamp ON traces(timestamp);

CREATE TABLE IF NOT EXISTS mismatches (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  trace_id TEXT NOT NULL,
  path TEXT NOT NULL,
  expected_value TEXT,
  actual_value TEXT,
  body_a_json TEXT,
  body_c_json TEXT
);
CREATE INDEX IF NOT EXISTS idx_mismatches_trace ON mismatches(trace_id);

CREATE TABLE IF NOT EXISTS noise_filters (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  shadow_test_name TEXT NOT NULL,
  path TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(shadow_test_name, path)
);
`

func (db *DB) migrate() error {
	_, err := db.sql.Exec(schemaDDL)
	return err
}
