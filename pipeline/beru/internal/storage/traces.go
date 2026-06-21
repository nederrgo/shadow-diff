package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/shadow-diff/beru/internal/diff"
)

// Trace is one analyzed trace row.
type Trace struct {
	ID            int64
	ShadowTestID  int64
	TraceID       string
	Protocol      string
	Status        string
	Timestamp     string
	ShadowTestName string
}

// Mismatch is one regression field diff.
type Mismatch struct {
	ID            int64
	TraceID       string
	Path          string
	ExpectedValue string
	ActualValue   string
	BodyAJSON     sql.NullString
	BodyCJSON     sql.NullString
}

// SaveDiffResult persists trace metadata and mismatch payloads.
func (db *DB) SaveDiffResult(ctx context.Context, shadowTestName string, r diff.Result) error {
	const maxAttempts = 5
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err = db.saveDiffResultOnce(ctx, shadowTestName, r)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "locked") {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
	return err
}

func (db *DB) saveDiffResultOnce(ctx context.Context, shadowTestName string, r diff.Result) error {
	if r.Err != nil || r.TraceID == "" {
		return nil
	}
	if shadowTestName == "" {
		shadowTestName = db.DefaultShadowTestName()
	}
	if r.Status == "" {
		if len(r.Regressions) > 0 {
			r.Status = diff.StatusMismatch
		} else {
			r.Status = diff.StatusMatch
		}
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	runID, err := db.ensureShadowTestTx(ctx, tx, shadowTestName)
	if err != nil {
		return err
	}
	mismatch := r.Status == diff.StatusMismatch
	if err := db.incrementRunCounters(ctx, tx, runID, mismatch); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO traces (shadow_test_id, trace_id, protocol, status) VALUES (?, ?, ?, ?)`,
		runID, r.TraceID, r.Protocol, r.Status)
	if err != nil {
		return err
	}
	if err := db.insertEgressPayloads(ctx, tx, r); err != nil {
		return err
	}
	if !mismatch {
		return tx.Commit()
	}

	if err := db.insertMismatches(ctx, tx, r); err != nil {
		return err
	}
	_ = res
	return tx.Commit()
}

func (db *DB) ensureShadowTestTx(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM shadow_tests WHERE name = ? ORDER BY id DESC LIMIT 1`, name,
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO shadow_tests (name) VALUES (?)`, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) insertMismatches(ctx context.Context, tx *sql.Tx, r diff.Result) error {
	bodyA := string(r.BodyA)
	bodyC := string(r.BodyC)
	for i, reg := range r.Regressions {
		var aBody, cBody sql.NullString
		if i == 0 {
			aBody = sql.NullString{String: bodyA, Valid: len(r.BodyA) > 0}
			cBody = sql.NullString{String: bodyC, Valid: len(r.BodyC) > 0}
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO mismatches (trace_id, protocol, path, expected_value, actual_value, body_a_json, body_c_json)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.TraceID, r.Protocol, reg.Path, reg.Expected, reg.Actual, aBody, cBody)
		if err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) insertEgressPayloads(ctx context.Context, tx *sql.Tx, r diff.Result) error {
	workloads := []struct {
		name     string
		payloads [][]byte
	}{
		{"control-a", r.ControlA},
		{"control-b", r.ControlB},
		{"candidate", r.Candidate},
	}
	for _, w := range workloads {
		for i, payload := range w.payloads {
			if len(payload) == 0 {
				continue
			}
			_, err := tx.ExecContext(ctx, `
INSERT INTO egress_payloads (trace_id, protocol, workload, sequence_index, payload_json)
VALUES (?, ?, ?, ?, ?)`,
				r.TraceID, r.Protocol, w.name, i, string(payload))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// EgressPayload is one ordered egress operation stored for a trace.
type EgressPayload struct {
	Workload       string
	SequenceIndex  int
	PayloadJSON    string
}

// ListEgressPayloads returns ordered egress payloads for a trace and protocol.
func (db *DB) ListEgressPayloads(ctx context.Context, traceID, protocol string) ([]EgressPayload, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT workload, sequence_index, payload_json
FROM egress_payloads
WHERE trace_id = ? AND protocol = ?
ORDER BY sequence_index, workload`, traceID, protocol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EgressPayload
	for rows.Next() {
		var p EgressPayload
		if err := rows.Scan(&p.Workload, &p.SequenceIndex, &p.PayloadJSON); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListTraces returns traces for a shadow test run with optional status filter.
func (db *DB) ListTraces(ctx context.Context, shadowTestID int64, status string, limit int) ([]Trace, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `
SELECT t.id, t.shadow_test_id, t.trace_id, t.protocol, t.status, t.timestamp, s.name
FROM traces t
JOIN shadow_tests s ON s.id = t.shadow_test_id
WHERE t.shadow_test_id = ?`
	args := []any{shadowTestID}
	if status != "" && !strings.EqualFold(status, "all") {
		q += ` AND t.status = ?`
		args = append(args, strings.ToUpper(status))
	}
	q += ` ORDER BY t.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Trace
	for rows.Next() {
		var t Trace
		if err := rows.Scan(&t.ID, &t.ShadowTestID, &t.TraceID, &t.Protocol, &t.Status, &t.Timestamp, &t.ShadowTestName); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTracesByTraceID returns all protocol rows for one W3C trace id within a run.
func (db *DB) ListTracesByTraceID(ctx context.Context, shadowTestID int64, traceID string) ([]Trace, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT t.id, t.shadow_test_id, t.trace_id, t.protocol, t.status, t.timestamp, s.name
FROM traces t
JOIN shadow_tests s ON s.id = t.shadow_test_id
WHERE t.shadow_test_id = ? AND t.trace_id = ?
ORDER BY t.id`, shadowTestID, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Trace
	for rows.Next() {
		var t Trace
		if err := rows.Scan(&t.ID, &t.ShadowTestID, &t.TraceID, &t.Protocol, &t.Status, &t.Timestamp, &t.ShadowTestName); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTraceByID returns one trace row by database id.
func (db *DB) GetTraceByID(ctx context.Context, id int64) (Trace, error) {
	var t Trace
	err := db.sql.QueryRowContext(ctx, `
SELECT t.id, t.shadow_test_id, t.trace_id, t.protocol, t.status, t.timestamp, s.name
FROM traces t
JOIN shadow_tests s ON s.id = t.shadow_test_id
WHERE t.id = ?`, id,
	).Scan(&t.ID, &t.ShadowTestID, &t.TraceID, &t.Protocol, &t.Status, &t.Timestamp, &t.ShadowTestName)
	if err != nil {
		return Trace{}, fmt.Errorf("get trace: %w", err)
	}
	return t, nil
}

// ListMismatchesForTrace returns mismatch rows for a trace id string and protocol.
func (db *DB) ListMismatchesForTrace(ctx context.Context, traceID, protocol string) ([]Mismatch, error) {
	q := `
SELECT id, trace_id, path, expected_value, actual_value, body_a_json, body_c_json
FROM mismatches WHERE trace_id = ?`
	args := []any{traceID}
	if protocol != "" {
		q += ` AND protocol = ?`
		args = append(args, protocol)
	}
	q += ` ORDER BY id`
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Mismatch
	for rows.Next() {
		var m Mismatch
		if err := rows.Scan(&m.ID, &m.TraceID, &m.Path, &m.ExpectedValue, &m.ActualValue, &m.BodyAJSON, &m.BodyCJSON); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MismatchBodies returns control-a and candidate JSON for a trace and protocol.
func (db *DB) MismatchBodies(ctx context.Context, traceID, protocol string) ([]byte, []byte, error) {
	q := `
SELECT body_a_json, body_c_json FROM mismatches
WHERE trace_id = ? AND body_a_json IS NOT NULL`
	args := []any{traceID}
	if protocol != "" {
		q += ` AND protocol = ?`
		args = append(args, protocol)
	}
	q += ` LIMIT 1`
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil, sql.ErrNoRows
	}
	var a, c sql.NullString
	if err := rows.Scan(&a, &c); err != nil {
		return nil, nil, err
	}
	var bodyA, bodyC []byte
	if a.Valid {
		bodyA = []byte(a.String)
	}
	if c.Valid {
		bodyC = []byte(c.String)
	}
	return bodyA, bodyC, nil
}
