# Beru

Beru is the **L5 — analysis sink** for Shadow-Diff. It correlates traffic from the three shadow roles (control-a, control-b, candidate), runs **diff-of-diffs** to separate noise from regressions, serves **egress mock responses** for strict downstream replay, and exposes a **web dashboard** for inspecting traces.

Monarch does **not** deploy Beru. Install it separately and point each `ShadowTest` at Beru's gRPC address (`spec.beruGRPCAddress`). See [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) for how Beru fits in the full pipeline.

---

## Role in the pipeline


| Path                     | How Beru receives data                          | What Beru does                                                       |
| ------------------------ | ----------------------------------------------- | -------------------------------------------------------------------- |
| **Ingress (HTTP)**       | Envoy sidecar **ingress `ext_proc`** (gRPC)     | Collects one response per role per trace → **diff-of-diffs**         |
| **Egress diff (MongoDB)** | OTel agent → **OTLP** (`:4317` gRPC or `:8080/v1/traces` HTTP) | Batches spans per trace → **sequence diff** with N+1 detection |
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

Results are persisted to SQLite (including per-operation `egress_payloads` for the dashboard sequence view). User-configured **noise filters** (per shadow test name) can suppress known flaky JSON paths.

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
| `BERU_DB_PATH`            | `/var/lib/beru/shadow_diff.db` (falls back to `./shadow_diff.db` if parent dir is not writable) | SQLite path for traces, mismatches, noise filters      |
| `BERU_DB_RETENTION_DAYS`  | `7`                                                                                             | Purge traces older than N days                         |
| `BERU_SHADOW_TEST_NAME`   | *(empty)*                                                                                       | Default shadow test name when metadata is missing      |
| `BERU_TRACE_TTL`          | `30s`                                                                                           | Pending trace correlation timeout                      |
| `BERU_MAX_PENDING_TRACES` | `5000`                                                                                          | Max in-flight pending traces                           |
| `BERU_EGRESS_WAIT`        | `5s`                                                                                            | Batch egress spans/reports per trace before diffing (OTLP and relay) |


The egress **mock store** is **in-memory** (not SQLite). Restarting Beru clears seeded mocks unless Recorder repopulates them.

---

## Storage and lifecycle

Beru uses **three layers** of storage. Raw reports are held in memory only long enough to correlate the three roles; diff **results** go to SQLite; egress **mocks** stay in memory until the process restarts.

### Overview


| Layer                   | What it holds                                                     | Where                                           | Survives restart?                |
| ----------------------- | ----------------------------------------------------------------- | ----------------------------------------------- | -------------------------------- |
| **Pending correlation** | In-flight reports waiting for control-a, control-b, and candidate | In-memory maps (`ingest`, `egressdiff`)         | No — dropped on exit or eviction |
| **Egress mock store**   | Recorded downstream HTTP responses for strict replay              | In-memory map (`replay.MockStore`)              | No                               |
| **Diff results**        | MATCH/MISMATCH status, regression paths, egress sequence payloads | SQLite (`traces`, `mismatches`, `egress_payloads`, `shadow_tests`) | Yes                              |
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


### Egress diff reports (AMQP, MongoDB OTLP, and other protocols)

All egress diff sources feed the same **`egressdiff` store** — one ordered payload slice per role per protocol per trace.

1. Each report is **appended** to that role's slice (multiple Mongo queries or AMQP publishes per trace are expected).
2. On the first report for a trace, Beru starts a **`BERU_EGRESS_WAIT`** timer (default 5s) to batch late-arriving OTLP spans or relay messages before diffing.
3. When **all three roles** have at least one report for a protocol → diff after the wait timer fires (or immediately if `BERU_EGRESS_WAIT` is 0).
4. If only **some** roles report before the timer, Beru diffs when at least **two** roles (including control-a) are present.
5. Otherwise TTL / cap eviction applies the same as ingress.

