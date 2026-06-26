package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var _ TraceRepository = (*SQLiteRepository)(nil)

const schemaDDL = `
CREATE TABLE IF NOT EXISTS raw_reports (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  trace_id TEXT NOT NULL,
  shadow_role TEXT NOT NULL,
  shadow_test_name TEXT NOT NULL DEFAULT '',
  protocol TEXT NOT NULL,
  direction TEXT NOT NULL,
  signature TEXT NOT NULL,
  payload_bytes BLOB,
  captured_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_raw_reports_trace_id ON raw_reports(trace_id);
CREATE INDEX IF NOT EXISTS idx_raw_reports_shadow_test ON raw_reports(shadow_test_name, captured_at);

CREATE TABLE IF NOT EXISTS verdicts (
  trace_id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  has_count_regression INTEGER NOT NULL,
  summary_details TEXT,
  updated_at TEXT NOT NULL
);
`

type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(db *sql.DB) (*SQLiteRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("sqlite repository: nil db")
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;"); err != nil {
		return nil, fmt.Errorf("sqlite repository pragmas: %w", err)
	}
	repo := &SQLiteRepository{db: db}
	if err := repo.migrate(); err != nil {
		return nil, err
	}
	return repo, nil
}

func (r *SQLiteRepository) migrate() error {
	if _, err := r.db.Exec(schemaDDL); err != nil {
		return fmt.Errorf("sqlite repository migrate: %w", err)
	}
	_, err := r.db.Exec(`ALTER TABLE raw_reports ADD COLUMN shadow_test_name TEXT NOT NULL DEFAULT ''`)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}
	_, err = r.db.Exec(`CREATE INDEX IF NOT EXISTS idx_raw_reports_shadow_test ON raw_reports(shadow_test_name, captured_at)`)
	return err
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

func (r *SQLiteRepository) AppendReport(ctx context.Context, report *RawReport) ([]RawReport, error) {
	if report == nil {
		return nil, fmt.Errorf("append report: nil report")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO raw_reports (trace_id, shadow_role, shadow_test_name, protocol, direction, signature, payload_bytes, captured_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		report.TraceID,
		report.ShadowRole,
		report.ShadowTestName,
		report.Protocol,
		string(report.Direction),
		report.Signature,
		report.PayloadBytes,
		formatTime(report.CapturedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("append report insert: %w", err)
	}
	return r.listReports(ctx, report.TraceID)
}

func (r *SQLiteRepository) listReports(ctx context.Context, traceID string) ([]RawReport, error) {
	return r.ListReports(ctx, traceID, "")
}

func (r *SQLiteRepository) SaveDiffVerdict(ctx context.Context, traceID string, verdict *VerdictState) error {
	if verdict == nil {
		return fmt.Errorf("save diff verdict: nil verdict")
	}
	regression := 0
	if verdict.HasCountRegression {
		regression = 1
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO verdicts (trace_id, status, has_count_regression, summary_details, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(trace_id) DO UPDATE SET
  status = excluded.status,
  has_count_regression = excluded.has_count_regression,
  summary_details = excluded.summary_details,
  updated_at = excluded.updated_at`,
		traceID,
		verdict.Status,
		regression,
		verdict.SummaryDetails,
		formatTime(verdict.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("save diff verdict: %w", err)
	}
	return nil
}
