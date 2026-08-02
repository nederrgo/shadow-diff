package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/url"
	"os"
	"sort"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/shadow-diff/beru/migrations"
)

var _ RunStore = (*PostgresStore)(nil)

const (
	defaultPostgresPort    = "5432"
	defaultPostgresSSLMode = "require"
	startTimeLayout = "2006-01-02 15:04:05"
)

// PostgresConfig is the BYO-Postgres connection, read from DB_* env vars.
type PostgresConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	SSLMode  string
}

// PostgresConfigFromEnv reads DB_* and rejects a partially configured backend.
// A durability backend that silently is not there is worse than a boot failure.
func PostgresConfigFromEnv() (PostgresConfig, error) {
	cfg := PostgresConfig{
		Host:     os.Getenv("DB_HOST"),
		Port:     os.Getenv("DB_PORT"),
		User:     os.Getenv("DB_USER"),
		Password: os.Getenv("DB_PASSWORD"),
		Name:     os.Getenv("DB_NAME"),
		SSLMode:  os.Getenv("DB_SSLMODE"),
	}
	if cfg.Port == "" {
		cfg.Port = defaultPostgresPort
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = defaultPostgresSSLMode
	}
	var missing []string
	for _, f := range []struct {
		name, value string
	}{
		{"DB_HOST", cfg.Host},
		{"DB_USER", cfg.User},
		{"DB_NAME", cfg.Name},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return PostgresConfig{}, fmt.Errorf("postgres requires %v", missing)
	}
	return cfg, nil
}

// DSN renders the connection string. url.UserPassword escapes credentials, so a
// password containing @ / : / # does not corrupt the URL.
// connect_timeout keeps unreachable-host flush attempts from hanging on dial
// longer than the WAL flush context (poison-pill / outage paths).
func (c PostgresConfig) DSN() string {
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.User, c.Password),
		Host:     net.JoinHostPort(c.Host, c.Port),
		Path:     "/" + c.Name,
		RawQuery: url.Values{
			"sslmode":         {c.SSLMode},
			"connect_timeout": {"5"},
		}.Encode(),
	}
	return u.String()
}

// PostgresStore is the durable backend. One type satisfies both halves of
// Beru's persistence: RunStore and v2/storage.TraceRepository.
type PostgresStore struct {
	db              *sql.DB
	log             *slog.Logger
	retentionDays   int
	defaultTestName string
	sessionID       string
}

// OpenPostgres connects, migrates, and records the shadow session.
func OpenPostgres(log *slog.Logger, cfg PostgresConfig) (*PostgresStore, error) {
	if log == nil {
		log = slog.Default()
	}
	db, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	// Headroom for 8 WAL flusher workers plus reaper / HTTP trace reads.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres %s:%s/%s: %w", cfg.Host, cfg.Port, cfg.Name, err)
	}

	p := &PostgresStore{
		db:              db,
		log:             log,
		retentionDays:   retentionDaysFromEnv(),
		defaultTestName: shadowTestNameFromEnv(),
		sessionID:       os.Getenv("SESSION_ID"),
	}
	if err := p.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := p.ensureSession(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := p.EnsureShadowTest(ctx, p.defaultTestName); err != nil {
		db.Close()
		return nil, err
	}
	go p.retentionLoop()

	log.Info("PostgreSQL storage ready",
		"host", cfg.Host, "port", cfg.Port, "database", cfg.Name, "sslmode", cfg.SSLMode,
		"retention_days", p.retentionDays, "session_id", p.sessionID)
	return p, nil
}

// Close shuts down the connection pool.
func (p *PostgresStore) Close() error {
	if p == nil || p.db == nil {
		return nil
	}
	return p.db.Close()
}

// migrate applies every embedded .sql file once, tracked in schema_migrations.
func (p *PostgresStore) migrate(ctx context.Context) error {
	if _, err := p.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    TEXT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("postgres migrate bootstrap: %w", err)
	}

	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return fmt.Errorf("postgres migrate glob: %w", err)
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		if err := p.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("postgres migrate check %s: %w", name, err)
		}
		if applied {
			continue
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("postgres migrate read %s: %w", name, err)
		}
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("postgres migrate begin %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("postgres migrate apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			tx.Rollback()
			return fmt.Errorf("postgres migrate record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("postgres migrate commit %s: %w", name, err)
		}
		p.log.Info("Applied migration", "version", name)
	}
	return nil
}

// ensureSession records the Monarch-minted session so Tusk can join diff rows
// against the S3 layout shadow-diff/<ns>/<test>/sessions/<session-id>/.
func (p *PostgresStore) ensureSession(ctx context.Context) error {
	if p.sessionID == "" {
		return nil
	}
	_, err := p.db.ExecContext(ctx, `
INSERT INTO shadow_sessions (session_id, shadow_test_name, namespace, mode)
VALUES ($1, $2, $3, $4)
ON CONFLICT (session_id) DO UPDATE SET
  shadow_test_name = excluded.shadow_test_name,
  namespace        = excluded.namespace,
  mode             = excluded.mode`,
		p.sessionID, p.defaultTestName, os.Getenv("SHADOW_NAMESPACE"), os.Getenv("SHADOW_MODE"))
	if err != nil {
		return fmt.Errorf("ensure shadow session: %w", err)
	}
	return nil
}

// DefaultShadowTestName returns the configured default shadow test name.
func (p *PostgresStore) DefaultShadowTestName() string {
	if p.defaultTestName == "" {
		return defaultShadowTest
	}
	return p.defaultTestName
}

