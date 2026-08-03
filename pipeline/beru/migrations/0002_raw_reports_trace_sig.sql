-- Folded into 0001_init.sql as idx_raw_reports_trace_sig
-- (replay_execution_id, trace_id, signature). Kept so schema_migrations
-- stays a no-op on greenfield boots that already recorded this version.
SELECT 1;
