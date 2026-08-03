package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shadow-diff/beru/internal/roles"
	"github.com/shadow-diff/beru/internal/v2/diff"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

func evaluateHistory(history []v2storage.RawReport, noise map[string]struct{}, timeout time.Duration) *v2storage.VerdictState {
	return diff.EvaluateTraceHistory(history, noise, diff.EvalOptions{Timeout: timeout})
}

var _ v2storage.TraceRepository = (*PostgresStore)(nil)

// AppendReport stores one report and returns the trace's full timeline.
func (p *PostgresStore) AppendReport(ctx context.Context, report *v2storage.RawReport) ([]v2storage.RawReport, error) {
	if report == nil {
		return nil, fmt.Errorf("append report: nil report")
	}
	if err := p.insertReport(ctx, p.db, report); err != nil {
		return nil, err
	}
	return p.ListReports(ctx, report.TraceID, "")
}

type dbQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (p *PostgresStore) insertReport(ctx context.Context, q dbQuerier, report *v2storage.RawReport) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO raw_reports (
  trace_id, shadow_role, shadow_test_name, session_id, replay_execution_id,
  protocol, direction, signature, status_code, payload_bytes, captured_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		report.TraceID,
		report.ShadowRole,
		report.ShadowTestName,
		p.sessionID,
		p.replayExecutionID,
		report.Protocol,
		string(report.Direction),
		report.Signature,
		report.StatusCode,
		report.PayloadBytes,
		report.CapturedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("append report insert: %w", err)
	}
	return nil
}

// lockKey scopes the advisory lock to this beru-local's replay execution so
// concurrent executions of the same capture session do not serialize on each other.
func (p *PostgresStore) lockKey(traceID string) string {
	return p.replayExecutionID + ":" + traceID
}

// flushReportsAndEvaluate inserts WAL-batched reports under an advisory lock,
// re-diffs the full history, and upserts the verdict + UI projection.
func (p *PostgresStore) flushReportsAndEvaluate(
	ctx context.Context,
	reports []v2storage.RawReport,
	noise map[string]struct{},
	timeout time.Duration,
) error {
	if len(reports) == 0 {
		return nil
	}
	traceID := reports[0].TraceID
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, p.lockKey(traceID)); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	for i := range reports {
		if err := p.insertReport(ctx, tx, &reports[i]); err != nil {
			return err
		}
	}
	history, err := p.listReportsQ(ctx, tx, traceID, "")
	if err != nil {
		return err
	}
	verdict := evaluateHistory(history, noise, timeout)
	if verdict != nil {
		if err := p.upsertVerdict(ctx, tx, traceID, verdict, history); err != nil {
			return err
		}
		if err := p.projectTraceTx(ctx, tx, traceID, verdict, history); err != nil {
			p.log.Warn("Trace projection failed", "trace_id", traceID, "err", err)
		}
	}
	return tx.Commit()
}

// saveDiffVerdictUnderLock upserts a verdict while holding the per-trace advisory lock.
func (p *PostgresStore) saveDiffVerdictUnderLock(ctx context.Context, traceID string, verdict *v2storage.VerdictState) error {
	if verdict == nil {
		return fmt.Errorf("save diff verdict: nil verdict")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, p.lockKey(traceID)); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	history, err := p.listReportsQ(ctx, tx, traceID, "")
	if err != nil {
		return err
	}
	if err := p.upsertVerdict(ctx, tx, traceID, verdict, history); err != nil {
		return err
	}
	if err := p.projectTraceTx(ctx, tx, traceID, verdict, history); err != nil {
		p.log.Warn("Trace projection failed", "trace_id", traceID, "err", err)
	}
	return tx.Commit()
}

func (p *PostgresStore) upsertVerdict(
	ctx context.Context,
	q dbQuerier,
	traceID string,
	verdict *v2storage.VerdictState,
	history []v2storage.RawReport,
) error {
	shadowTestName := ""
	if len(history) > 0 {
		shadowTestName = history[0].ShadowTestName
	}
	_, err := q.ExecContext(ctx, `
INSERT INTO verdicts (
  replay_execution_id, trace_id, shadow_test_name, session_id,
  status, has_count_regression, summary_details, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (replay_execution_id, trace_id) DO UPDATE SET
  shadow_test_name     = excluded.shadow_test_name,
  session_id           = excluded.session_id,
  status               = excluded.status,
  has_count_regression = excluded.has_count_regression,
  summary_details      = excluded.summary_details,
  updated_at           = excluded.updated_at`,
		p.replayExecutionID,
		traceID,
		shadowTestName,
		p.sessionID,
		verdict.Status,
		verdict.HasCountRegression,
		jsonbValue(verdict.SummaryDetails),
		verdict.UpdatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("save diff verdict: %w", err)
	}
	return nil
}

