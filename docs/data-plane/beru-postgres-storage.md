---
type: Architecture Specification
title: Beru Storage Backends
description: Beru's Postgres-only persistence behind RunStore and TraceRepository, the Bbolt disk WAL with claimed parallel flushers, advisory-locked evaluate, and 3-retry dead-lettering.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/internal/storage
tags: [data-plane, beru, storage, postgres, wal, persistence, networking]
timestamp: 2026-08-18T18:40:00Z
---

# Beru Storage Backends

Beru persists behind two interfaces. Both are satisfied by `*storage.WALStore`, which wraps `*storage.PostgresStore`.

| Interface | Owns | Consumers |
| --- | --- | --- |
| `v2/storage.TraceRepository` | `raw_reports`, `verdicts` | WAL flusher, slim HTTP traces API, reaper |
| `storage.RunStore` | `shadow_tests`, `noise_filters` | TraceRouter, seed/ingest HTTP |

PostgreSQL is the sole database engine. Missing `DB_HOST` / `DB_USER` / `DB_NAME` fails boot. There is no SQLite path.

## Ingest path and disk WAL

HTTP/gRPC ingest hits `TraceRouter`, which only calls `AppendReport`. That appends a record to a Bbolt WAL at `/data/beru_wal.db` (`BERU_WAL_PATH` override) and returns immediately — handlers never wait on Postgres.

A single-threaded dispatcher owns an `inFlightTraces` map:

1. Drain worker `completionEvent`s (unclaim; set per-trace backoff on failure).
2. `View` the WAL, group pending ops by `trace_id`.
3. Skip traces that are inflight or before `nextRetryAt`.
4. Non-blocking enqueue onto one of **8** sticky FNV-hashed worker channels; claim only after a successful send.

Workers run `BEGIN` → `SELECT pg_advisory_xact_lock(hashtext(replay_execution_id || ':' || trace_id))` → insert reports → evaluate → upsert verdicts + UI projection → `COMMIT`, then delete exact WAL keys. On failure, keys stay on disk, `RetryCount` is bumped, and the dispatcher applies exponential backoff (`100ms` → `500ms` → `1s`, capped at `5s`).

After **3** consecutive failures for a batch, Beru appends a JSON line to `/data/dead_letters.jsonl` (`BERU_DEAD_LETTER_PATH`), deletes the WAL keys, and logs an error so a poison payload cannot block the pipeline.

If the WAL file exceeds 1 GiB, the oldest ~10% of pending keys are dropped before new appends.

EmptyDir WAL state does not survive pod restart. Advisory locks serialize flush/evaluate across beru-local replicas sharing one Postgres.

## How beru-local reaches PostgreSQL

Every ShadowTest gets a `beru-local` pod in the shadow namespace.

| Step | Where |
| --- | --- |
| `BERU_DB_SECRET` on the manager names the Secret (`namespace/name` or bare name in `monarch-system`). Unset or malformed fails the ShadowTest | `beruDBSecretRef`, `shadowtest_beru_db.go` |
| Manager SA `get`s that Secret via a namespaced Role (`resourceNames`); no cluster-wide Secret list/watch | `beru_db_secret_role.yaml`, `cmd/main.go` `DisableFor` Secrets |
| Monarch replicates that Secret into `shadow-<ns>-<name>` on each reconcile | `syncBeruDBSecret` |
| beru-local mounts it via `envFrom` and always mounts a disk EmptyDir at `/data` for the WAL + DLQ | `localBeruPodSpec` |

`envFrom` passes through whatever keys the Secret holds. Monarch sets `SESSION_ID`, `REPLAY_EXECUTION_ID`, `SHADOW_NAMESPACE`, and `SHADOW_MODE` directly on the pod.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `DB_HOST` | — | Required |
| `DB_PORT` | `5432` | |
| `DB_USER` | — | Required |
| `DB_PASSWORD` | — | Supply from a Secret |
| `DB_NAME` | — | Required |
| `DB_SSLMODE` | `require` | |
| `BERU_WAL_PATH` | `/data/beru_wal.db` | Bbolt WAL file |
| `BERU_DEAD_LETTER_PATH` | `/data/dead_letters.jsonl` | Poison-pill DLQ |
| `BERU_WAL_FLUSH_TIMEOUT` | `30s` | Per-attempt Postgres flush context bound |
| `SESSION_ID` | `""` | Monarch session; keys `shadow_sessions` |
| `REPLAY_EXECUTION_ID` | `legacy` | Monarch replay run; scopes diffs so re-plays do not inflate occurrences |
| `SHADOW_NAMESPACE` | `""` | Recorded on the session row |
| `SHADOW_MODE` | `""` | `record` or `replay` |

