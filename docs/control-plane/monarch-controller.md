---
type: Architecture Specification
title: Monarch Controller — Envoy-Only Shadow Injection
description: Reconcile contract for telemetry-dependent shadow pods after Plan 1 realignment.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch
tags: [architecture, control-plane, monarch, envoy, kubernetes]
timestamp: 2026-07-01T00:00:00Z
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

## Egress capture (env-based)

All shadow app containers receive:

- `HTTP_PROXY` / `HTTPS_PROXY` → `http://127.0.0.1:10001` (Envoy egress listener)
- `NO_PROXY` → `127.0.0.1,localhost,beru-ingest.shadow-system.svc.cluster.local,.cluster.local,.svc`

The expanded `NO_PROXY` prevents Envoy loopback deadlock when forwarding to cluster-internal upstreams.

## Envoy configuration highlights

Per-role ConfigMap `{shadowtest}-{role}-envoy` renders:

1. **Ingress listener** — `ext_proc` → `beru_ext_proc` (gRPC, unchanged)
2. **Egress HTTP listener** (`127.0.0.1:10001`) — filter order: `lua` → `ext_proc` → `router`; Lua truncates bodies at 64KB (`max_bytes = 65536`; Envoy v1.26 HCM has no `max_request_bytes` field)
   - Lua extracts `traceparent`, method/path, and truncated (64KB) request/response bodies
   - Lua `httpCall` async POST to `beru_ingest` cluster at `/api/v1/ingest/wire` (`string.format` JSON — no `json.encode`)
   - Sidecar env: `SHADOW_ROLE`, `SHADOW_TEST_NAME`
   - `ext_proc` handles record/replay mocking when `spec.recordAndReplay` is set
   - Passthrough mode uses `dynamic_forward_proxy` when no record/replay hosts
3. **Mongo egress** (`127.0.0.1:27017`) — `mongo_proxy` with `emit_dynamic_metadata: true` + `tcp_proxy` to sandbox Mongo (Envoy→Beru mongo wire POST is Phase 2b)
4. **Clusters** — `beru_ext_proc`, `beru_ingest`, `dynamic_egress_cluster`, optional `mongo_upstream`

`beru_ingest` resolves via `beruIngestAddressFor(st, shadowNS)`: explicit `spec.beruIngestAddress`, shared `beru-ingest.shadow-system.svc.cluster.local:8080` when `spec.beruGRPCAddress` points at beru-system, or `beru-local.{shadow-ns}.svc.cluster.local:8080` for per-shadow Beru.

## Optional CRD fields

- `spec.beruGRPCAddress` — ext_proc gRPC target (default: local `beru-local` or `beru.beru-system`)
- `spec.beruIngestAddress` — wire-payload ingest target (default: same host resolution as HTTP above)

## Beru wire ingest (Plan 2)

Beru exposes `POST /api/v1/ingest/wire` on `:8080` (`BERU_HTTP_ADDR`). Envelopes decode to `NetworkEventEnvelope` → `FromWireEnvelope` → existing `TraceRouter.Route` → SQLite `raw_reports`. OTLP Mongo span export is deprecated; callers should use wire ingest.

## Out of scope

- Envoy mongo_listener → Beru HTTP POST (Phase 2b access log)
- iptables transparent egress capture
- Ingress migration from `ext_proc` to `beru_ingest`

# Citations

- [ARCHITECTURE_SHIFT.md](/refactor/ARCHITACTURE_SHIFT.md) — telemetry-dependent strategy
- [pipeline/monarch/internal/controller/shadowtest_envoy.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_envoy.go) — Envoy YAML generation
- [pipeline/monarch/internal/controller/shadowtest_resources.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_resources.go) — Deployment patch
