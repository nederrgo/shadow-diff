---
type: Architecture Specification
title: Monarch Controller — Envoy-Only Shadow Injection
description: Reconcile contract for telemetry-dependent shadow pods after Plan 1 realignment; Shop HTTP egress report to Beru.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch
tags: [architecture, control-plane, monarch, envoy, shop, beru]
timestamp: 2026-07-26T13:20:00Z
---

# Monarch Controller — Envoy-Only Shadow Injection

Plan 1 of the [telemetry-dependent pivot](/refactor/ARCHITACTURE_SHIFT.md) removes all language-specific OpenTelemetry Operator injection from Monarch. Shadow pods are orchestrated purely through infrastructure: an unmodified app container plus a protocol-aware Envoy sidecar.

## Reconcile contract

For each shadow role (`control-a`, `control-b`, `candidate`), Monarch `CreateOrPatch`es:

| Pod component | Behavior |
|---------------|----------|
| **app** | Production image; literal env copied from target Deployment; dependency env overrides (Mongo → `mongodb://127.0.0.1:27017`) |
| **envoy-sidecar** | `envoyproxy/envoy:v1.26-latest`; ConfigMap-mounted `envoy.yaml`; **no** `HTTP_PROXY` / `HTTPS_PROXY` env |
| **Volumes** | `envoy-config` ConfigMap only |

**Removed:** OTel Operator annotations, `Instrumentation` CR reconciliation, Node.js entrypoint wrapper initContainers, `spec.language`, `spec.otelInjection`.

## Application prerequisite

Applications must propagate W3C `traceparent` on outbound HTTP and database commands (e.g. Mongo `$comment`). Monarch no longer injects runtime agents.

## Egress capture (iptables)

Shadow app pods run an init container that redirects outbound TCP on ports **80** and **8080** to the Envoy egress listener (`127.0.0.1:10001`). Apps keep prod egress URLs; Monarch does **not** inject `HTTP_PROXY`. Set `Host` / `:authority` to the record/replay hostname (copied from prod env) so Shop can match mocks.

## Envoy configuration highlights

Per-role ConfigMap `{shadowtest}-{role}-envoy` renders:

1. **Ingress listener** — `ext_proc` → `beru_ext_proc` (gRPC)
2. **Egress HTTP listener** (`127.0.0.1:10001`) — filter order: `ext_proc` (`shop_ext_proc`) → `router`
   - `request_body_mode: BUFFERED` so Shop receives the full outbound request body
   - `initial_metadata`: `x-shadow-mode=egress`, `x-shadow-role=<role>`
   - Shop returns `ImmediateResponse` (mock or 599), then async `POST /api/v1/egress/diff` to Beru for HTTP egress diff-of-diffs
   - Apps keep prod egress URLs; iptables redirects :80/:8080 to Envoy `:10001`
3. **Clusters** — `beru_ext_proc`, `shop_ext_proc`, `local_app`

Shop Deployment env includes `BERU_HTTP_URL` (same host resolution as egress-relay / beru-local) and `SHADOW_TEST_NAME`. HTTP egress reporting is Shop fire-and-forget — not Envoy Lua / `beru_ingest`.

## Optional CRD fields

- `spec.beruGRPCAddress` — ext_proc gRPC target (default: local `beru-local` or `beru.beru-system`)
- `spec.beruIngestAddress` — wire-payload ingest target (default: same host resolution as HTTP above)
- `spec.samplePercentage` — shared prod sampling gate (1-100, default 100) for all input types. Rule (package `github.com/shadow-diff/sample`): decode the 32-hex W3C trace id to 16 bytes, `V = FNV-1a-64(bytes) & 0xFF`, keep iff `(V*100)<(N*256)`; empty/missing `traceparent` always dropped. Monarch seeds by `inputs[].driver`: HTTP → KaiselRule (ingress and egress); `rabbitmq_message` → igris-rabbitmq (`IGRIS_RMQ_SAMPLE_PERCENTAGE`). RabbitMQ does not use Kaisel.
- `spec.maxQPSPerPod` — requests/sec Igris forwards per shadow pod replica (default 50). See Spike Guard below.

## Spike Guard (ingress load shedding)

Shadow pods run at fixed low replica counts with no autoscaling, so Monarch and the Igris hubs protect them from production traffic spikes:

| Mechanism | Where | Behavior |
|---|---|---|
| Concurrency cap | igris-http | `IGRIS_MAX_CONCURRENCY = shadowRoleReplicas × spec.maxQPSPerPod` (default 50), computed by Monarch and passed as an env var. Requests over the cap get `429` before trace resolution or multicast — never forwarded to shadow pods. |
| Per-message TTL | igris-rabbitmq | Every mirrored AMQP message published to the 3 shadow brokers carries `Expiration: "10000"` (10s) — stale backlog expires instead of being processed by a stuck shadow consumer. |
| Prod shadow queue bound | Monarch (`shadowtest_rabbitmq.go`) | `x-max-length: 500`, `x-overflow: drop-head` on the prod-side shadow queue declare — oldest messages are dropped once the queue backs up. |

`shadowRoleReplicas` (currently `1`, one shared constant for control-a/b/candidate) is the single source of truth for both the shadow Deployment replica count and this capacity calc, so they can't drift.

## Beru wire ingest (Plan 2)

Beru exposes `POST /api/v1/ingest/wire` on `:8080` (`BERU_HTTP_ADDR`). Envelopes decode to `NetworkEventEnvelope` → `FromWireEnvelope` → existing `TraceRouter.Route` → SQLite `raw_reports`. OTLP Mongo span export is deprecated; callers should use wire ingest.

## Out of scope

- Envoy mongo_listener → Beru HTTP POST (Phase 2b access log)
- Ingress migration from `ext_proc` to `beru_ingest`

# Citations

- [ARCHITECTURE_SHIFT.md](/refactor/ARCHITACTURE_SHIFT.md) — telemetry-dependent strategy
- [pipeline/monarch/internal/controller/shadowtest_envoy.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_envoy.go) — Envoy YAML generation
- [pipeline/monarch/internal/controller/shadowtest_resources.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_resources.go) — Deployment patch
