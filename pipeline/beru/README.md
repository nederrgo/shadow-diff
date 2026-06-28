# Beru

Beru is the **L5 — analysis sink** for Shadow-Diff. It correlates traffic from the three shadow roles (control-a, control-b, candidate), runs **diff-of-diffs** to separate noise from regressions, serves **egress mock responses** for strict downstream replay, and exposes a **web dashboard** for inspecting traces.

Monarch does **not** deploy Beru. Install it separately and point each `ShadowTest` at Beru's gRPC address (`spec.beruGRPCAddress`). See [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) for how Beru fits in the full pipeline.

---

## Role in the pipeline


| Path                     | How Beru receives data                          | What Beru does                                                       |
| ------------------------ | ----------------------------------------------- | -------------------------------------------------------------------- |
| **Ingress (HTTP)**       | Envoy sidecar **ingress `ext_proc`** (gRPC)     | Collects one response per role per trace → **diff-of-diffs**         |
| **Egress diff (MongoDB)** | OTel agent → **OTLP** (`:4317` gRPC or `:8080/v1/traces` HTTP) | Appends spans per trace → **sequence diff** with N+1 detection (re-diff on each arrival) |
| **Egress replay (HTTP)** | Envoy sidecar **egress `ext_proc`** on `:15001` | Hashes outbound request → returns mock from store or **599** on miss |
| **Egress record**        | **Recorder** or manual HTTP API                 | Seeds the in-memory mock store from prod capture                     |
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