// ListReports returns a trace's reports in capture order, optionally one protocol.
func (p *PostgresStore) ListReports(ctx context.Context, traceID, protocol string) ([]v2storage.RawReport, error) {
	return p.listReportsQ(ctx, p.db, traceID, protocol)
}

func (p *PostgresStore) listReportsQ(ctx context.Context, q dbQuerier, traceID, protocol string) ([]v2storage.RawReport, error) {
	if traceID == "" {
		return nil, fmt.Errorf("list reports: empty trace_id")
	}
	query := `
SELECT trace_id, shadow_role, shadow_test_name, protocol, direction, signature, status_code, payload_bytes, captured_at
FROM raw_reports
WHERE replay_execution_id = $1 AND trace_id = $2`
	args := []any{p.replayExecutionID, traceID}
	if protocol != "" {
		query += ` AND protocol = $3`
		args = append(args, protocol)
	}
	query += ` ORDER BY captured_at ASC, id ASC`

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query reports: %w", err)
	}
	defer rows.Close()

	var out []v2storage.RawReport
	for rows.Next() {
		var (
			rep       v2storage.RawReport
			direction string
		)
		if err := rows.Scan(
			&rep.TraceID, &rep.ShadowRole, &rep.ShadowTestName, &rep.Protocol,
			&direction, &rep.Signature, &rep.StatusCode, &rep.PayloadBytes, &rep.CapturedAt,
		); err != nil {
			return nil, err
		}
		rep.Direction = v2storage.PayloadDirection(direction)
		rep.CapturedAt = rep.CapturedAt.UTC()
		out = append(out, rep)
	}
	return out, rows.Err()
}

