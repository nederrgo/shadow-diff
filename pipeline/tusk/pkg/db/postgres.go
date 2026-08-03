// Package db reads Beru's Postgres projection tables for the ShadowDiff UI.
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

const (
	defaultPort    = "5432"
	defaultSSLMode = "require"
)

// Config is the shared Beru/Tusk Postgres connection, read from DB_* env vars.
type Config struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	SSLMode  string
}

// ConfigFromEnv reads DB_*. Missing required fields returns ok=false so Tusk
// can keep serving topology in control-plane-only mode.
func ConfigFromEnv() (Config, bool) {
	cfg := Config{
		Host:     os.Getenv("DB_HOST"),
		Port:     os.Getenv("DB_PORT"),
		User:     os.Getenv("DB_USER"),
		Password: os.Getenv("DB_PASSWORD"),
		Name:     os.Getenv("DB_NAME"),
		SSLMode:  os.Getenv("DB_SSLMODE"),
	}
	if cfg.Port == "" {
		cfg.Port = defaultPort
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = defaultSSLMode
	}
	if cfg.Host == "" || cfg.User == "" || cfg.Name == "" {
		return Config{}, false
	}
	return cfg, true
}

// DSN renders the connection string.
func (c Config) DSN() string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   net.JoinHostPort(c.Host, c.Port),
		Path:   "/" + c.Name,
		RawQuery: url.Values{
			"sslmode":         {c.SSLMode},
			"connect_timeout": {"5"},
		}.Encode(),
	}
	return u.String()
}

// Store is a thin query layer over Beru's UI projection tables.
type Store struct {
	db *sql.DB
}

