---
type: Architecture Specification
title: Egress Record and Replay
description: How Shadow-Diff captures production HTTP egress with Kaisel eBPF request/response pairing, seeds Shop with Put dedup, and replays via Envoy egress ext_proc.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/kaisel
tags: [data-plane, kaisel, shop, envoy, egress, replay]
timestamp: 2026-07-26T10:30:00Z
---

# Egress Record and Replay

Shadow workers cannot call real downstream services — doing so would produce side effects in production systems. Instead, the record-and-replay pipeline captures what the **production worker** actually received from each downstream service and replays those exact responses to shadow workers, scoped to the same trace.

Monarch deploys Shop into each shadow namespace. Kaisel captures the production workload's outbound calls off the wire and seeds Shop with what each dependency actually returned.

## Why this exists

The diff-of-diffs model requires all three roles (control-a, control-b, candidate) to observe identical inputs. HTTP egress responses are an input: if shadow workers hit real services, non-determinism in downstream state (latency, rate-limiting, DB reads) introduces noise that masks real regressions. Record-and-replay eliminates this source of noise.

---

## Component Map

```
[Prod Worker]
    │  HTTP call to a dependency
    ▼
[Kaisel eBPF]  ──pairs request + response off the wire──▶
    │
    └── POST /v1/record_egress ──▶ [Shop]
                                      │
[Shadow Worker]                       │
    │  HTTP call (same host/path, traceparent header set)
    ▼                                 │
[Envoy egress ext_proc] ──GET mock───▶│
    │  immediateResponse(statusCode, headers, body)
    ▼
[Shadow Worker receives mocked response]
```

---

## Components

### Kaisel (`pipeline/kaisel/`)

L1 eBPF capture. Reads both directions of a target pod's TCP connections off the
wire and pairs each outbound request with its response, then POSTs the pair to
`KaiselRule.spec.egressBaseURL` — the Shop in that ShadowTest's shadow namespace.

Kaisel sends `host`, `method` and `path` **verbatim** off the wire and lets Shop
derive the key, because Shop applies the identical transform on the ext_proc
lookup path. `path` carries the query string, matching Envoy's `:path`.

Response headers pass an allowlist before being stored: framing headers
(`content-length`, `transfer-encoding`) are dropped because Envoy synthesizes its
own on the immediate-response path, and `date`/`server` because they differ on
every capture. See [/data-plane/kaisel-ebpf.md](/data-plane/kaisel-ebpf.md).

### Shop (`pipeline/shop/`)

Always-on in-memory mock store. Two interfaces:

| Endpoint | Caller | Purpose |
|---|---|---|
| `POST /v1/record_egress` | Kaisel | Seed a mock from a live-captured response |
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

Status 599 is the "miss" signal. Shadow workers are expected to retry on 599 while Kaisel is still seeding.

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
```

`KaiselRule.spec.egressBaseURL` is set to the shadow namespace's Shop, which is where Kaisel POSTs each captured pair.

---

## Data Flow (Sequence)

```
1.  Prod worker receives a message / request (traceparent set)
2.  Prod worker calls its dependency
3.  Kaisel captures both directions and pairs request with response
4.  Kaisel POSTs /v1/record_egress → Shop derives the key and stores the mock
5.  Shadow worker (same trace) hits Envoy :10001 → shop_ext_proc mock lookup
```

---

## Failure Modes

| Condition | Observed behaviour |
|---|---|
| Shop not seeded yet (capture lag) | ext_proc returns 599; shadow worker retries |
| Host port mismatch (seed vs lookup) | 599; keys don't match |
| `failure_mode_allow: true` on ingress ext_proc | Ingress passes even if beru-local is down |

---

# Citations

- Shop HTTP API: [`pipeline/shop/internal/api/http.go`](../../pipeline/shop/internal/api/http.go)
- Envoy egress ext_proc: [`pipeline/shop/internal/envoyextproc/egress.go`](../../pipeline/shop/internal/envoyextproc/egress.go)
- Mock keys: [`pipeline/shop/internal/replay/keys.go`](../../pipeline/shop/internal/replay/keys.go)
- Shop Put dedup: [`pipeline/shop/internal/replay/mockstore.go`](../../pipeline/shop/internal/replay/mockstore.go)
- Bats seed helper: [`testing/bats/lib/kaisel.bash`](../../testing/bats/lib/kaisel.bash) — `wait_kaisel_egress_seed`
- Kaisel egress export: [`pipeline/kaisel/internal/export/exporter.go`](../../pipeline/kaisel/internal/export/exporter.go) — `HandleTransaction`
- Kaisel request/response pairing: [`pipeline/kaisel/internal/decode/bidi.go`](../../pipeline/kaisel/internal/decode/bidi.go)
- Egress test workload: [`testing/example-apps/egress-test-app`](../../testing/example-apps/egress-test-app)
