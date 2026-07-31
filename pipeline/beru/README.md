# Beru

Beru is the **L5 — analysis sink** for Shadow-Diff. It correlates traffic from the three shadow roles (control-a, control-b, candidate), runs **diff-of-diffs** to separate noise from regressions, serves **egress mock responses** for strict downstream replay, and exposes a **web dashboard** for inspecting traces.

Monarch provisions Beru automatically: one **`beru-local`** pod per ShadowTest, inside that ShadowTest's shadow namespace. It runs on SQLite over an in-memory EmptyDir, so state is lost on pod restart and with the namespace. Setting `BERU_DB_SECRET` on the Monarch manager points every beru-local at a shared PostgreSQL instead, and diff history then outlives the ShadowTest — see [docs/data-plane/beru-postgres-storage.md](../../docs/data-plane/beru-postgres-storage.md). For how Beru fits in the full pipeline see [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md).

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

Every inbound report is appended to SQLite immediately. Each arrival triggers a **full timeline re-diff** for that trace. Evaluation is a strict 3-step pipeline: (1) wait for all three roles or mark `WAITING_FOR_ROLES` after `BERU_TRACE_TIMEOUT`, (2) void the trace on control baseline divergence (`VOIDED_BASELINE_DIVERGENCE`), (3) compound-diff the candidate against control-a (`MATCH` / `MISMATCH` with JSON detail flags). Results land in `raw_reports` and `verdicts`. User-configured **noise filters** suppress known flaky JSON paths before `MISMATCH_PAYLOAD` is recorded. See [docs/data-plane/beru-analysis.md](../../docs/data-plane/beru-analysis.md).

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

Open the dashboard at [http://localhost:8080/dashboard/](http://localhost:8080/dashboard/).

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
| **8080**  | HTTP     | Egress diff ingest, dashboard                                 |
| **8081**  | HTTP     | Egress ingest for in-pod sidecars (Service port → container 8080) |


In-pod sidecars post to `:8081` rather than `:8080`, because the shadow pod's iptables rules REDIRECT outbound 8080 into Envoy's egress listener.

To browse history after a ShadowTest is gone, run Beru anywhere against the same database:

```sh
kubectl run beru --image=beru:dev --restart=Never \
  --env=DB_DRIVER=postgres --env=DB_HOST=postgres.monarch-system.svc.cluster.local \
  --env=DB_USER=beru --env=DB_PASSWORD=beru --env=DB_NAME=beru --env=DB_SSLMODE=disable
kubectl port-forward pod/beru 8080:8080   # then open /dashboard/
```

---

## Configuration


| Variable                  | Default                                                                                         | Description                                            |
| ------------------------- | ----------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| `BERU_GRPC_ADDR`          | `:50051`                                                                                        | gRPC listen address (ext_proc, TrafficReporter)        |
| `BERU_HTTP_ADDR`          | `:8080`                                                                                         | HTTP listen address (egress diff ingest, dashboard)             |
| `BERU_DB_PATH`            | `/var/lib/beru/shadow_diff.db` (falls back to `./shadow_diff.db` if parent dir is not writable) | SQLite path (`raw_reports`, `verdicts`, `shadow_tests`, `noise_filters`) |
| `BERU_DB_RETENTION_DAYS`  | `7`                                                                                             | Purge `raw_reports` older than N days; orphan `verdicts` removed        |
| `BERU_SHADOW_TEST_NAME`   | `default`                                                                                       | Default shadow test name when ingest metadata omits `shadow_test_name`  |
| `BERU_TRACE_TIMEOUT`      | `10s`                                                                                           | Incomplete traces older than this become `WAITING_FOR_ROLES`            |


---

## Storage and lifecycle

Beru uses **two persistence layers** plus an in-memory mock store.

### Overview

| Layer | What it holds | Where | Survives restart? |
| ----- | ------------- | ----- | ----------------- |
| **State engine** | Every report + latest verdict per trace | SQLite `raw_reports`, `verdicts` | Yes |
| **Shadow test runs** | Run names for dashboard filter + noise filter scope | SQLite `shadow_tests`, `noise_filters` | Yes |

### State engine (`internal/v2/`)

All ingress and egress sources normalize to a `RawReport` and hit the **TraceRouter**:

```
Handler → TraceRouter (FNV-sharded worker)
       → AppendReport (SQLite raw_reports)
       → EvaluateTraceHistory (signature-based diff)
       → SaveDiffVerdict (SQLite verdicts, upsert per trace_id)
       → mirrorLegacyLogs (E2E log strings)
```

| Table | Write model | Contents |
| ----- | ----------- | -------- |
| `raw_reports` | Append-only | `trace_id`, `shadow_role`, `shadow_test_name`, `protocol`, `direction`, `signature`, `status_code`, payload bytes, `captured_at` |
| `verdicts` | Upsert on `trace_id` | `MATCH` / `MISMATCH` / `VOIDED_BASELINE_DIVERGENCE` / `WAITING_FOR_ROLES`, count-regression flag, JSON `summary_details` |

Late-arriving spans **re-open** the timeline and overwrite the verdict — including rows previously marked `WAITING_FOR_ROLES`. A background reaper finalizes incomplete traces past `BERU_TRACE_TIMEOUT` (default 10s).

`shadow_test_name` is set from ingest metadata (`shadow_test_name` on gRPC/HTTP, `x-shadow-test-name` on ext_proc) or falls back to `BERU_SHADOW_TEST_NAME`.

### Dashboard

The web UI reads **v2 tables only** — no duplicate legacy projection. Trace list shows one row per `(trace_id, protocol)` with signatures from stored `raw_reports`. Detail URLs: `/dashboard/traces/{traceID}?protocol=mongodb`.

Match/mismatch stats on the index page are **computed on load** from v2 data (not stored counters on `shadow_tests`).

### SQLite retention

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
| `POST /api/v1/debug/seed-reports`               | Inject RawReport histories for UI/bats (no live traffic)                            |
| `GET /dashboard/`                               | Web UI — trace list, diff detail, egress sequence, noise filter management |
| `GET /api/v1/traces?shadow_test_id=`            | Dashboard JSON — trace summaries (`trace_id`, `protocol`, `status`, `signatures`) |
| `GET /api/v1/traces/{traceID}?protocol=`        | Trace detail — `raw_reports`, `verdict`, `sequence_steps`               |
| `GET /api/v1/shadow-tests`                      | Shadow test run list (for dashboard run selector)                       |
| `POST /api/v1/noise/filters`                    | Save a noise filter path for a shadow test name                         |


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
  v2/
    engine/            TraceRouter worker pool, legacy log mirroring
    storage/           SQLite raw_reports + verdicts
    diff/              Signature-based timeline evaluation
    report/            RawReport builders (ingress, egress, signatures)
  envoyextproc/        Envoy ext_proc (ingress observe → TraceRouter)
  diff/                JSON diff-of-diffs (ingress noise paths; noise filter tests)
  api/                 HTTP handlers (egress diff, wire ingest, seed)
  dashboard/           Embedded web UI + REST API (reads v2 tables)
  storage/             SQLite shadow_tests + noise_filters + retention
  server/              gRPC TrafficReporter
api/proto/beru/v1/     Protobuf definitions
deploy/                Kubernetes Deployment + Service
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