Every inbound report is appended to SQLite immediately. Each arrival triggers a **full timeline re-diff** for that trace (no in-memory correlation buffers, no egress wait timer). Results land in `raw_reports` (event log) and `verdicts` (latest status per trace). The dashboard reads those v2 tables directly. User-configured **noise filters** (per shadow test name) can suppress known flaky JSON paths.

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
# gRPC :50051, OTLP gRPC :4317, HTTP :8080 (defaults)
```

Open the dashboard at [http://localhost:8080/dashboard/](http://localhost:8080/dashboard/).

### Deploy to Kubernetes

```sh
make docker-build BERU_IMG=beru:dev
# Kind: load the image into your cluster, then:
kubectl apply -f deploy/
```

This creates namespace `**beru-system**`, Deployment `**beru**`, and Service `**beru**`:


| Port      | Protocol | Purpose                                                       |
| --------- | -------- | ------------------------------------------------------------- |
| **50051** | gRPC     | `TrafficReporter`, Envoy `ext_proc`                           |
| **4317**  | gRPC     | OTLP trace receiver (MongoDB egress from OTel agents)         |
| **8080**  | HTTP     | OTLP/HTTP (`POST /v1/traces`), mock store APIs, egress diff ingest, dashboard |


Point Monarch / ShadowTest at `beru.beru-system.svc.cluster.local:50051` (gRPC). OTel agents export to `:4317` (gRPC) or `:8080/v1/traces` (HTTP/protobuf). Recorder and egress-relay use the HTTP service on `:8080`.

---

## Configuration


| Variable                  | Default                                                                                         | Description                                            |
| ------------------------- | ----------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| `BERU_GRPC_ADDR`          | `:50051`                                                                                        | gRPC listen address (ext_proc, TrafficReporter)        |
| `BERU_OTLP_GRPC_ADDR`     | `:4317`                                                                                         | OTLP gRPC listen address                               |
| `BERU_HTTP_ADDR`          | `:8080`                                                                                         | HTTP listen address (OTLP/HTTP, mock APIs, dashboard)  |
| `BERU_DB_PATH`            | `/var/lib/beru/shadow_diff.db` (falls back to `./shadow_diff.db` if parent dir is not writable) | SQLite path (`raw_reports`, `verdicts`, `shadow_tests`, `noise_filters`) |
| `BERU_DB_RETENTION_DAYS`  | `7`                                                                                             | Purge `raw_reports` older than N days; orphan `verdicts` removed        |
| `BERU_SHADOW_TEST_NAME`   | `default`                                                                                       | Default shadow test name when ingest metadata omits `shadow_test_name`  |


The egress **mock store** is **in-memory** (not SQLite). Restarting Beru clears seeded mocks unless Recorder repopulates them.

---

## Storage and lifecycle

Beru uses **two persistence layers** plus an in-memory mock store.

### Overview

| Layer | What it holds | Where | Survives restart? |
| ----- | ------------- | ----- | ----------------- |
| **State engine** | Every report + latest verdict per trace | SQLite `raw_reports`, `verdicts` | Yes |
| **Shadow test runs** | Run names for dashboard filter + noise filter scope | SQLite `shadow_tests`, `noise_filters` | Yes |
| **Egress mock store** | Recorded downstream HTTP responses for strict replay | In-memory map (`replay.MockStore`) | No |

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
| `raw_reports` | Append-only | `trace_id`, `shadow_role`, `shadow_test_name`, `protocol`, `direction`, `signature`, payload bytes, `captured_at` |
| `verdicts` | Upsert on `trace_id` | `MATCH`/`MISMATCH`, count-regression flag, summary details |

Late-arriving spans or relay messages **re-open** the timeline: Beru re-reads all reports for the trace and overwrites the verdict. No TTL eviction of in-flight traces — if a role never reports, that trace simply never appears on the dashboard (rows require all three roles for a protocol).

`shadow_test_name` is set from ingest metadata (`shadow_test_name` on gRPC/HTTP, `x-shadow-test-name` on ext_proc) or falls back to `BERU_SHADOW_TEST_NAME`.

### Dashboard

The web UI reads **v2 tables only** — no duplicate legacy projection. Trace list shows one row per `(trace_id, protocol)` with signatures from stored `raw_reports`. Detail URLs: `/dashboard/traces/{traceID}?protocol=mongodb`.

Match/mismatch stats on the index page are **computed on load** from v2 data (not stored counters on `shadow_tests`).

### Egress mock store (`seed_mock` / `record_egress`)

`POST /v1/seed_mock` and `POST /v1/record_egress` write directly into the in-memory mock map keyed by **request hash**. There is **no TTL** and **no SQLite** — entries live until Beru restarts or the hash is overwritten.

Envoy egress `ext_proc` only **reads** this map (lookup by hash); it does not store responses there.

### SQLite retention

A background job runs **every hour** and deletes `raw_reports` rows older than `BERU_DB_RETENTION_DAYS` (default 7), then removes `verdicts` whose `trace_id` no longer appears in `raw_reports`. Noise filters are **not** auto-deleted.

---

## API surfaces

Beru exposes gRPC (`:50051`, `:4317`) and HTTP (`:8080`). Detailed request/response schemas will live in a dedicated API doc; summary below.

### gRPC (`:50051`)


| Service                                  | Purpose                                                                                                                                            |
| ---------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `**TrafficReporter.ReportTraffic`**      | Direct ingress reports (role, trace_id, payload) — used for tests and integrations                                                                 |
| `**ExternalProcessor` (Envoy ext_proc)** | **Ingress mode:** observe shadow app responses → TraceRouter. **Egress mode** (`x-shadow-mode: egress`): hash outbound HTTP and return mock |


### OTLP gRPC (`:4317`)


| Service                         | Purpose                                                          |
| ------------------------------- | ---------------------------------------------------------------- |
| `**TraceService.Export`**       | OTLP span batches from OTel agents — MongoDB egress spans → TraceRouter |


Protobuf: `[api/proto/beru/v1/traffic.proto](api/proto/beru/v1/traffic.proto)` (Beru gRPC). OTLP uses standard `opentelemetry.proto.collector.trace.v1`. Regenerate Beru protos with `make proto`.

### HTTP (`:8080`)


| Endpoint                                        | Purpose                                                                 |
| ----------------------------------------------- | ----------------------------------------------------------------------- |
| `GET /healthz`                                  | Liveness                                                                |
| `POST /v1/traces`                               | OTLP/HTTP protobuf trace export (Python OTel default) → Mongo egress |
| `POST /v1/seed_mock`                            | Manually seed an egress mock response                                   |
| `POST /v1/record_egress`                        | Same storage as `seed_mock` — used by **Recorder** for prod auto-record |
| `POST /api/v1/egress/diff`                      | Egress diff ingest — used by **egress-relay-rabbitmq** (optional `shadow_test_name`) |
| `GET /dashboard/`                               | Web UI — trace list, diff detail, egress sequence, noise filter management |
| `GET /api/v1/traces?shadow_test_id=`            | Dashboard JSON — trace summaries (`trace_id`, `protocol`, `status`, `signatures`) |
| `GET /api/v1/traces/{traceID}?protocol=`        | Trace detail — `raw_reports`, `verdict`, `sequence_steps`               |
| `GET /api/v1/shadow-tests`                      | Shadow test run list (for dashboard run selector)                       |
| `POST /api/v1/noise/filters`                    | Save a noise filter path for a shadow test name                         |


### Trace correlation

**Ingress (Envoy ext_proc):** trace id resolution order — `x-shadow-trace-id` → W3C `traceparent` → Envoy `x-request-id`. Shadow role from `x-shadow-role` (Envoy metadata or `SHADOW_ROLE` env).

**Egress (OTLP):** trace id from the span's W3C trace id bytes. Shadow role from `shadow_role` resource attribute, or parsed from `service.name` suffix (`<shadowtest>-control-a`, etc.). Each span is appended immediately; late spans trigger re-diff.

**Egress (egress-relay-rabbitmq):** trace id from AMQP message headers (`traceparent` or `x-shadow-trace-id`). Payload is the message body JSON; routing metadata is not yet included (signatures may show `rabbitmq:unknown:…` until relay enrichment lands).

### MongoDB egress (OTLP)

When shadow pods have a Mongo dependency and `spec.otelInjection` is enabled, the OTel agent exports MongoDB client spans to Beru OTLP. Beru:

1. Parses `db.statement` / `db.query.text` from each span into canonical JSON (via `internal/otlp/mongo_parser`).
2. Derives a **signature** `mongodb:{operation}:{collection}` from wire-command JSON and/or span metadata.
3. Appends a `RawReport` per span; re-diff runs on every arrival — **N+1 count regression** when the candidate issues extra inserts/updates.

Example: control-a and control-b each perform one `insert` into `orders`; the candidate performs that insert plus an extra `insert` → Beru logs `Egress count regression … expected 1 query but got 2`.

#### Mongo egress signatures & OTel by language

Signatures pair the same logical operation across the three roles even when drivers emit different span shapes.

| Source | Priority | Example signature |
|--------|----------|-------------------|
| Wire JSON command key | 1st | `{"insert":"orders",…}` → `mongodb:insert:orders` |
| Span metadata | 2nd | `db.operation.name=insert`, `db.collection.name=orders` → `mongodb:insert:orders` |
| Wrapped non-JSON text | 3rd | `{"query":"insert orders"}` → `mongodb:insert:orders` |
| Hash fallback | last | `mongodb:unknown:<8 hex>` |

Beru reads legacy and stable semconv attributes:

| Purpose | Legacy | Stable |
|---------|--------|--------|
| DB system | `db.system` | `db.system.name` |
| Query text | `db.statement` | `db.query.text` |
| Operation | `db.operation` | `db.operation.name` |
| Collection | `db.mongodb.collection` | `db.collection.name` |

Spans without query text are ingested when **operation + collection** attrs are present (common on Java/.NET instrumentations). **Python pymongo** is different: it sets `db.mongodb.collection` and a command-only `db.statement` (e.g. `insert`) but not `db.operation` — Beru derives the operation from that statement text.

| Language | OTLP to Beru | Mongo-specific Monarch env | E2E in repo |
|----------|--------------|----------------------------|-------------|
| **Python** | HTTP `:8080/v1/traces` | `OTEL_PYTHON_MONGODB_CAPTURE_STATEMENT=true` (required for `db.statement`) | Yes |
| **Node.js** | gRPC `:4317` | `OTEL_NODE_ENABLED_INSTRUMENTATIONS=mongodb,http` | Yes |
| **Java / .NET / Go** | gRPC `:4317` | none extra | No mongo E2E |

**Known limitations:** `getMore` continuations share the parent `find` signature; `bulkWrite` may hash as `unknown` without query text; non-JSON wire text without span attrs stays opaque.

See `./testing/scripts/e2e-python-hybrid-test.sh` and `./testing/scripts/e2e-mongo-egress-test.sh`.

---

## Project layout

```
cmd/beru/              Entrypoint — gRPC + OTLP + HTTP servers, wiring
internal/
  v2/
    engine/            TraceRouter worker pool, legacy log mirroring
    storage/           SQLite raw_reports + verdicts
    diff/              Signature-based timeline evaluation
    report/            RawReport builders (ingress, egress, signatures)
  envoyextproc/        Envoy ext_proc (ingress observe + egress mock)
  otlp/                OTLP trace receiver + MongoDB db.statement parser
  diff/                JSON diff-of-diffs (ingress noise paths; noise filter tests)
  replay/              In-memory egress mock store and request hashing
  api/                 HTTP handlers (OTLP, seed_mock, record_egress, egress diff)
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
| **Mongo egress (OTel → Beru)** | OTel agent auto-instruments MongoDB drivers, extracts inbound context from AMQP/HTTP headers, exports `db.statement` spans to Beru OTLP. No app-level header copying. |
| **RabbitMQ egress (relay)** | Workers publish with W3C context (OTel `amqplib` / `pika` injection); egress-relay-rabbitmq reads Firehose and posts to Beru HTTP API (dedupes duplicate Firehose events by trace+span+payload). |

Enable OTel injection via `spec.otelInjection` on ShadowTest + OpenTelemetry Operator + `Instrumentation` CR. Monarch sets `OTEL_EXPORTER_OTLP_ENDPOINT` to Beru when a Mongo dependency is declared. See `./testing/scripts/e2e-python-hybrid-test.sh`, `./testing/scripts/e2e-otel-rabbitmq-test.sh`, and [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) Phase 5_OTel.

Manual propagation (`x-shadow-trace-id` / `traceparent` copying) remains supported for libraries the agent cannot instrument — see `testing/example-apps/rmq-test-worker` with `RMQ_WORKER_MANUAL_TRACE=1`. Python `pika` is auto-instrumented when OTel injection is enabled; egress-relay deduplicates duplicate Firehose publishes.

---

## Related reading

- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — layers, data flow, Envoy sidecar roles
- [pipeline/monarch/DEPLOYMENT.md](../monarch/DEPLOYMENT.md) — ShadowTest `beruGRPCAddress`, egress recordAndReplay
- [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) — end-to-end verification steps

