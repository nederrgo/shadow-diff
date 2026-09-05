# Beru

Beru is the **L5 — analysis sink** for Shadow-Diff. It correlates traffic from the three shadow roles (control-a, control-b, candidate), runs **diff-of-diffs** to separate noise from regressions, and serves **egress mock responses** for strict downstream replay. Trace inspection UI lives in [The System](../../docs/control-plane/the-system.md) (ShadowDiff via Tusk + Postgres).

Monarch provisions Beru automatically: one **`beru-local`** pod per ShadowTest, inside that ShadowTest's shadow namespace. `BERU_DB_SECRET` on the Monarch manager names a Secret with `DB_*` keys; Monarch replicates it into the shadow namespace and mounts it via `envFrom`. Diff history lives in that shared PostgreSQL — see [docs/data-plane/beru-postgres-storage.md](../../docs/data-plane/beru-postgres-storage.md). For how Beru fits in the full pipeline see [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md).

---

## Role in the pipeline


| Path                     | How Beru receives data                          | What Beru does                                                       |
| ------------------------ | ----------------------------------------------- | -------------------------------------------------------------------- |
| **Ingress (HTTP)**       | Envoy sidecar **ingress `ext_proc`** (gRPC)     | Collects one response per role per trace → **diff-of-diffs**         |
| **Egress diff (database)** | **shadow-soldier** sidecar HTTP API              | Compares MongoDB / PostgreSQL / Redis / MSSQL query sequences across the three roles, with N+1 detection |
| **Egress diff (AMQP)**   | **egress-relay-rabbitmq** HTTP API              | Compares outbound broker publishes across the three roles (same sequence engine) |


### Diff-of-diffs (ingress and egress)

**Ingress** (one HTTP response per role):

1. **Diff(control-a, control-b)** → fields that differ on identical builds (noise).
2. **Diff(control-a, candidate)** → total changes.
3. **Regressions** ≈ changes that are not explained by noise.

**Egress** (ordered operation sequences per role — MongoDB queries, AMQP publishes, etc.):

1. Each role may report **multiple** egress operations per trace (append order preserved).
2. Operations are **paired by signature**, not strict index — e.g. `mongodb:insert:orders`, `rabbitmq:publish:orders:order.created`. Out-of-order side effects with the same signature still match.
3. For each matched pair, Beru runs the same diff-of-diffs as ingress (noise from control-a vs control-b, then regressions on control-a vs candidate).
4. **N+1 detection:** if the candidate has more operations than control-a, Beru flags a **count regression** (`expected N queries/messages but got N+1`). This catches extra loops, duplicate publishes, and spurious DB writes without mis-aligning later operations.
5. When counts match but the candidate introduces an operation with no control-a signature, Beru reports an **unexpected extra egress** for that signature.

Every inbound report is appended to the Bbolt WAL immediately, then flushed to Postgres. Each arrival triggers a **full timeline re-diff** for that trace. Evaluation is a strict 3-step pipeline: (1) wait for all three roles or mark `WAITING_FOR_ROLES` after `BERU_TRACE_TIMEOUT`, (2) void the trace on control baseline divergence (`VOIDED_BASELINE_DIVERGENCE`), (3) compound-diff the candidate against control-a (`MATCH` / `MISMATCH` with JSON detail flags). Results land in `raw_reports` and `verdicts`. User-configured **noise filters** suppress known flaky JSON paths before `MISMATCH_PAYLOAD` is recorded. See [docs/data-plane/beru-analysis.md](../../docs/data-plane/beru-analysis.md).

---

## Quick start

### Build and test

From the repo root:

```sh
make beru-build    # → pipeline/beru/bin/beru
make beru-test
```

From this directory:

```sh
make build
make test
```

### Run locally

```sh
./bin/beru
# gRPC :50051, HTTP :8080 (defaults)
```

