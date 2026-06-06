# Beru

Beru is the **L5 — analysis sink** for Shadow-Diff. It correlates traffic from the three shadow roles (control-a, control-b, candidate), runs **diff-of-diffs** to separate noise from regressions, serves **egress mock responses** for strict downstream replay, and exposes a **web dashboard** for inspecting traces.

Monarch does **not** deploy Beru. Install it separately and point each `ShadowTest` at Beru's gRPC address (`spec.beruGRPCAddress`). See [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) for how Beru fits in the full pipeline.

---

## Role in the pipeline


| Path                     | How Beru receives data                          | What Beru does                                                       |
| ------------------------ | ----------------------------------------------- | -------------------------------------------------------------------- |
| **Ingress (HTTP/DB)**    | Envoy sidecar **ingress `ext_proc`** (gRPC)     | Collects one response per role per trace → **diff-of-diffs**         |
| **Egress replay (HTTP)** | Envoy sidecar **egress `ext_proc`** on `:15001` | Hashes outbound request → returns mock from store or **599** on miss |
| **Egress record**        | **Recorder** or manual HTTP API                 | Seeds the in-memory mock store from prod capture                     |
| **Egress diff (AMQP)**   | **egress-relay-rabbitmq** HTTP API              | Compares outbound broker publishes across the three roles            |


### Diff-of-diffs (ingress and egress)

1. **Diff(control-a, control-b)** → fields that differ on identical builds (noise).
2. **Diff(control-a, candidate)** → total changes.
3. **Regressions** ≈ changes that are not explained by noise.

Results are persisted to SQLite. User-configured **noise filters** (per shadow test name) can suppress known flaky JSON paths.

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

### Deploy to Kubernetes

```sh
make docker-build BERU_IMG=beru:dev
# Kind: load the image into your cluster, then:
kubectl apply -f deploy/
```

This creates namespace `**beru-system**`, Deployment `**beru**`, and Service `**beru**`:


| Port      | Protocol | Purpose                                                       |
| --------- | -------- | ------------------------------------------------------------- |
| **50051** | gRPC     | `TrafficReporter`, Envoy `ext_proc`, Envoy ALS (Mongo egress) |
| **8080**  | HTTP     | Mock store APIs, egress diff ingest, dashboard                |


Point Monarch / ShadowTest at `beru.beru-system.svc.cluster.local:50051` (gRPC) and ensure Recorder / egress-relay use the HTTP service on `:8080`.

---

## Configuration


| Variable                  | Default                                                                                         | Description                                            |
| ------------------------- | ----------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| `BERU_GRPC_ADDR`          | `:50051`                                                                                        | gRPC listen address                                    |
| `BERU_HTTP_ADDR`          | `:8080`                                                                                         | HTTP listen address                                    |
| `BERU_DB_PATH`            | `/var/lib/beru/shadow_diff.db` (falls back to `./shadow_diff.db` if parent dir is not writable) | SQLite path for traces, mismatches, noise filters      |
| `BERU_DB_RETENTION_DAYS`  | `7`                                                                                             | Purge traces older than N days                         |
| `BERU_SHADOW_TEST_NAME`   | *(empty)*                                                                                       | Default shadow test name when metadata is missing      |
| `BERU_TRACE_TTL`          | `30s`                                                                                           | Pending trace correlation timeout                      |
| `BERU_MAX_PENDING_TRACES` | `5000`                                                                                          | Max in-flight pending traces                           |
| `BERU_EGRESS_WAIT`        | `5s`                                                                                            | Extra wait after partial egress reports before diffing |


The egress **mock store** is **in-memory** (not SQLite). Restarting Beru clears seeded mocks unless Recorder repopulates them.

---

## Storage and lifecycle

Beru uses **three layers** of storage. Raw reports are held in memory only long enough to correlate the three roles; diff **results** go to SQLite; egress **mocks** stay in memory until the process restarts.

### Overview


| Layer                   | What it holds                                                     | Where                                           | Survives restart?                |
| ----------------------- | ----------------------------------------------------------------- | ----------------------------------------------- | -------------------------------- |
| **Pending correlation** | In-flight reports waiting for control-a, control-b, and candidate | In-memory maps (`ingest`, `egressdiff`, `als`)  | No — dropped on exit or eviction |
| **Egress mock store**   | Recorded downstream HTTP responses for strict replay              | In-memory map (`replay.MockStore`)              | No                               |
| **Diff results**        | MATCH/MISMATCH status, regression paths, bodies for mismatches    | SQLite (`traces`, `mismatches`, `shadow_tests`) | Yes                              |
| **Noise filters**       | User-suppressed JSON paths per shadow test                        | SQLite (`noise_filters`)                        | Yes                              |


### Ingress reports (HTTP responses via `ext_proc` or gRPC)

1. Each report arrives with a **trace id** and **role** and is stored in the ingress **pending map** keyed by trace id.
2. When **all three roles** have reported for the same trace, Beru **removes that trace from the pending map immediately**, runs diff-of-diffs in a background goroutine, and writes the outcome to SQLite (`SaveDiffResult`).
3. Raw response bodies are **not** kept in the pending map after diff starts — only the computed result (and mismatch detail) is persisted.

**Removed from memory without a SQLite row when:**


| Condition                                                                       | Behavior                                                   |
| ------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| **TTL expired** (`BERU_TRACE_TTL`, default 30s) and not all three roles arrived | Logged as timeout; pending entry deleted (sweep every 10s) |
| **Pending map full** (`BERU_MAX_PENDING_TRACES`, default 5000)                  | Oldest trace evicted after trying expired entries first    |
| **Non-JSON payload**                                                            | Diff skipped; nothing written to SQLite                    |


