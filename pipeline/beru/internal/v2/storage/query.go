package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// TraceGroup is one trace plus protocol seen in raw_reports.
type TraceGroup struct {
	TraceID        string
	Protocol       string
	LastCapturedAt string
}

func (r *SQLiteRepository) ListReports(ctx context.Context, traceID, protocol string) ([]RawReport, error) {
	if traceID == "" {
		return nil, fmt.Errorf("list reports: empty trace_id")
	}
	q := `
SELECT trace_id, shadow_role, shadow_test_name, protocol, direction, signature, payload_bytes, captured_at
FROM raw_reports
WHERE trace_id = ?`
	args := []any{traceID}
	if protocol != "" {
		q += ` AND protocol = ?`
		args = append(args, protocol)
	}
	q += ` ORDER BY captured_at ASC, id ASC`
	return r.queryReports(ctx, q, args...)
}

func (r *SQLiteRepository) ListTraceGroups(ctx context.Context, shadowTestName string, limit int) ([]TraceGroup, error) {
	if limit <= 0 {
		limit = 200
	}
	if shadowTestName == "" {
		shadowTestName = "default"
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT trace_id, protocol, MAX(captured_at) AS last_at
FROM raw_reports
WHERE shadow_test_name = ?
GROUP BY trace_id, protocol
ORDER BY last_at DESC
LIMIT ?`, shadowTestName, limit)
	if err != nil {
		return nil, fmt.Errorf("list trace groups: %w", err)
	}
	defer rows.Close()
	var out []TraceGroup
	for rows.Next() {
		var g TraceGroup
		if err := rows.Scan(&g.TraceID, &g.Protocol, &g.LastCapturedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) GetVerdict(ctx context.Context, traceID string) (*VerdictState, error) {
	var (
		status     string
		regression int
		details    sql.NullString
		updated    string
	)
	err := r.db.QueryRowContext(ctx, `
SELECT status, has_count_regression, summary_details, updated_at
FROM verdicts WHERE trace_id = ?`, traceID,
	).Scan(&status, &regression, &details, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get verdict: %w", err)
	}
	t, err := parseTime(updated)
	if err != nil {
		return nil, err
	}
	v := &VerdictState{
		Status:             status,
		HasCountRegression: regression != 0,
		UpdatedAt:          t,
	}
	if details.Valid {
		v.SummaryDetails = details.String
	}
	return v, nil
}

func (r *SQLiteRepository) queryReports(ctx context.Context, q string, args ...any) ([]RawReport, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query reports: %w", err)
	}
	defer rows.Close()
	var out []RawReport
	for rows.Next() {
		rep, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rep)
	}
	return out, rows.Err()
}

func scanReport(rows *sql.Rows) (RawReport, error) {
	var (
		rep       RawReport
		direction string
		captured  string
	)
	if err := rows.Scan(
		&rep.TraceID,
		&rep.ShadowRole,
		&rep.ShadowTestName,
		&rep.Protocol,
		&direction,
		&rep.Signature,
		&rep.PayloadBytes,
		&captured,
	); err != nil {
		return RawReport{}, err
	}
	rep.Direction = PayloadDirection(direction)
	var err error
	rep.CapturedAt, err = parseTime(captured)
	return rep, err
}
