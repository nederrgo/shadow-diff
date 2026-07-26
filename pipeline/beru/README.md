# Beru

Beru is the **L5 — analysis sink** for Shadow-Diff. It correlates traffic from the three shadow roles (control-a, control-b, candidate), runs **diff-of-diffs** to separate noise from regressions, serves **egress mock responses** for strict downstream replay, and exposes a **web dashboard** for inspecting traces.

Monarch provisions Beru automatically. When `spec.beruGRPCAddress` is unset, Monarch deploys a per-ShadowTest **`beru-local`** pod inside the shadow namespace (SQLite on an in-memory EmptyDir — state is lost on pod restart). To use a persistent, shared Beru instance instead, set `spec.beruGRPCAddress` on the `ShadowTest` CR and point it at a separately deployed Beru (e.g. `beru.beru-system.svc.cluster.local:50051`). See [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) for how Beru fits in the full pipeline.

---

## Role in the pipeline


| Path                     | How Beru receives data                          | What Beru does                                                       |
| ------------------------ | ----------------------------------------------- | -------------------------------------------------------------------- |
| **Ingress (HTTP)**       | Envoy sidecar **ingress `ext_proc`** (gRPC)     | Collects one response per role per trace → **diff-of-diffs**         |
| **Egress diff (MongoDB)** | **Dormant** — OTLP `:4317` receiver retained, no capture path produces spans | Sequence diff with N+1 detection remains implemented and unit-tested |
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
| **8080**  | HTTP     | OTLP/HTTP (`POST /v1/traces`), egress diff ingest, dashboard |


Point Monarch / ShadowTest at `beru.beru-system.svc.cluster.local:50051` (gRPC). OTel agents export to `:4317` (gRPC) or `:8080/v1/traces` (HTTP/protobuf). egress-relay-rabbitmq posts egress diffs to `:8080/api/v1/egress/diff`.

---

## Configuration


| Variable                  | Default                                                                                         | Description                                            |
| ------------------------- | ----------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| `BERU_GRPC_ADDR`          | `:50051`                                                                                        | gRPC listen address (ext_proc, TrafficReporter)        |
| `BERU_OTLP_GRPC_ADDR`     | `:4317`                                                                                         | OTLP gRPC listen address                               |
| `BERU_HTTP_ADDR`          | `:8080`                                                                                         | HTTP listen address (OTLP/HTTP, egress diff ingest, dashboard)  |
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

Beru exposes gRPC (`:50051`, `:4317`) and HTTP (`:8080`). Detailed request/response schemas will live in a dedicated API doc; summary below.

### gRPC (`:50051`)


| Service                                  | Purpose                                                                                                                                            |
| ---------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `**TrafficReporter.ReportTraffic`**      | Direct ingress reports (role, trace_id, payload) — used for tests and integrations                                                                 |
| `**ExternalProcessor` (Envoy ext_proc)** | Observes shadow app responses → TraceRouter (ingress diff only) |


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
| `POST /api/v1/egress/diff`                      | Egress diff ingest — used by **egress-relay-rabbitmq** (optional `shadow_test_name`) |
| `POST /api/v1/debug/seed-reports`               | Inject RawReport histories for UI/bats (no live traffic)                            |
| `GET /dashboard/`                               | Web UI — trace list, diff detail, egress sequence, noise filter management |
| `GET /api/v1/traces?shadow_test_id=`            | Dashboard JSON — trace summaries (`trace_id`, `protocol`, `status`, `signatures`) |
| `GET /api/v1/traces/{traceID}?protocol=`        | Trace detail — `raw_reports`, `verdict`, `sequence_steps`               |
| `GET /api/v1/shadow-tests`                      | Shadow test run list (for dashboard run selector)                       |
| `POST /api/v1/noise/filters`                    | Save a noise filter path for a shadow test name                         |


### Trace correlation

**Ingress (Envoy ext_proc):** trace id resolution order — `traceparent` → W3C `traceparent` → Envoy `x-request-id`. Shadow role from `x-shadow-role` (Envoy metadata or `SHADOW_ROLE` env).

**Egress (OTLP):** trace id from the span's W3C trace id bytes. Shadow role from `shadow_role` resource attribute, or parsed from `service.name` suffix (`<shadowtest>-control-a`, etc.). Each span is appended immediately; late spans trigger re-diff.

**Egress (egress-relay-rabbitmq):** trace id from AMQP message headers (`traceparent` or `traceparent`). Payload includes `exchange`, `routing_key`, and `body` (message JSON) for signatures like `rabbitmq:publish:egress-events:order.shipped`.

### MongoDB egress (dormant)

The OTLP receiver on `:4317`, the MongoDB wire-payload parser and the sequence
diff are all present and unit-tested, but **no capture path currently produces
MongoDB spans** — that half was removed with Pixie. See
[/data-plane/pixie-removal.md](../../docs/data-plane/pixie-removal.md).

The parser reads raw MongoDB wire bytes from a `db.raw_payload` OTLP attribute
and derives the signature `mongodb:{operation}:{collection}` from the wire
command doc (e.g. `{"insert":"orders",…}` → `mongodb:insert:orders`). The trace
id comes from a `traceparent` the application injects into the MongoDB
`$comment` field. Any future capture path that emits that shape can point at the
same unchanged port.

## Project layout

```
cmd/beru/              Entrypoint — gRPC + OTLP + HTTP servers, wiring
internal/
  v2/
    engine/            TraceRouter worker pool, legacy log mirroring
    storage/           SQLite raw_reports + verdicts
    diff/              Signature-based timeline evaluation
    report/            RawReport builders (ingress, egress, signatures)
  envoyextproc/        Envoy ext_proc (ingress observe → TraceRouter)
  otlp/                OTLP trace receiver + MongoDB wire payload parser (dormant)
  diff/                JSON diff-of-diffs (ingress noise paths; noise filter tests)
  api/                 HTTP handlers (OTLP, egress diff)
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
- [pipeline/monarch/DEPLOYMENT.md](../monarch/DEPLOYMENT.md) — ShadowTest `beruGRPCAddress`; always-on Shop egress replay
- [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) — end-to-end verification steps