### Egress diff reports (AMQP via `egress-relay-rabbitmq`)

Same pending-map pattern as ingress, with one extra rule:

- When **all three roles** report → diff immediately and remove from pending.
- When only **some** roles report, Beru starts a `**BERU_EGRESS_WAIT`** timer (default 5s). If at least **two** roles (including control-a) are present when the timer fires, it diffs with whatever is available and removes the trace from pending.
- Otherwise TTL / cap eviction applies the same as ingress.

Successful diffs are saved to SQLite with protocol set (e.g. `rabbitmq`). Failed or incomplete correlations are dropped from memory only.

### MongoDB egress (Envoy access logs)

Mongo query payloads are buffered in a separate pending map, **gated on ingress completion** for that trace id. Diff runs when ingress has finished and all three roles have the **same number** of queries. The pending entry is then removed and the result is written to SQLite. Same TTL and max-pending eviction as ingress.

### Egress mock store (`seed_mock` / `record_egress`)

`POST /v1/seed_mock` and `POST /v1/record_egress` write directly into the in-memory mock map keyed by **request hash**. There is **no TTL** and **no SQLite** — entries live until:

- Beru process restarts, or
- The same hash is overwritten by a later seed/record call.

Envoy egress `ext_proc` only **reads** this map (lookup by hash); it does not store responses there.

### SQLite retention

A background job runs **every hour** and deletes trace rows older than `**BERU_DB_RETENTION_DAYS`** (default 7). Orphaned mismatch rows are removed when their trace is pruned. Shadow test run counters are decremented accordingly. Noise filters are **not** auto-deleted by retention.

---

## API surfaces

Beru exposes two listen ports. Detailed request/response schemas will live in a dedicated API doc; summary below.

### gRPC (`:50051`)


| Service                                  | Purpose                                                                                                                                            |
| ---------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `**TrafficReporter.ReportTraffic`**      | Direct ingress reports (role, trace_id, payload) — used for tests and integrations                                                                 |
| `**ExternalProcessor` (Envoy ext_proc)** | **Ingress mode:** observe shadow app responses and enqueue for diff. **Egress mode** (`x-shadow-mode: egress`): hash outbound HTTP and return mock |
| `**AccessLogService` (Envoy ALS)**       | MongoDB egress access logs from Envoy sidecars                                                                                                     |


Protobuf: `[api/proto/beru/v1/traffic.proto](api/proto/beru/v1/traffic.proto)`. Regenerate with `make proto`.

### HTTP (`:8080`)


| Endpoint                                        | Purpose                                                                 |
| ----------------------------------------------- | ----------------------------------------------------------------------- |
| `GET /healthz`                                  | Liveness                                                                |
| `POST /v1/seed_mock`                            | Manually seed an egress mock response                                   |
| `POST /v1/record_egress`                        | Same storage as `seed_mock` — used by **Recorder** for prod auto-record |
| `POST /api/v1/egress/diff`                      | Egress diff ingest — used by **egress-relay-rabbitmq**                  |
| `GET /dashboard/`                               | Web UI — trace list, diff detail, noise filter management               |
| `GET /api/v1/traces`, `/api/v1/shadow-tests`, … | Dashboard JSON API                                                      |


### Trace correlation

Beru resolves trace id in order: `x-shadow-trace-id` → W3C `traceparent` → Envoy `x-request-id`. Shadow role comes from `x-shadow-role` (Envoy metadata or `SHADOW_ROLE` env on standalone Beru).

---

## Project layout

```
cmd/beru/              Entrypoint — gRPC + HTTP servers, wiring
internal/
  envoyextproc/        Envoy ext_proc (ingress observe + egress mock)
  ingest/              Ingress report correlation and diff trigger
  egressdiff/          Egress diff correlation (AMQP and other protocols)
  diff/                JSON diff-of-diffs engine
  replay/              In-memory egress mock store and request hashing
  api/                 HTTP handlers (seed_mock, record_egress, egress diff)
  dashboard/           Embedded web UI + REST API
  storage/             SQLite persistence (traces, mismatches, noise filters)
  als/                 Envoy access log ingest (Mongo egress)
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

| Ingress | How trace reaches Beru |
| ------- | ---------------------- |
| **HTTP (Igris → Envoy)** | Igris sets `x-shadow-trace-id` and W3C `traceparent` on replay; Envoy ingress `ext_proc` reports to Beru. Apps usually need no trace code. |
| **RabbitMQ (igris-rabbitmq → worker)** | Igris-rabbitmq copies trace headers onto shadow AMQP messages. Workers must propagate context on **outbound HTTP** (Envoy egress) and/or **AMQP publish** (egress-relay Firehose). |

For RabbitMQ egress diffing without app changes, use **OpenTelemetry auto-instrumentation** (`spec.otelInjection` on ShadowTest + OpenTelemetry Operator + `Instrumentation` CR). The OTel Node agent instruments `amqplib` to extract/inject `traceparent`. See `./testing/scripts/e2e-otel-rabbitmq-test.sh` and [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) Phase 5_OTel.

Manual propagation (copy `x-shadow-trace-id` / `traceparent` from consumed message to outbound publish) is still supported — see `testing/example-apps/rmq-test-worker` with `RMQ_WORKER_MANUAL_TRACE=1`.

---

## Related reading

- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — layers, data flow, Envoy sidecar roles
- [pipeline/monarch/DEPLOYMENT.md](../monarch/DEPLOYMENT.md) — ShadowTest `beruGRPCAddress`, egress downstreams
- [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) — end-to-end verification steps