// ListTraceGroups returns recent (trace, protocol) pairs for a shadow test.
func (p *PostgresStore) ListTraceGroups(ctx context.Context, shadowTestName string, limit int) ([]v2storage.TraceGroup, error) {
	if limit <= 0 {
		limit = 200
	}
	if shadowTestName == "" {
		shadowTestName = defaultShadowTest
	}
	rows, err := p.db.QueryContext(ctx, `
SELECT trace_id, protocol, MAX(captured_at) AS last_at
FROM raw_reports
WHERE shadow_test_name = $1 AND replay_execution_id = $2
GROUP BY trace_id, protocol
ORDER BY last_at DESC
LIMIT $3`, shadowTestName, p.replayExecutionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list trace groups: %w", err)
	}
	defer rows.Close()

	var out []v2storage.TraceGroup
	for rows.Next() {
		var (
			g      v2storage.TraceGroup
			lastAt time.Time
		)
		if err := rows.Scan(&g.TraceID, &g.Protocol, &lastAt); err != nil {
			return nil, err
		}
		g.LastCapturedAt = lastAt.UTC().Format(time.RFC3339Nano)
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetVerdict returns the stored verdict for a trace, or nil when absent.
func (p *PostgresStore) GetVerdict(ctx context.Context, traceID string) (*v2storage.VerdictState, error) {
	var (
		status     string
		regression bool
		details    sql.NullString
		updated    time.Time
	)
	err := p.db.QueryRowContext(ctx, `
SELECT status, has_count_regression, summary_details, updated_at
FROM verdicts WHERE replay_execution_id = $1 AND trace_id = $2`,
		p.replayExecutionID, traceID,
	).Scan(&status, &regression, &details, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get verdict: %w", err)
	}
	v := &v2storage.VerdictState{
		Status:             status,
		HasCountRegression: regression,
		UpdatedAt:          updated.UTC(),
		SummaryDetails:     jsonbString(details),
	}
	if v.SummaryDetails != "" {
		var vd v2storage.VerdictDetails
		if json.Unmarshal([]byte(v.SummaryDetails), &vd) == nil {
			v.Flags = vd.Flags
		}
	}
	return v, nil
}

// ListStaleIncompleteTraces returns traces older than olderThan that are still
// missing at least one of the three required roles.
//
// Scoped to this beru-local's shadow test and skips traces that already have a
// verdict: every beru-local shares one Postgres, and without those guards the
// reaper would re-project foreign WAITING_FOR_ROLES rows onto the current
// SESSION_ID (UI "session hopping").
func (p *PostgresStore) ListStaleIncompleteTraces(ctx context.Context, olderThan time.Time) ([]v2storage.StaleIncompleteTrace, error) {
	// Postgres does not allow SELECT aliases in HAVING, so the role counts are
	// spelled out again rather than referenced as n_a / n_b / n_c.
	rows, err := p.db.QueryContext(ctx, `
SELECT r.trace_id, MIN(r.captured_at) AS first_at
FROM raw_reports r
WHERE r.shadow_test_name = $5
  AND r.replay_execution_id = $6
  AND NOT EXISTS (
    SELECT 1 FROM verdicts v
    WHERE v.replay_execution_id = r.replay_execution_id AND v.trace_id = r.trace_id)
GROUP BY r.trace_id
HAVING MIN(r.captured_at) <= $1
   AND (SUM(CASE WHEN r.shadow_role = $2 THEN 1 ELSE 0 END) = 0
     OR SUM(CASE WHEN r.shadow_role = $3 THEN 1 ELSE 0 END) = 0
     OR SUM(CASE WHEN r.shadow_role = $4 THEN 1 ELSE 0 END) = 0)`,
		olderThan.UTC(), roles.ControlA, roles.ControlB, roles.Candidate,
		p.DefaultShadowTestName(), p.replayExecutionID,
	)
	if err != nil {
		return nil, fmt.Errorf("list stale incomplete traces: %w", err)
	}
	defer rows.Close()

	var out []v2storage.StaleIncompleteTrace
	for rows.Next() {
		var s v2storage.StaleIncompleteTrace
		if err := rows.Scan(&s.TraceID, &s.FirstSeen); err != nil {
			return nil, err
		}
		s.FirstSeen = s.FirstSeen.UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// SaveDiffVerdict upserts the trace's verdict, then refreshes the UI projection.
func (p *PostgresStore) SaveDiffVerdict(ctx context.Context, traceID string, verdict *v2storage.VerdictState) error {
	if verdict == nil {
		return fmt.Errorf("save diff verdict: nil verdict")
	}
	history, err := p.ListReports(ctx, traceID, "")
	if err != nil {
		return fmt.Errorf("save diff verdict: %w", err)
	}
	if err := p.upsertVerdict(ctx, p.db, traceID, verdict, history); err != nil {
		return err
	}
	if err := p.projectTrace(ctx, traceID, verdict, history); err != nil {
		p.log.Warn("Trace projection failed", "trace_id", traceID, "err", err)
	}
	return nil
}

// projectTrace rebuilds the traces / diff_reports rows Tusk reads.
func (p *PostgresStore) projectTrace(
	ctx context.Context,
	traceID string,
	verdict *v2storage.VerdictState,
	history []v2storage.RawReport,
) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := p.projectTraceTx(ctx, tx, traceID, verdict, history); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *PostgresStore) projectTraceTx(
	ctx context.Context,
	tx *sql.Tx,
	traceID string,
	verdict *v2storage.VerdictState,
	history []v2storage.RawReport,
) error {
	if len(history) == 0 {
		return nil
	}

	var details v2storage.VerdictDetails
	if verdict.SummaryDetails != "" {
		_ = json.Unmarshal([]byte(verdict.SummaryDetails), &details)
	}
	noiseDiff := jsonOrNil(details.Baseline)

	sessionID := sql.NullString{String: p.sessionID, Valid: p.sessionID != ""}
	method, path := httpMethodPath(history)

	if _, err := tx.ExecContext(ctx, `
INSERT INTO traces (replay_execution_id, trace_id, session_id, shadow_test_name, path, method,
                    status_code_a, status_code_b, status_code_candidate, verdict)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (replay_execution_id, trace_id) DO UPDATE SET
  session_id            = excluded.session_id,
  shadow_test_name      = excluded.shadow_test_name,
  path                  = excluded.path,
  method                = excluded.method,
  status_code_a         = excluded.status_code_a,
  status_code_b         = excluded.status_code_b,
  status_code_candidate = excluded.status_code_candidate,
  verdict               = excluded.verdict`,
		p.replayExecutionID, traceID, sessionID, history[0].ShadowTestName, path, method,
		ingressStatus(history, roles.ControlA),
		ingressStatus(history, roles.ControlB),
		ingressStatus(history, roles.Candidate),
		verdict.Status,
	); err != nil {
		return fmt.Errorf("project traces row: %w", err)
	}

	for _, sig := range signatureOrder(history) {
		bucket := reportsForSignature(history, sig)
		regressionDiff := jsonOrNil(stepsForSignature(details.Steps, sig))
		if _, err := tx.ExecContext(ctx, `
INSERT INTO diff_reports (replay_execution_id, trace_id, session_id, signature, source_type,
                          control_a_payload, control_b_payload, candidate_payload,
                          noise_diff, regression_diff, verdict)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (replay_execution_id, trace_id, signature) DO UPDATE SET
  session_id        = excluded.session_id,
  source_type       = excluded.source_type,
  control_a_payload = excluded.control_a_payload,
  control_b_payload = excluded.control_b_payload,
  candidate_payload = excluded.candidate_payload,
  noise_diff        = excluded.noise_diff,
  regression_diff   = excluded.regression_diff,
  verdict           = excluded.verdict`,
			p.replayExecutionID, traceID, sessionID, sig, sourceType(bucket),
			payloadJSON(bucket, roles.ControlA),
			payloadJSON(bucket, roles.ControlB),
			payloadJSON(bucket, roles.Candidate),
			noiseDiff, regressionDiff, verdict.Status,
		); err != nil {
			return fmt.Errorf("project diff_reports row %s: %w", sig, err)
		}
	}

	// Fan out to Tusk LISTEN/NOTIFY so The System ShadowDiff page can hydrate
	// via GET then stream live verdict deltas without polling.
	notifyPayload, err := json.Marshal(map[string]string{
		"session_id":           p.sessionID,
		"replay_execution_id":  p.replayExecutionID,
		"trace_id":             traceID,
		"verdict":              verdict.Status,
	})
	if err != nil {
		return fmt.Errorf("project notify payload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_notify('verdict_events', $1)`, string(notifyPayload)); err != nil {
		return fmt.Errorf("project pg_notify: %w", err)
	}
	return nil
}

// signatureOrder lists each distinct signature once, in first-seen order.
func signatureOrder(history []v2storage.RawReport) []string {
	seen := make(map[string]struct{}, len(history))
	var out []string
	for _, r := range history {
		if _, ok := seen[r.Signature]; ok {
			continue
		}
		seen[r.Signature] = struct{}{}
		out = append(out, r.Signature)
	}
	return out
}

func reportsForSignature(history []v2storage.RawReport, signature string) []v2storage.RawReport {
	var out []v2storage.RawReport
	for _, r := range history {
		if r.Signature == signature {
			out = append(out, r)
		}
	}
	return out
}

func sourceType(bucket []v2storage.RawReport) string {
	if len(bucket) == 0 {
		return ""
	}
	return bucket[0].Protocol + "/" + string(bucket[0].Direction)
}

// payloadJSON renders a role's payload for a JSONB column.
//
// ponytail: a role that performs the same operation twice in one trace lands in
// the same signature bucket and only its first payload is projected. raw_reports
// keeps every occurrence; The System loads repeats lazily via Tusk
// GET /api/v1/diffs/occurrences (not by widening the diff_reports unique key).
func payloadJSON(bucket []v2storage.RawReport, role string) any {
	for _, r := range bucket {
		if r.ShadowRole != role {
			continue
		}
		if len(r.PayloadBytes) == 0 {
			return nil
		}
		if json.Valid(r.PayloadBytes) {
			return string(r.PayloadBytes)
		}
		// Payloads are not guaranteed JSON (raw HTTP bodies); keep them
		// readable rather than dropping the row.
		b, err := json.Marshal(map[string]string{"_raw": string(r.PayloadBytes)})
		if err != nil {
			return nil
		}
		return string(b)
	}
	return nil
}

func stepsForSignature(steps []v2storage.VerdictStep, signature string) []v2storage.VerdictStep {
	var out []v2storage.VerdictStep
	for _, s := range steps {
		if s.Signature == signature {
			out = append(out, s)
		}
	}
	return out
}

// httpMethodPath recovers the request line from the HTTP ingress signature,
// which report.HTTPSignature builds as "http:{METHOD}:{path}".
func httpMethodPath(history []v2storage.RawReport) (method, path string) {
	for _, r := range history {
		if r.Protocol != "http" || r.Direction != v2storage.DirectionIngress {
			continue
		}
		parts := strings.SplitN(r.Signature, ":", 3)
		if len(parts) == 3 {
			return parts[1], parts[2]
		}
	}
	return "", ""
}

func ingressStatus(history []v2storage.RawReport, role string) string {
	for _, r := range history {
		if r.ShadowRole == role && r.Direction == v2storage.DirectionIngress && r.StatusCode != "" {
			return r.StatusCode
		}
	}
	return ""
}

// jsonOrNil marshals v for a JSONB column, yielding NULL for nil/empty input.
func jsonOrNil(v any) any {
	switch typed := v.(type) {
	case nil:
		return nil
	case []v2storage.VerdictStep:
		if len(typed) == 0 {
			return nil
		}
	case *v2storage.BaselineFailure:
		if typed == nil {
			return nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(b)
}