The diff engine runs **signature-based sequence comparison** (see [Diff-of-diffs](#diff-of-diffs-ingress-and-egress) above). Successful diffs are saved to SQLite with protocol set (e.g. `mongodb`, `rabbitmq`) and per-operation rows in `egress_payloads`. The dashboard renders these as an **egress sequence** with extra/missing step badges.

### MongoDB egress (OTLP)

When shadow pods have a Mongo dependency and `spec.otelInjection` is enabled, the OTel agent exports MongoDB client spans to Beru OTLP. Beru:

1. Parses `db.statement` from each span into a JSON payload (via `internal/otlp/mongo_parser`).
2. Routes spans into the egress diff store keyed by trace id and shadow role (`OTEL_SERVICE_NAME` suffix or `shadow_role` resource attribute).
3. Waits for `BERU_EGRESS_WAIT`, then runs the sequence diff — including **N+1 count regression** when the candidate issues extra inserts/updates.

Example: control-a and control-b each perform one `insert` into `orders`; the candidate performs that insert plus an extra `insert` into `orders` with an audit field → Beru logs `Egress count regression … expected 1 query but got 2`.

See `./testing/scripts/e2e-python-hybrid-test.sh` and `./testing/scripts/e2e-mongo-egress-test.sh`.

### Egress mock store (`seed_mock` / `record_egress`)

`POST /v1/seed_mock` and `POST /v1/record_egress` write directly into the in-memory mock map keyed by **request hash**. There is **no TTL** and **no SQLite** — entries live until:

- Beru process restarts, or
- The same hash is overwritten by a later seed/record call.

Envoy egress `ext_proc` only **reads** this map (lookup by hash); it does not store responses there.

### SQLite retention

A background job runs **every hour** and deletes trace rows older than `**BERU_DB_RETENTION_DAYS`** (default 7). Orphaned mismatch rows are removed when their trace is pruned. Shadow test run counters are decremented accordingly. Noise filters are **not** auto-deleted by retention.

---

## API surfaces

Beru exposes gRPC (`:50051`, `:4317`) and HTTP (`:8080`). Detailed request/response schemas will live in a dedicated API doc; summary below.

### gRPC (`:50051`)


| Service                                  | Purpose                                                                                                                                            |
| ---------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `**TrafficReporter.ReportTraffic`**      | Direct ingress reports (role, trace_id, payload) — used for tests and integrations                                                                 |
| `**ExternalProcessor` (Envoy ext_proc)** | **Ingress mode:** observe shadow app responses and enqueue for diff. **Egress mode** (`x-shadow-mode: egress`): hash outbound HTTP and return mock |


### OTLP gRPC (`:4317`)


| Service                         | Purpose                                                          |
| ------------------------------- | ---------------------------------------------------------------- |
| `**TraceService.Export`**       | OTLP span batches from OTel agents — MongoDB egress spans routed to egress diff store |


Protobuf: `[api/proto/beru/v1/traffic.proto](api/proto/beru/v1/traffic.proto)` (Beru gRPC). OTLP uses standard `opentelemetry.proto.collector.trace.v1`. Regenerate Beru protos with `make proto`.

### HTTP (`:8080`)


| Endpoint                                        | Purpose                                                                 |
| ----------------------------------------------- | ----------------------------------------------------------------------- |
| `GET /healthz`                                  | Liveness                                                                |
| `POST /v1/traces`                               | OTLP/HTTP protobuf trace export (Python OTel default) → Mongo egress diff |
| `POST /v1/seed_mock`                            | Manually seed an egress mock response                                   |
| `POST /v1/record_egress`                        | Same storage as `seed_mock` — used by **Recorder** for prod auto-record |
| `POST /api/v1/egress/diff`                      | Egress diff ingest — used by **egress-relay-rabbitmq**                  |
| `GET /dashboard/`                               | Web UI — trace list, diff detail, egress sequence, noise filter management |
| `GET /api/v1/traces`, `/api/v1/shadow-tests`, … | Dashboard JSON API (includes `sequence_steps` for egress traces)        |


### Trace correlation

**Ingress (Envoy ext_proc):** trace id resolution order — `x-shadow-trace-id` → W3C `traceparent` → Envoy `x-request-id`. Shadow role from `x-shadow-role` (Envoy metadata or `SHADOW_ROLE` env).

**Egress (OTLP):** trace id from the span's W3C trace id bytes. Shadow role from `shadow_role` resource attribute, or parsed from `service.name` suffix (`<shadowtest>-control-a`, etc.).

**Egress (egress-relay-rabbitmq):** trace id from AMQP message headers (`traceparent` or `x-shadow-trace-id`).

---

## Project layout

```
cmd/beru/              Entrypoint — gRPC + OTLP + HTTP servers, wiring
internal/
  envoyextproc/        Envoy ext_proc (ingress observe + egress mock)
  ingest/              Ingress report correlation and diff trigger
  egressdiff/          Egress sequence correlation, BERU_EGRESS_WAIT batching
  otlp/                OTLP trace receiver + MongoDB db.statement parser
  diff/                JSON diff-of-diffs engine + egress signature pairing
  replay/              In-memory egress mock store and request hashing
  api/                 HTTP handlers (OTLP, seed_mock, record_egress, egress diff)
  dashboard/           Embedded web UI + REST API (egress sequence view)
  storage/             SQLite persistence (traces, mismatches, egress_payloads, noise filters)
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
| **RabbitMQ egress (relay)** | Workers publish with W3C context (OTel `amqplib` injection or manual `traceparent` on `pika`); egress-relay-rabbitmq reads Firehose and posts to Beru HTTP API. |

Enable OTel injection via `spec.otelInjection` on ShadowTest + OpenTelemetry Operator + `Instrumentation` CR. Monarch sets `OTEL_EXPORTER_OTLP_ENDPOINT` to Beru when a Mongo dependency is declared. See `./testing/scripts/e2e-python-hybrid-test.sh`, `./testing/scripts/e2e-otel-rabbitmq-test.sh`, and [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) Phase 5_OTel.

Manual propagation (`x-shadow-trace-id` / `traceparent` copying) remains supported for libraries the agent cannot instrument — see `testing/example-apps/rmq-test-worker` with `RMQ_WORKER_MANUAL_TRACE=1`.

---

## Related reading

- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — layers, data flow, Envoy sidecar roles
- [pipeline/monarch/DEPLOYMENT.md](../monarch/DEPLOYMENT.md) — ShadowTest `beruGRPCAddress`, egress recordAndReplay
- [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) — end-to-end verification steps

