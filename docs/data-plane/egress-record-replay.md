---
type: Architecture Specification
title: Egress Record and Replay
description: How Shadow-Diff captures production HTTP egress via Pixie, seeds the Shop mock store, and replays responses to shadow workers through Envoy's egress ext_proc.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/recorder
tags: [data-plane, recorder, shop, envoy, pixie, egress, replay]
timestamp: 2026-07-10T00:00:00Z
---

# Egress Record and Replay

Shadow workers cannot call real downstream services — doing so would produce side effects in production systems. Instead, the record-and-replay pipeline captures what the **production worker** actually received from each downstream service and replays those exact responses to shadow workers, scoped to the same trace.

## Why this exists

The diff-of-diffs model requires all three roles (control-a, control-b, candidate) to observe identical inputs. HTTP egress responses are an input: if shadow workers hit real services, non-determinism in downstream state (latency, rate-limiting, DB reads) introduces noise that masks real regressions. Record-and-replay eliminates this source of noise.

---

## Component Map

```
[Prod Worker]
    │  HTTP call to downstream
    ▼
[Pixie eBPF]  ──egress PxL──▶  [Recorder]  ──POST /v1/record_egress──▶  [Shop]
                                                                              │
[Shadow Worker]                                                               │
    │  HTTP call (same host/path, traceparent header set)                     │
    ▼                                                                         │
[Envoy egress ext_proc] ──GET mock──────────────────────────────────────────▶│
    │  immediateResponse(statusCode, headers, body)                           │
    ▼                                                                         │
[Shadow Worker receives mocked response]
```

---

## Components

### Recorder (`pipeline/recorder/`)

L4b pipeline service. Accepts OTLP gRPC on `:4317` from the Pixie egress PxL. For each span it extracts:

| Span attribute | Field |
|---|---|
| `http.host` / `server.address` | host (port stripped) |
| `url.path` / `http.target` | path |
| `http.request.method` / `http.method` | method |
| `http.response.status_code` / `http.status_code` | response status |
| `http.response.body` | response body |
| `traceparent` | trace ID (W3C format preferred over Pixie span ID) |

The Recorder normalises the host with `NormalizeHTTPHost` (lowercase, port stripped) before forwarding to Shop. It then calls Shop's `POST /v1/record_egress` asynchronously and logs:

```
shop client: recorded POST <host><path> -> <status>
```

**Always-on capture**: The Recorder's `recordAndReplay.json` config is always written as `[]` (empty array) by Monarch, which `HostMatches` treats as capture-all — no host allowlist is needed.

### Shop (`pipeline/shop/`)

In-memory mock store. Two interfaces:

| Endpoint | Caller | Purpose |
|---|---|---|
| `POST /v1/record_egress` | Recorder | Seed a mock from a live-captured response |
| `POST /v1/seed_mock` | Manual / test scripts | Seed a mock directly |
| `GET /healthz` | Kubernetes probe | Liveness check |

Envoy egress ext_proc holds a direct reference to the Shop mock map (`replay.MockStore`) — lookups are in-process, not over HTTP.

### Envoy egress ext_proc (`pipeline/shop/internal/envoyextproc/egress.go`)

Intercepts outbound HTTP from shadow workers at the Envoy egress listener. On every request:

1. Extract `:authority` (or `host`) header → `host`
2. Extract `:method`, `:path`, `traceparent` headers
3. Build mock key: `replay.TraceKey(traceID, method, HostWithoutPort(host), path)`
4. If key found → `immediateResponse(mock.StatusCode, mock.Headers, mock.Body)`
5. If key not found → `immediateResponse(599, ..., "Egress Regression")`

Status 599 is the "miss" signal. Shadow workers are expected to retry on 599 while the Recorder is still seeding (the prod worker processes first, Pixie exports a few seconds later, then the seed arrives at Shop).

---

## Mock Key Format

```
trace:<traceID>:<METHOD>:<host>:<path>
```

- `traceID` — 32 hex chars, from W3C `traceparent` (`00-<traceID>-<spanID>-<flags>`)
- `METHOD` — uppercased (`POST`, `GET`, …)
- `host` — **no port** (both seed and lookup apply `HostWithoutPort`)
- `path` — verbatim, leading `/` enforced

Example:
```
trace:375fd9eab5e0b4903e5b0915ea06c12b:POST:user-service-python.default.internal:/v1/log
```