// Open connects with the pgx stdlib driver.
func Open(cfg Config) (*Store, error) {
	db, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the pool.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Session is one row from shadow_sessions.
type Session struct {
	SessionID      string    `json:"session_id"`
	ShadowTestName string    `json:"shadow_test_name"`
	Namespace      string    `json:"namespace"`
	Mode           string    `json:"mode"`
	CreatedAt      time.Time `json:"created_at"`
}

// SessionDiff is a diff_reports row joined with traces metadata.
type SessionDiff struct {
	TraceID             string          `json:"trace_id"`
	SessionID           string          `json:"session_id"`
	ReplayExecutionID   string          `json:"replay_execution_id"`
	Signature           string          `json:"signature"`
	SourceType          string          `json:"source_type"`
	Method              string          `json:"method"`
	Path                string          `json:"path"`
	StatusCodeA         string          `json:"status_code_a"`
	StatusCodeB         string          `json:"status_code_b"`
	StatusCodeCandidate string          `json:"status_code_candidate"`
	ControlAPayload     json.RawMessage `json:"control_a_payload"`
	ControlBPayload     json.RawMessage `json:"control_b_payload"`
	CandidatePayload    json.RawMessage `json:"candidate_payload"`
	NoiseDiff           json.RawMessage `json:"noise_diff"`
	RegressionDiff      json.RawMessage `json:"regression_diff"`
	Verdict             string          `json:"verdict"`
	CreatedAt           time.Time       `json:"created_at"`
}

// ReplayExecution is one replay run of an S3 session.
type ReplayExecution struct {
	ReplayExecutionID string    `json:"replay_execution_id"`
	SessionID         string    `json:"session_id"`
	CreatedAt         time.Time `json:"created_at"`
}

// SessionSummary is aggregate verdict counts for one replay execution's traces.
type SessionSummary struct {
	SessionID         string `json:"session_id"`
	ReplayExecutionID string `json:"replay_execution_id"`
	Total             int    `json:"total"`
	Match             int    `json:"match"`
	Mismatch          int    `json:"mismatch"`
	Voided            int    `json:"voided"`
}

// ListSessionsOpts controls which shadow_sessions rows are returned.
type ListSessionsOpts struct {
	// WithDiffsOnly keeps sessions that have at least one traces row
	// (projected verdict data). Empty boot-only sessions are dropped.
	WithDiffsOnly bool
}

// ListSessions returns shadow_sessions rows, newest first.
func (s *Store) ListSessions(ctx context.Context, opts ListSessionsOpts) ([]Session, error) {
	q := `
SELECT s.session_id, s.shadow_test_name, s.namespace, s.mode, s.created_at
FROM shadow_sessions s`
	if opts.WithDiffsOnly {
		q += `
WHERE EXISTS (SELECT 1 FROM traces t WHERE t.session_id = s.session_id)`
	}
	q += `
ORDER BY s.created_at DESC`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	out := []Session{}
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.SessionID, &sess.ShadowTestName, &sess.Namespace, &sess.Mode, &sess.CreatedAt); err != nil {
			return nil, fmt.Errorf("list sessions scan: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// ListExecutions returns replay runs for a session, newest first.
func (s *Store) ListExecutions(ctx context.Context, sessionID string) ([]ReplayExecution, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT replay_execution_id, session_id, created_at
FROM replay_executions
WHERE session_id = $1
ORDER BY created_at DESC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list executions: %w", err)
	}
	defer rows.Close()

	out := []ReplayExecution{}
	for rows.Next() {
		var e ReplayExecution
		if err := rows.Scan(&e.ReplayExecutionID, &e.SessionID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("list executions scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LatestExecutionID returns the newest replay_execution_id for a session, or "".
func (s *Store) LatestExecutionID(ctx context.Context, sessionID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
SELECT replay_execution_id
FROM replay_executions
WHERE session_id = $1
ORDER BY created_at DESC
LIMIT 1`, sessionID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("latest execution: %w", err)
	}
	return id, nil
}

// ResolveExecutionID returns execID when non-empty, otherwise the latest for sessionID.
func (s *Store) ResolveExecutionID(ctx context.Context, sessionID, execID string) (string, error) {
	if execID != "" {
		return execID, nil
	}
	return s.LatestExecutionID(ctx, sessionID)
}

// GetSessionDiffs returns joined diff_reports + traces for one session execution.
func (s *Store) GetSessionDiffs(ctx context.Context, sessionID, execID string) ([]SessionDiff, error) {
	execID, err := s.ResolveExecutionID(ctx, sessionID, execID)
	if err != nil {
		return nil, err
	}
	if execID == "" {
		return []SessionDiff{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT d.trace_id, COALESCE(d.session_id, ''), d.replay_execution_id, d.signature, d.source_type,
       COALESCE(t.method, ''), COALESCE(t.path, ''),
       COALESCE(t.status_code_a, ''), COALESCE(t.status_code_b, ''),
       COALESCE(t.status_code_candidate, ''),
       d.control_a_payload, d.control_b_payload, d.candidate_payload,
       d.noise_diff, d.regression_diff, d.verdict::text,
       COALESCE(t.created_at, d.created_at)
FROM diff_reports d
LEFT JOIN traces t
  ON t.trace_id = d.trace_id AND t.replay_execution_id = d.replay_execution_id
WHERE d.session_id = $1 AND d.replay_execution_id = $2
ORDER BY COALESCE(t.created_at, d.created_at) DESC, d.signature`, sessionID, execID)
	if err != nil {
		return nil, fmt.Errorf("get session diffs: %w", err)
	}
	defer rows.Close()

	out := []SessionDiff{}
	for rows.Next() {
		var d SessionDiff
		var controlA, controlB, candidate, noise, regression []byte
		if err := rows.Scan(
			&d.TraceID, &d.SessionID, &d.ReplayExecutionID, &d.Signature, &d.SourceType,
			&d.Method, &d.Path, &d.StatusCodeA, &d.StatusCodeB, &d.StatusCodeCandidate,
			&controlA, &controlB, &candidate, &noise, &regression,
			&d.Verdict, &d.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("get session diffs scan: %w", err)
		}
		d.ControlAPayload = nullRaw(controlA)
		d.ControlBPayload = nullRaw(controlB)
		d.CandidatePayload = nullRaw(candidate)
		d.NoiseDiff = nullRaw(noise)
		d.RegressionDiff = nullRaw(regression)
		out = append(out, d)
	}
	return out, rows.Err()
}

// SessionSummary counts traces by verdict for one session execution.
func (s *Store) SessionSummary(ctx context.Context, sessionID, execID string) (SessionSummary, error) {
	execID, err := s.ResolveExecutionID(ctx, sessionID, execID)
	if err != nil {
		return SessionSummary{}, err
	}
	sum := SessionSummary{SessionID: sessionID, ReplayExecutionID: execID}
	if execID == "" {
		return sum, nil
	}
	err = s.db.QueryRowContext(ctx, `
SELECT
  COUNT(*)::int,
  COUNT(*) FILTER (WHERE verdict = 'MATCH')::int,
  COUNT(*) FILTER (WHERE verdict = 'MISMATCH')::int,
  COUNT(*) FILTER (WHERE verdict = 'VOIDED_BASELINE_DIVERGENCE')::int
FROM traces
WHERE session_id = $1 AND replay_execution_id = $2`, sessionID, execID).
		Scan(&sum.Total, &sum.Match, &sum.Mismatch, &sum.Voided)
	if err != nil {
		return SessionSummary{}, fmt.Errorf("session summary: %w", err)
	}
	return sum, nil
}

func nullRaw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}
