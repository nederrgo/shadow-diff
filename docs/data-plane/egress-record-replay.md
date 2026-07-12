---
type: Architecture Specification
title: Egress Record and Replay
description: How Shadow-Diff captures production HTTP egress via Pixie dual-branch export (client + server), seeds Shop with Put dedup, and replays via Envoy egress ext_proc. No ShadowTest recordAndReplay field.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/recorder
tags: [data-plane, recorder, shop, envoy, pixie, egress, replay]
timestamp: 2026-07-12T15:35:00Z
---

# Egress Record and Replay

Shadow workers cannot call real downstream services — doing so would produce side effects in production systems. Instead, the record-and-replay pipeline captures what the **production worker** actually received from each downstream service and replays those exact responses to shadow workers, scoped to the same trace.

**There is no `spec.recordAndReplay` field.** Monarch always deploys Shop + Recorder into each shadow namespace. Recorder unconditionally forwards every OTLP HTTP span to Shop.

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

L4b pipeline service. Always deployed by Monarch. Accepts OTLP gRPC on `:4317` from the Pixie egress PxL. For each span it extracts:

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

No host allowlist ConfigMap — every span is forwarded.

### Shop (`pipeline/shop/`)

Always-on in-memory mock store. Two interfaces:

| Endpoint | Caller | Purpose |
|---|---|---|
| `POST /v1/record_egress` | Recorder | Seed a mock from a live-captured response |
| `POST /v1/seed_mock` | Manual / test scripts | Seed a mock directly |
| `GET /healthz` | Kubernetes probe | Liveness check |

Envoy egress `shop_ext_proc` looks up mocks by trace-keyed host/path (gRPC `:50051`).

### Envoy egress ext_proc (`pipeline/shop/internal/envoyextproc/egress.go`)

Intercepts outbound HTTP from shadow workers at the Envoy egress listener (`127.0.0.1:10001`, iptables redirect). On every request:

1. Extract `:authority` (or `host`) header → `host`
2. Extract `:method`, `:path`, `traceparent` headers
3. Build mock key: `replay.TraceKey(traceID, method, HostWithoutPort(host), path)`
4. If key found → `immediateResponse(mock.StatusCode, mock.Headers, mock.Body)`
5. If key not found → `immediateResponse(599, ..., "Egress Regression")`

Status 599 is the "miss" signal. Shadow workers are expected to retry on 599 while the Recorder is still seeding.

---

## Mock Key Format

```
trace:<traceID>:<METHOD>:<host>:<path>
```

- `traceID` — 32 hex chars, from W3C `traceparent`
- `METHOD` — uppercased
- `host` — **no port**
- `path` — verbatim, leading `/` enforced

---

## Provisioning (Monarch)

Always, for every ShadowTest:

```
reconcileShop           → Shop Deployment + Service
shopDeploymentReady     → requeue until available
reconcileRecorderStack  → Recorder Deployment + Service (SHOP_HTTP_URL → Shop)
recorderDeploymentReady → requeue until available
```

`PixieStreamRule.spec.recorderOtelEndpoint` is always set so pixie-stream-bridge runs the egress PxL.

---

## Pixie Egress PxL

Bridge renders:

```
$PIXIE_BRIDGE_STATE_DIR/<ns>-pixie-<shadowtest>-egress.pxl
```

The PxL queries `http_events` in the **prod namespace** (`targetNamespace`) and exports OTLP to Recorder `:4317`.

**Dual-branch export** (PxL has no reliable OR — two `px.export`s):

| Branch | Filter | Covers |
|--------|--------|--------|
| Client / external | `trace_role == 1` + worker `app` pod contains | Outside HTTP |
| Server / in-cluster | `trace_role == 2` + `client_pod` from `remote_addr` contains worker `app` | In-cluster HTTP |

Worker identity comes from `PixieStreamRule.targetLabels` (copied from the target Deployment). When both sides seed the same Shop key, `MockStore.Put` keeps the **first 2xx**.

`wait_recorder_seed` (bats) polls Recorder logs for `shop client: recorded POST …` and nudges `px run` on the egress PxL.

---

## Data Flow (Sequence)

```
1.  Prod worker receives RMQ message (traceparent header)
2.  Prod worker calls downstream with Host = logical replay hostname
3.  Pixie captures http_events (client-side and/or server-side by destination)
4.  Dual-branch egress PxL exports OTLP → Recorder :4317
5.  Recorder → Shop POST /v1/record_egress (Shop Put dedups by mock key)
6.  Shadow worker (same trace) hits Envoy :10001 → shop_ext_proc mock lookup
```

---

## Failure Modes

| Condition | Observed behaviour |
|---|---|
| Shop not seeded yet (Pixie lag) | ext_proc returns 599; shadow worker retries |
| Pixie vizier not healthy | seed timeout / skip |
| Host port mismatch (seed vs lookup) | 599; keys don't match |
| `failure_mode_allow: true` on ingress ext_proc | Ingress passes even if beru-local is down |

---

# Citations

- Recorder OTLP: [`pipeline/recorder/internal/receiver/otel_receiver.go`](../../pipeline/recorder/internal/receiver/otel_receiver.go)
- Shop HTTP API: [`pipeline/shop/internal/api/http.go`](../../pipeline/shop/internal/api/http.go)
- Envoy egress ext_proc: [`pipeline/shop/internal/envoyextproc/egress.go`](../../pipeline/shop/internal/envoyextproc/egress.go)
- Mock keys: [`pipeline/shop/internal/replay/keys.go`](../../pipeline/shop/internal/replay/keys.go)
- Shop Put dedup: [`pipeline/shop/internal/replay/mockstore.go`](../../pipeline/shop/internal/replay/mockstore.go)
- Egress PxL template: [`testing/bats/manifests/pixie-bridge/configmap.yaml`](../../testing/bats/manifests/pixie-bridge/configmap.yaml)
- Monarch Recorder: [`pipeline/monarch/internal/controller/shadowtest_recorder.go`](../../pipeline/monarch/internal/controller/shadowtest_recorder.go)
- Bats seed helper: [`testing/bats/lib/traffic.bash`](../../testing/bats/lib/traffic.bash) — `wait_recorder_seed`