The "no port" rule is enforced at both ends:
- **Seed path**: `NormalizeHTTPHost` in the Recorder strips port before calling Shop; Shop's `putMockFromRequest` additionally applies `replay.HostWithoutPort` as a belt-and-suspenders measure.
- **Lookup path**: ext_proc applies `replay.HostWithoutPort` to the `:authority` header (which may include the port from the shadow worker's `Host` header).

---

## Provisioning (Monarch)

Monarch provisions one Shop + one Recorder per shadow namespace, always, regardless of what fields appear in the ShadowTest spec. The reconcile sequence after shadow workloads are ready:

```
reconcileShop        → Shop Deployment + Service
shopDeploymentReady  → requeue until available
reconcileRecorderStack → ConfigMap (recordAndReplay.json = "[]") + Deployment + Service
recorderDeploymentReady → requeue until available
```

The PixieStreamRule always has `spec.recorderOtelEndpoint` set, which signals the pixie-stream-bridge to render and run the egress PxL for this ShadowTest.

---

## Pixie Egress PxL

The bridge renders an egress PxL file at:
```
$PIXIE_BRIDGE_STATE_DIR/<ns>-pixie-<shadowtest>-egress.pxl
```

The PxL queries `http_events` from the **prod namespace** and exports OTLP to the Recorder's `:4317` endpoint. The `traceparent` header is embedded in each span so the Recorder can extract the correct trace ID.

`wait_recorder_seed` (in bats) polls the Recorder logs for the seed confirmation line and triggers `run_pixie_export_once` on each iteration to drain any buffered Pixie data.

---

## Data Flow (Sequence)

```
1.  Prod worker receives RMQ message (traceparent header: 00-<TRACE_ID>-...-01)
2.  Prod worker calls downstream: POST http://user-service:8080/v1/log
    ↳ Host header: user-service-python.default.internal:8080
3.  Pixie eBPF captures the HTTP event, PxL stamps traceparent attribute
4.  PxL exports OTLP span to Recorder :4317
5.  Recorder: NormalizeHTTPHost → "user-service-python.default.internal"
6.  Recorder: POST Shop /v1/record_egress {trace_id, method, host (no port), path, response}
7.  Shop stores: key = "trace:<TRACE_ID>:POST:user-service-python.default.internal:/v1/log"

--- igris-rabbitmq fans out message to shadow workers ---

8.  Shadow worker receives message (same traceparent)
9.  Shadow worker calls: POST http://egress-proxy/v1/log
    ↳ Host header: user-service-python.default.internal:8080
10. Envoy ext_proc: HostWithoutPort(":authority") → "user-service-python.default.internal"
11. Envoy ext_proc: key lookup → hit → immediateResponse(200, ..., <recorded body>)
12. Shadow worker sees 200, proceeds normally
```

---

## Failure Modes

| Condition | Observed behaviour |
|---|---|
| Shop not seeded yet (Pixie lag) | ext_proc returns 599; shadow worker retries |
| Pixie vizier not healthy | `wait_recorder_seed` times out; bats test skips |
| Recorder not started | 599 on every shadow request; no seed ever arrives |
| Host port mismatch (seed vs lookup) | 599 on every shadow request; keys don't match |
| `failure_mode_allow: true` on ingress ext_proc | Ingress requests pass even if beru-local is down |

---

# Citations

- Recorder OTLP receiver: [`pipeline/recorder/internal/receiver/otel_receiver.go`](../../pipeline/recorder/internal/receiver/otel_receiver.go)
- Shop HTTP API (seed + record): [`pipeline/shop/internal/api/http.go`](../../pipeline/shop/internal/api/http.go)
- Envoy egress ext_proc: [`pipeline/shop/internal/envoyextproc/egress.go`](../../pipeline/shop/internal/envoyextproc/egress.go)
- Mock key format: [`pipeline/shop/internal/replay/keys.go`](../../pipeline/shop/internal/replay/keys.go)
- Host normalisation (Recorder): [`pipeline/recorder/internal/parse/parser.go`](../../pipeline/recorder/internal/parse/parser.go)
- Monarch provisioning: [`pipeline/monarch/internal/controller/shadowtest_recorder.go`](../../pipeline/monarch/internal/controller/shadowtest_recorder.go)
- Bats seed helper: [`testing/bats/lib/traffic.bash`](../../testing/bats/lib/traffic.bash) — `wait_recorder_seed`
</content>
</invoke>