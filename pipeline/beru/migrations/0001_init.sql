-- Beru PostgreSQL schema.
--
-- Two layers:
--   1. Source of truth  — raw_reports / verdicts / shadow_tests / noise_filters.
--      A faithful port of the SQLite schema. Beru's engine reads and writes these.
--   2. UI projection     — shadow_sessions / replay_executions / traces / diff_reports.
--      Derived, rebuilt on every SaveDiffVerdict. Safe to truncate.
--
-- Diff rows are scoped by (session_id, replay_execution_id) so re-playing the
-- same S3 session does not inflate occurrence counts across runs.

-- ---------------------------------------------------------------- source of truth

CREATE TABLE IF NOT EXISTS raw_reports (
  id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  trace_id             TEXT NOT NULL,
  shadow_role          TEXT NOT NULL,
  shadow_test_name     TEXT NOT NULL DEFAULT '',
  session_id           TEXT NOT NULL DEFAULT '',
  replay_execution_id  TEXT NOT NULL,
  protocol             TEXT NOT NULL,
  direction            TEXT NOT NULL,
  signature            TEXT NOT NULL,
  status_code          TEXT NOT NULL DEFAULT '',
  -- BYTEA, not JSONB: HTTP ingress bodies come out of ingressCodec.Normalize and
  -- are not guaranteed to be JSON. A JSONB column would reject the INSERT and
  -- drop the report on the ingest hot path.
  payload_bytes        BYTEA,
  captured_at          TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_raw_reports_trace_id ON raw_reports (trace_id);
CREATE INDEX IF NOT EXISTS idx_raw_reports_shadow_test ON raw_reports (shadow_test_name, captured_at);
CREATE INDEX IF NOT EXISTS idx_raw_reports_exec_trace ON raw_reports (shadow_test_name, session_id, replay_execution_id, trace_id);
CREATE INDEX IF NOT EXISTS idx_raw_reports_trace_sig ON raw_reports (replay_execution_id, trace_id, signature);

-- shadow_test_name is denormalised from raw_reports: many beru-local pods share
-- one database, and without it "every verdict for ShadowTest X" needs a join.
CREATE TABLE IF NOT EXISTS verdicts (
  replay_execution_id  TEXT NOT NULL,
  trace_id             TEXT NOT NULL,
  shadow_test_name     TEXT NOT NULL DEFAULT '',
  session_id           TEXT NOT NULL DEFAULT '',
  status               TEXT NOT NULL,
  has_count_regression BOOLEAN NOT NULL,
  summary_details      JSONB,
  updated_at           TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (replay_execution_id, trace_id)
);

CREATE INDEX IF NOT EXISTS idx_verdicts_shadow_test ON verdicts (shadow_test_name, updated_at);
CREATE INDEX IF NOT EXISTS idx_verdicts_exec ON verdicts (shadow_test_name, session_id, replay_execution_id);

CREATE TABLE IF NOT EXISTS shadow_tests (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name           TEXT NOT NULL,
  start_time     TIMESTAMPTZ NOT NULL DEFAULT now(),
  total_traces   INTEGER NOT NULL DEFAULT 0,
  mismatch_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_shadow_tests_name ON shadow_tests (name);

CREATE TABLE IF NOT EXISTS noise_filters (
  id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shadow_test_name TEXT NOT NULL,
  path             TEXT NOT NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (shadow_test_name, path)
);

-- ---------------------------------------------------------------- UI projection

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'verdict_kind') THEN
    CREATE TYPE verdict_kind AS ENUM (
      'MATCH',
      'MISMATCH',
      'VOIDED_BASELINE_DIVERGENCE',
      'WAITING_FOR_ROLES'
    );
  END IF;
END
$$;

CREATE TABLE IF NOT EXISTS shadow_sessions (
  session_id       TEXT PRIMARY KEY,
  shadow_test_name TEXT NOT NULL,
  namespace        TEXT NOT NULL DEFAULT '',
  mode             TEXT NOT NULL DEFAULT '',
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One S3 session may be replayed many times; each run gets a fresh execution id.
CREATE TABLE IF NOT EXISTS replay_executions (
  replay_execution_id TEXT PRIMARY KEY,
  session_id          TEXT NOT NULL REFERENCES shadow_sessions (session_id) ON DELETE CASCADE,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_replay_executions_session
  ON replay_executions (session_id, created_at DESC);

CREATE TABLE IF NOT EXISTS traces (
  replay_execution_id   TEXT NOT NULL,
  trace_id              TEXT NOT NULL,
  session_id            TEXT REFERENCES shadow_sessions (session_id) ON DELETE SET NULL,
  shadow_test_name      TEXT NOT NULL DEFAULT '',
  path                  TEXT NOT NULL DEFAULT '',
  method                TEXT NOT NULL DEFAULT '',
  status_code_a         TEXT NOT NULL DEFAULT '',
  status_code_b         TEXT NOT NULL DEFAULT '',
  status_code_candidate TEXT NOT NULL DEFAULT '',
  verdict               verdict_kind,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (replay_execution_id, trace_id)
);

-- One row per signature bucket, not per trace: a trace fans out to N egress
-- operations and diff.EvaluateTraceHistory pairs them by signature.
CREATE TABLE IF NOT EXISTS diff_reports (
  id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  replay_execution_id  TEXT NOT NULL,
  trace_id             TEXT NOT NULL,
  session_id           TEXT,
  signature            TEXT NOT NULL,
  source_type          TEXT NOT NULL,
  control_a_payload    JSONB,
  control_b_payload    JSONB,
  candidate_payload    JSONB,
  noise_diff           JSONB,
  regression_diff      JSONB,
  verdict              verdict_kind NOT NULL,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (replay_execution_id, trace_id, signature)
);

CREATE INDEX IF NOT EXISTS idx_traces_session       ON traces (session_id);
CREATE INDEX IF NOT EXISTS idx_traces_exec          ON traces (session_id, replay_execution_id);
CREATE INDEX IF NOT EXISTS idx_traces_verdict       ON traces (verdict);
CREATE INDEX IF NOT EXISTS idx_traces_shadow_test   ON traces (shadow_test_name, created_at);
CREATE INDEX IF NOT EXISTS idx_diff_reports_session ON diff_reports (session_id);
CREATE INDEX IF NOT EXISTS idx_diff_reports_exec    ON diff_reports (session_id, replay_execution_id, trace_id);
CREATE INDEX IF NOT EXISTS idx_diff_reports_trace   ON diff_reports (trace_id);
CREATE INDEX IF NOT EXISTS idx_diff_reports_verdict ON diff_reports (verdict);
