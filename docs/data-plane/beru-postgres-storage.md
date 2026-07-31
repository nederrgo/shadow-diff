---
type: Architecture Specification
title: Beru Storage Backends
description: Beru's two storage halves behind RunStore and TraceRepository, the SQLite and BYO-PostgreSQL drivers selected by DB_DRIVER, the UI projection tables, and Beru's egress network surface.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/internal/storage
tags: [data-plane, beru, storage, postgres, sqlite, persistence, networking]
timestamp: 2026-07-30T18:00:00Z
---

# Beru Storage Backends

Beru persists behind two interfaces. `DB_DRIVER` picks which driver satisfies them.

| Interface | Owns | Consumers |
| --- | --- | --- |
| `v2/storage.TraceRepository` | `raw_reports`, `verdicts` | `TraceRouter`, dashboard |
| `storage.RunStore` | `shadow_tests`, `noise_filters` | `TraceRouter`, dashboard, HTTP API |

| Driver | `DB_DRIVER` | Types | Durability |
| --- | --- | --- | --- |
| SQLite | unset / anything else | `*storage.DB` + `*v2storage.SQLiteRepository` | File at `BERU_DB_PATH`; tmpfs EmptyDir under beru-local |
| PostgreSQL | `postgres` | `*storage.PostgresStore` satisfies both | Survives pod and ShadowTest deletion |

`storage.OpenBackend` returns the pair as a `Backend`. Selecting `postgres` opens no SQLite file. A `DB_DRIVER=postgres` with missing `DB_HOST` / `DB_USER` / `DB_NAME` fails the boot rather than falling back, so storage never silently stops being durable.

## How beru-local reaches PostgreSQL

Every ShadowTest gets its own `beru-local` pod inside the shadow namespace, which is deleted with the ShadowTest. Durable storage is what lets its diff history outlive that.

| Step | Where |
| --- | --- |
| `BERU_DB_SECRET` on the manager names the Secret, as `namespace/name` or a bare `name` in `monarch-system` | `beruDBSecretRef`, `shadowtest_beru_db.go` |
| Monarch replicates that Secret into `shadow-<ns>-<name>` on each reconcile | `syncBeruDBSecret`, called before `reconcileLocalBeruIfNeeded` |
| beru-local mounts it wholesale through `envFrom` | `localBeruPodSpec`, `shadowtest_beru_local.go` |

`envFrom` passes through whatever keys the Secret holds, so a new connection setting needs no controller change. The copy is garbage-collected when `reconcileDelete` removes the namespace; the rows in PostgreSQL are not.

With the Secret configured, `localBeruPodSpec` also drops `BERU_DB_PATH` and the `beru-sqlite-data` volume — the 64Mi in-memory EmptyDir is charged against the container's 128Mi limit and no SQLite file is opened. Leaving `BERU_DB_SECRET` unset keeps beru-local on tmpfs SQLite.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `DB_DRIVER` | *(unset)* | `postgres` selects the durable backend |
| `DB_HOST` | — | Required when driver is `postgres` |
| `DB_PORT` | `5432` | |
| `DB_USER` | — | Required |
| `DB_PASSWORD` | — | Supply from a Secret |
| `DB_NAME` | — | Required |
| `DB_SSLMODE` | `require` | |
| `SESSION_ID` | `""` | Monarch's session; keys `shadow_sessions` |
| `SHADOW_NAMESPACE` | `""` | Recorded on the session row |
| `SHADOW_MODE` | `""` | `record` or `replay` |

Beru reads all of these from its own process env. The first seven arrive from the Secret; Monarch sets the last three directly on beru-local from `st.Status.CurrentSessionID`, `st.Namespace` and `st.Spec.Mode`, so `session_id` matches the S3 layout `shadow-diff/<namespace>/<test>/sessions/<session-id>/`.

`BERU_DB_SECRET` is read by Monarch, not Beru, and is operator-wide: every beru-local shares one database, partitioned by `shadow_test_name` and `session_id`.

## Schema layers

**Source of truth** — the engine's read/write path.

| Table | Key | Notes |
| --- | --- | --- |
| `raw_reports` | `id` | Append-only. `payload_bytes` is `BYTEA`: HTTP ingress bodies are not guaranteed JSON, and a `JSONB` column would reject the insert on the ingest hot path |
| `verdicts` | `trace_id` | Upserted; `summary_details` is `JSONB`. `shadow_test_name` is denormalised from `raw_reports` so a shared database can be filtered by tenant without a join — PostgreSQL only, since SQLite serves a single ShadowTest |
| `shadow_tests` | `id` | Created lazily per shadow test name |
| `noise_filters` | `(shadow_test_name, path)` | User ignore paths |

**UI projection** — derived, rebuilt on every `SaveDiffVerdict`, safe to truncate.