// EnsureShadowTest creates the shadow test run row if missing.
func (p *PostgresStore) EnsureShadowTest(ctx context.Context, name string) error {
	if name == "" {
		name = defaultShadowTest
	}
	var id int64
	err := p.db.QueryRowContext(ctx,
		`SELECT id FROM shadow_tests WHERE name = $1 ORDER BY id DESC LIMIT 1`, name,
	).Scan(&id)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("ensure shadow test lookup: %w", err)
	}
	// pgx does not implement LastInsertId; RETURNING is the Postgres idiom.
	if err := p.db.QueryRowContext(ctx,
		`INSERT INTO shadow_tests (name) VALUES ($1) RETURNING id`, name,
	).Scan(&id); err != nil {
		return fmt.Errorf("ensure shadow test insert: %w", err)
	}
	return nil
}

// ListShadowTests returns recent shadow test runs.
func (p *PostgresStore) ListShadowTests(ctx context.Context, limit int) ([]ShadowTest, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := p.db.QueryContext(ctx, `
SELECT id, name, start_time, total_traces, mismatch_count
FROM shadow_tests
ORDER BY id DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list shadow tests: %w", err)
	}
	defer rows.Close()

	var out []ShadowTest
	for rows.Next() {
		st, err := scanShadowTest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// GetShadowTest returns one run by id.
func (p *PostgresStore) GetShadowTest(ctx context.Context, id int64) (ShadowTest, error) {
	var (
		st        ShadowTest
		startTime time.Time
	)
	err := p.db.QueryRowContext(ctx, `
SELECT id, name, start_time, total_traces, mismatch_count
FROM shadow_tests WHERE id = $1`, id,
	).Scan(&st.ID, &st.Name, &startTime, &st.TotalTraces, &st.MismatchCount)
	if err != nil {
		return ShadowTest{}, fmt.Errorf("get shadow test: %w", err)
	}
	st.StartTime = startTime.UTC().Format(startTimeLayout)
	st.MatchRate = matchRate(st.TotalTraces, st.MismatchCount)
	return st, nil
}

func scanShadowTest(rows *sql.Rows) (ShadowTest, error) {
	var (
		st        ShadowTest
		startTime time.Time
	)
	if err := rows.Scan(&st.ID, &st.Name, &startTime, &st.TotalTraces, &st.MismatchCount); err != nil {
		return ShadowTest{}, err
	}
	st.StartTime = startTime.UTC().Format(startTimeLayout)
	st.MatchRate = matchRate(st.TotalTraces, st.MismatchCount)
	return st, nil
}

func matchRate(total, mismatches int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(total-mismatches) / float64(total) * 100
}

// NoisePathsForTest returns configured ignore paths for a shadow test name.
func (p *PostgresStore) NoisePathsForTest(ctx context.Context, shadowTestName string) (map[string]struct{}, error) {
	if shadowTestName == "" {
		shadowTestName = p.DefaultShadowTestName()
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT path FROM noise_filters WHERE shadow_test_name = $1`, shadowTestName)
	if err != nil {
		return nil, fmt.Errorf("noise paths: %w", err)
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
func (p *PostgresStore) AddNoiseFilter(ctx context.Context, shadowTestName, path string) error {
	if shadowTestName == "" {
		shadowTestName = p.DefaultShadowTestName()
	}
	_, err := p.db.ExecContext(ctx, `
INSERT INTO noise_filters (shadow_test_name, path) VALUES ($1, $2)
ON CONFLICT (shadow_test_name, path) DO NOTHING`, shadowTestName, path)
	if err != nil {
		return fmt.Errorf("add noise filter: %w", err)
	}
	return nil
}

// ListNoiseFilters returns filters for a shadow test name.
func (p *PostgresStore) ListNoiseFilters(ctx context.Context, shadowTestName string) ([]string, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT path FROM noise_filters WHERE shadow_test_name = $1 ORDER BY created_at`, shadowTestName)
	if err != nil {
		return nil, fmt.Errorf("list noise filters: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *PostgresStore) retentionLoop() {
	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()
	for range ticker.C {
		if err := p.Prune(context.Background()); err != nil {
			p.log.Error("Retention prune failed", "err", err)
		}
	}
}

// Prune deletes reports past the retention window plus the rows derived from them.
func (p *PostgresStore) Prune(ctx context.Context) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM raw_reports WHERE captured_at < now() - ($1 || ' days')::interval`,
		p.retentionDays); err != nil {
		return fmt.Errorf("prune raw_reports: %w", err)
	}
	for _, stmt := range []string{
		`DELETE FROM verdicts     WHERE trace_id NOT IN (SELECT DISTINCT trace_id FROM raw_reports)`,
		`DELETE FROM diff_reports WHERE trace_id NOT IN (SELECT DISTINCT trace_id FROM raw_reports)`,
		`DELETE FROM traces       WHERE trace_id NOT IN (SELECT DISTINCT trace_id FROM raw_reports)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("prune derived rows: %w", err)
		}
	}
	return tx.Commit()
}

// jsonbValue coerces a SummaryDetails string into something JSONB accepts.
// Production always writes json.Marshal(VerdictDetails); the string-wrap
// fallback keeps a malformed value storable instead of failing the whole
// verdict write, and jsonbString reverses it on read.
func jsonbValue(s string) any {
	if s == "" {
		return nil
	}
	if json.Valid([]byte(s)) {
		return s
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return string(b)
}

func jsonbString(ns sql.NullString) string {
	if !ns.Valid || ns.String == "" {
		return ""
	}
	// A bare JSON string scalar means jsonbValue wrapped a non-JSON original.
	var unwrapped string
	if err := json.Unmarshal([]byte(ns.String), &unwrapped); err == nil {
		return unwrapped
	}
	return ns.String
}