Inspect diffs in The System at `/diffs` (Tusk reads Beru's Postgres projection tables).

### In Kubernetes

Beru ships no manifests. Build the image and let Monarch place it:

```sh
make docker-build BERU_IMG=beru:dev
kubectl set env deployment/monarch-controller-manager -n monarch-system BERU_IMAGE=beru:dev
```

Monarch creates Deployment and Service `beru-local` in each shadow namespace:


| Port      | Protocol | Purpose                                                       |
| --------- | -------- | ------------------------------------------------------------- |
| **50051** | gRPC     | `TrafficReporter`, Envoy `ext_proc`                           |
| **8080**  | HTTP     | Egress/wire ingest, seed, slim trace detail API               |
| **8081**  | HTTP     | Egress ingest for in-pod sidecars (Service port → container 8080) |


In-pod sidecars post to `:8081` rather than `:8080`, because the shadow pod's iptables rules REDIRECT outbound 8080 into Envoy's egress listener.

History outlives the ShadowTest in shared Postgres. Browse it in The System `/diffs` (Tusk connects to the same database).

---

## Configuration


| Variable                  | Default                    | Description                                            |
| ------------------------- | -------------------------- | ------------------------------------------------------ |
| `BERU_GRPC_ADDR`          | `:50051`                   | gRPC listen address (ext_proc, TrafficReporter)        |
| `BERU_HTTP_ADDR`          | `:8080`                    | HTTP listen address (ingest, seed, trace detail)       |
| `DB_HOST` / `DB_USER` / `DB_NAME` | —                   | Required Postgres connection (boot fails if missing)   |
| `DB_PORT` / `DB_PASSWORD` / `DB_SSLMODE` | `5432` / — / `require` | Postgres connection details                    |
| `BERU_WAL_PATH`           | `/data/beru_wal.db`        | Bbolt disk WAL for ingest buffering                    |
| `BERU_DEAD_LETTER_PATH`   | `/data/dead_letters.jsonl` | Poison-pill DLQ after 3 flush failures                 |
| `BERU_WAL_FLUSH_TIMEOUT`  | `30s`                      | Per-attempt Postgres flush context bound               |
| `BERU_DB_RETENTION_DAYS`  | `7`                        | Purge `raw_reports` older than N days; orphan `verdicts` removed |
| `BERU_SHADOW_TEST_NAME`   | `default`                  | Default shadow test name when ingest metadata omits it |
| `BERU_TRACE_TIMEOUT`      | `10s`                      | Incomplete traces older than this become `WAITING_FOR_ROLES` |


---

## Storage and lifecycle

Beru uses **PostgreSQL** as the sole database, fronted by a **Bbolt disk WAL** so ingest never blocks on DB blips. See [docs/data-plane/beru-postgres-storage.md](../../docs/data-plane/beru-postgres-storage.md).

### Overview

| Layer | What it holds | Where | Survives restart? |
| ----- | ------------- | ----- | ----------------- |
| **Disk WAL** | Unflushed `append_report` ops | Bbolt `/data/beru_wal.db` | No (EmptyDir) |
| **State engine** | Every report + latest verdict per trace | Postgres `raw_reports`, `verdicts` | Yes |
| **Shadow test runs** | Run names + noise filter scope | Postgres `shadow_tests`, `noise_filters` | Yes |

### State engine (`internal/{diff,engine,model,report}/` + `internal/storage/`)

All ingress and egress sources normalize to a `RawReport` and hit the **TraceRouter**:

```
Handler → TraceRouter (FNV-sharded worker)
       → AppendReport (Bbolt WAL, returns immediately)
       → WAL flusher (8 workers, inFlight claim, pg_advisory_xact_lock)
       → INSERT raw_reports → EvaluateTraceHistory → UPSERT verdicts
```

| Table | Write model | Contents |
| ----- | ----------- | -------- |
| `raw_reports` | Append-only | `trace_id`, `shadow_role`, `shadow_test_name`, `protocol`, `direction`, `signature`, `status_code`, payload bytes, `captured_at` |
| `verdicts` | Upsert on `trace_id` | `MATCH` / `MISMATCH` / `VOIDED_BASELINE_DIVERGENCE` / `WAITING_FOR_ROLES`, count-regression flag, JSON `summary_details` |

Late-arriving spans **re-open** the timeline and overwrite the verdict — including rows previously marked `WAITING_FOR_ROLES`. A background reaper finalizes incomplete traces past `BERU_TRACE_TIMEOUT` (default 10s).

`shadow_test_name` is set from ingest metadata (`shadow_test_name` on gRPC/HTTP, `x-shadow-test-name` on ext_proc) or falls back to `BERU_SHADOW_TEST_NAME`.

### Retention

A background job runs **every hour** and deletes `raw_reports` rows older than `BERU_DB_RETENTION_DAYS` (default 7), then removes `verdicts` whose `trace_id` no longer appears in `raw_reports`. Noise filters are **not** auto-deleted.

---

## API surfaces

Beru exposes gRPC (`:50051`) and HTTP (`:8080`). Detailed request/response schemas will live in a dedicated API doc; summary below.

### gRPC (`:50051`)


| Service                                  | Purpose                                                                                                                                            |
| ---------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `**TrafficReporter.ReportTraffic`**      | Direct ingress reports (role, trace_id, payload) — used for tests and integrations                                                                 |
| `**ExternalProcessor` (Envoy ext_proc)** | Observes shadow app responses → TraceRouter (ingress diff only) |


Protobuf: `[api/proto/beru/v1/traffic.proto](api/proto/beru/v1/traffic.proto)` (Beru gRPC). Regenerate Beru protos with `make proto`.

### HTTP (`:8080`)


| Endpoint                                        | Purpose                                                                 |
| ----------------------------------------------- | ----------------------------------------------------------------------- |
| `GET /healthz`                                  | Liveness                                                                |
| `POST /api/v1/egress/diff`                      | Egress diff ingest — used by **shadow-soldier** and **egress-relay-rabbitmq** (optional `signature`, `shadow_test_name`) |
| `POST /api/v1/ingest/wire`                      | Envoy Lua wire-protocol HTTP egress ingest                              |
| `POST /api/v1/debug/seed-reports`               | Inject RawReport histories for bats (no live traffic)                   |
| `GET /api/v1/traces/{traceID}?protocol=`        | Slim trace detail — `reports` + `verdict` (optional `direction=` for HTTP) |


### Trace correlation

**Ingress (Envoy ext_proc):** trace id resolution order — `traceparent` → W3C `traceparent` → Envoy `x-request-id`. Shadow role from `x-shadow-role` (Envoy metadata or `SHADOW_ROLE` env).

**Egress (shadow-soldier):** trace id from the W3C `traceparent` the application embeds in its query (a SQL comment, or a BSON `$comment`). Shadow role from the sidecar's `SHADOW_ROLE`. The sidecar supplies its own `protocol:operation:target` signature. Each report is appended immediately; late reports trigger re-diff.

**Egress (egress-relay-rabbitmq):** trace id from AMQP message headers (`traceparent` or `traceparent`). Payload includes `exchange`, `routing_key`, and `body` (message JSON) for signatures like `rabbitmq:publish:egress-events:order.shipped`.

### Database egress

Database queries reach Beru from the **shadow-soldier** sidecar, which proxies
each shadow pod's plain-text database connections and decodes the wire protocol.
Reports arrive on `POST /api/v1/egress/diff` carrying an explicit
`protocol:operation:target` signature (e.g. `mongodb:insert:orders`,
`postgresql:select:users`).

MongoDB payloads are stored as the command document, so `mongoPayloadsEqual`
strips the per-connection fields (`_id`, `lsid`, `comment`, `$db`) before
comparing. See
[/data-plane/shadow-soldier.md](../../docs/data-plane/shadow-soldier.md).

## Project layout

```
cmd/beru/              Entrypoint — gRPC + HTTP servers, wiring
internal/
  engine/              TraceRouter worker pool, legacy log mirroring
  model/               RawReport, verdict types, repository contracts
  diff/                Signature-based timeline evaluation
  report/              RawReport builders (ingress, egress, signatures)
  envoyextproc/        Envoy ext_proc (ingress observe → TraceRouter)
  api/                 HTTP handlers (egress/wire ingest, seed, slim traces)
  storage/             Postgres + WAL (raw_reports, verdicts, noise_filters)
  server/              gRPC TrafficReporter
api/proto/beru/v1/     Protobuf definitions
pkg/api/beru/v1/       Generated protobuf Go code
```

---

## Development

```sh
make proto          # requires protoc + protoc-gen-go + protoc-gen-go-grpc
make build
make test
make docker-build BERU_IMG=beru:dev
```

Root Makefile aliases: `make beru-build`, `make beru-test`, and Monarch's `make beru-docker-build`.

---

## Trace propagation

| Path | How trace reaches Beru |
| ---- | ---------------------- |
| **HTTP ingress (Igris → Envoy)** | Igris injects W3C `traceparent` on multicast; Envoy ingress `ext_proc` reports responses. Apps usually need no trace code. |
| **RabbitMQ egress (relay)** | Workers publish with W3C context (OTel `amqplib` / `pika` injection); egress-relay-rabbitmq reads Firehose and posts to Beru HTTP API (dedupes duplicate Firehose events by trace+span+payload). |

RabbitMQ egress-relay deduplicates duplicate Firehose publishes (by trace+span+payload). Manual `traceparent` propagation is supported for libraries that cannot auto-inject — see `testing/example-apps/rmq-test-worker` with `RMQ_WORKER_MANUAL_TRACE=1`.

---

## Related reading

- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — layers, data flow, Envoy sidecar roles
- [pipeline/monarch/DEPLOYMENT.md](../monarch/DEPLOYMENT.md) — ShadowTest deployment; always-on Shop egress replay
- [docs/data-plane/beru-postgres-storage.md](../../docs/data-plane/beru-postgres-storage.md) — storage backends and durable diff history
- [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) — end-to-end verification steps