| Table | Key | Notes |
| --- | --- | --- |
| `shadow_sessions` | `session_id` | Written once at boot |
| `traces` | `trace_id` | `method` / `path` recovered from the HTTP ingress signature; one status column per role |
| `diff_reports` | `(trace_id, signature)` | One row per signature bucket, matching how `EvaluateTraceHistory` pairs reports. Payloads that are not JSON are wrapped as `{"_raw": "..."}` |

A projection fault is logged and swallowed — the verdict write still succeeds.

Migrations live in `pipeline/beru/migrations/*.sql`, embedded via `go:embed` and applied once each, tracked in `schema_migrations`.

## Dialect differences

| SQLite | PostgreSQL |
| --- | --- |
| `?` placeholders | `$1..$N` |
| `LastInsertId()` | `INSERT ... RETURNING id` (pgx implements no `LastInsertId`) |
| `INSERT OR IGNORE` | `ON CONFLICT ... DO NOTHING` |
| `datetime('now', printf('-%d days', ?))` | `now() - ($1 \|\| ' days')::interval` |
| `has_count_regression` as `0`/`1` | native `BOOLEAN` |
| `captured_at` as RFC3339Nano text | native `TIMESTAMPTZ`, re-rendered on read so the dashboard sees one format |
| `HAVING` may reference a `SELECT` alias | Aggregates must be repeated in `HAVING` |
| `MaxOpenConns(1)` (single-writer) | Pool of 10 |

## Network surface

Beru runs unrestricted. NetworkPolicy is deny-only, so a pod no policy selects reaches whatever the cluster network permits — which is how beru-local reaches PostgreSQL in `monarch-system` or an external RDS, the same way Shop and Igris reach S3.

Monarch holds no `networking.k8s.io` RBAC, so egress hardening belongs to a cluster policy engine generating a NetworkPolicy into namespaces labelled `app.kubernetes.io/managed-by: monarch`. Such a policy has to admit more than Beru's own traffic: DNS, `5432` for beru-local, `9000` (or the provider's port) for Shop and Igris reaching S3, `5672` for igris-rabbitmq reaching the prod broker, and intra-namespace traffic. Since `spec.storage.endpoint` and the AMQP URLs are user-supplied and may point outside the cluster, those rules have to be port-based rather than selector-based.

## Local fixture

`testing/tools/e2e-reset-minikube.sh` always deploys PostgreSQL to `monarch-system`, beside the MinIO fixture, and exposes it on NodePort `30432`. beru-local stays on SQLite unless `BERU_POSTGRES=1`, which sets `BERU_DB_SECRET=monarch-system/beru-postgres` on the manager.

`monarch-system` enforces `pod-security.kubernetes.io/enforce: restricted`, so the fixture runs non-root with all capabilities dropped.

## Verification

```bash
export BERU_TEST_POSTGRES_DSN="postgres://beru:beru@$(minikube ip):30432/beru?sslmode=disable"
go -C pipeline/beru test ./internal/storage/... -run 'Conformance|Projection' -v
go -C pipeline/monarch test ./internal/controller/... -run 'BeruDB|LocalBeruPodSpec' -v
```

`TestSQLiteConformance` and `TestPostgresConformance` run one assertion set against both drivers; the Postgres case skips when the DSN is unset. `TestPostgresProjection` covers the derived tables. `TestLocalBeruPodSpec_*` asserts the storage-mode branch — `envFrom` present and the tmpfs volume dropped with the Secret configured, `BERU_DB_PATH` and the EmptyDir present without it.

End to end:

```bash
BERU_POSTGRES=1 ./testing/tools/e2e-reset-minikube.sh
SHADOW_NS=$(kubectl get shadowtest my-app-shadow -n default -o jsonpath='{.status.shadowNamespace}')
kubectl get secret beru-postgres -n "$SHADOW_NS"          # replicated by Monarch
kubectl logs -n "$SHADOW_NS" deploy/beru-local | head -1   # "PostgreSQL storage ready"
```

# Citations

* [/data-plane/index.md](/data-plane/index.md) — Data-plane document map
* [/data-plane/beru-analysis.md](/data-plane/beru-analysis.md) — Verdict statuses this schema stores
* [pipeline/beru/internal/storage/postgres.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/internal/storage/postgres.go) — PostgresStore
* [pipeline/beru/internal/storage/open.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/internal/storage/open.go) — Driver selection
* [pipeline/beru/migrations/0001_init.sql](https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/migrations/0001_init.sql) — Schema
* [pipeline/monarch/internal/controller/shadowtest_beru_db.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_beru_db.go) — `BERU_DB_SECRET` resolution and Secret replication
* [pipeline/monarch/internal/controller/shadowtest_beru_local.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_beru_local.go) — beru-local pod spec