`BERU_DB_SECRET` is required on the Monarch manager (not on Beru). Missing or malformed env, or a Secret that cannot be copied, fails the ShadowTest. Every beru-local shares one database, partitioned by `shadow_test_name`, `session_id`, and `replay_execution_id`. The incomplete-trace reaper only lists rows for `BERU_SHADOW_TEST_NAME` + this execution that lack a `verdicts` row, so one beru-local cannot re-project another test's `WAITING_FOR_ROLES` onto its own session.

## Schema layers

**Source of truth** — the engine's read/write path.

| Table | Key | Notes |
| --- | --- | --- |
| `raw_reports` | `id` | Append-only. Stamped with `session_id` + `replay_execution_id`. Index on `(replay_execution_id, trace_id, signature)` for Tusk occurrence pager |
| `verdicts` | `(replay_execution_id, trace_id)` | Upserted; `summary_details` is `JSONB`. `shadow_test_name` + `session_id` denormalised |
| `shadow_tests` | `id` | Created lazily per shadow test name |
| `noise_filters` | `(shadow_test_name, path)` | User ignore paths |

**UI projection** — derived, rebuilt on every successful flush evaluate, safe to truncate.

| Table | Key | Notes |
| --- | --- | --- |
| `shadow_sessions` | `session_id` | Written once at boot |
| `replay_executions` | `replay_execution_id` | FK → `shadow_sessions`; one row per replay run |
| `traces` | `(replay_execution_id, trace_id)` | One status column per role |
| `diff_reports` | `(replay_execution_id, trace_id, signature)` | One row per signature bucket (first payload per role; repeats stay in `raw_reports`) |

After each successful projection transaction, Beru emits Postgres `NOTIFY` on channel `verdict_events` so Tusk can stream live verdict deltas to The System ShadowDiff page:

```json
{"session_id":"session-…","replay_execution_id":"exec-…","trace_id":"…","verdict":"MISMATCH"}
```

Payload fields are always present; `session_id` may be `""` if `SESSION_ID` was unset at beru-local boot. Tusk `LISTEN`s this channel and fans frames to `/ws/diffs`.

Migrations live in `pipeline/beru/migrations/*.sql`, embedded via `go:embed`. Schema is greenfield for the current testing phase — wipe Postgres when applying DDL changes rather than shipping ALTER migrations.

See [/control-plane/replay-execution-isolation.md](/control-plane/replay-execution-isolation.md).

## Network surface

Beru runs unrestricted. NetworkPolicy is deny-only, so beru-local reaches PostgreSQL in `monarch-system` or an external RDS the same way Shop and Igris reach S3.

## Local fixture

`testing/tools/e2e-reset-minikube.sh` always deploys PostgreSQL to `monarch-system` and sets `BERU_DB_SECRET=monarch-system/beru-postgres` on the manager. Host access:

```bash
export BERU_TEST_POSTGRES_DSN="postgres://beru:beru@$(minikube ip):30432/beru?sslmode=disable"
go -C pipeline/beru test ./internal/storage/... -run 'Conformance|Projection|WAL|concurrentFlushSameTrace' -v
go -C pipeline/monarch test ./internal/controller/... -run 'BeruDB|LocalBeruPodSpec' -v
```

`TestPostgresConformance` skips when the DSN is unset. `TestPostgres_concurrentFlushSameTrace` opens two store pools and races `flushReportsAndEvaluate` on the same `trace_id` under `pg_advisory_xact_lock`. `TestWAL_*` exercises claim-skip and 3-strike dead-lettering against a temp Bbolt file. `TestLocalBeruPodSpec_postgresAndWAL` asserts the WAL EmptyDir and Postgres `envFrom` are both mounted.

# Citations

* [/data-plane/index.md](/data-plane/index.md) — Data-plane document map
* [/data-plane/beru-analysis.md](/data-plane/beru-analysis.md) — Verdict statuses this schema stores
* [pipeline/beru/internal/storage/wal.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/internal/storage/wal.go) — WAL + claimed flusher pool
* [pipeline/beru/internal/storage/postgres.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/internal/storage/postgres.go) — PostgresStore
* [pipeline/beru/migrations/0001_init.sql](https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/migrations/0001_init.sql) — Schema
* [pipeline/monarch/internal/controller/shadowtest_beru_local.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_beru_local.go) — beru-local pod spec
* [/control-plane/tusk-bff.md](/control-plane/tusk-bff.md) — Tusk LISTEN/NOTIFY consumer and ShadowDiff APIs
