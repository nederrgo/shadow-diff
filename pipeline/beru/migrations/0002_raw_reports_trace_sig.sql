-- Speeds Tusk GET /api/v1/diffs/occurrences?trace_id=&signature= (lazy UI pager).
CREATE INDEX IF NOT EXISTS idx_raw_reports_trace_sig ON raw_reports (trace_id, signature);
