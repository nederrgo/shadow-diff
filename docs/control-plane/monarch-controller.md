---
type: Architecture Specification
title: Monarch Controller — Envoy-Only Shadow Injection
description: Reconcile contract for record/replay ShadowTests; S3 env; replay trigger; S3 prefix finalizer.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch
tags: [architecture, control-plane, monarch, envoy, shop, beru, record-replay]
timestamp: 2026-07-28T14:15:00Z
---

# Monarch Controller — Envoy-Only Shadow Injection

Plan 1 of the [telemetry-dependent pivot](/refactor/ARCHITACTURE_SHIFT.md) removes all language-specific OpenTelemetry Operator injection from Monarch. Shadow pods are orchestrated purely through infrastructure: an unmodified app container plus a protocol-aware Envoy sidecar.

## Operating modes

Every ShadowTest is either `record` or `replay` (`spec.mode`; default `record`). `spec.storage` is required. There is no live-traffic reconcile path.

| Mode | Provisions | Garbage collects |
|------|------------|------------------|
| `record` | KaiselRule → Igris → Shop (+ beru-local); mints `status.currentSessionID` when unset | ABC Deployments/Services; clears `status.replayState` |
| `replay` | Shop → Igris → ABC (+ deps); requires `spec.sessionID` or existing `status.currentSessionID` | KaiselRule |

Monarch copies `storage.credentialsSecretRef` from the CR namespace into the shadow namespace (same name), then injects `OPERATING_MODE`, `S3_*`, `TEST_*`, `SESSION_ID`, and optional AWS `secretKeyRef` env into Igris and Shop. Igris exposes admin `:9090` (`IGRIS_ADMIN_ADDR`, Service port `9090`).

### Replay trigger

After Shop, Igris, and ABC Deployments are roll-ready (`ReadyReplicas > 0` and `UpdatedReplicas == Replicas`), if `status.replayState` is empty Monarch `POST`s `http://<igris>.<shadow-ns>.svc:9090/v1/replay/start` (10s timeout). HTTP 202 or 409 sets `status.replayState=started`.

### S3 retention finalizer

Finalizer `shadow-diff.io/s3-cleanup` (alongside `shadowtest.finalizers.shadow-diff.io`) runs after the shadow namespace is gone:

- `retentionPolicy: Delete` — delete objects under prefix `shadow-diff/<CR-namespace>/<CR-name>/` only (never the BYOB bucket)
- `Retain` or unset — skip prefix cleanup

See [async record/replay ADR](/refactor/async-record-replay.md).

## Reconcile contract

For each shadow role (`control-a`, `control-b`, `candidate`) in **replay** mode, Monarch `CreateOrPatch`es:

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

Shop Deployment env includes `BERU_HTTP_URL` (same host resolution as egress-relay / beru-local), `SHADOW_TEST_NAME`, plus S3/mode env from `spec.storage`. HTTP egress reporting is Shop fire-and-forget — not Envoy Lua / `beru_ingest`.

## Optional CRD fields

- `spec.beruGRPCAddress` — ext_proc gRPC target (default: local `beru-local` or `beru.beru-system`)
- `spec.beruIngestAddress` — wire-payload ingest target (default: same host resolution as HTTP above)
- `spec.samplePercentage` — shared prod sampling gate (1-100, default 100) for all input types. Rule (package `github.com/shadow-diff/sample`): decode the 32-hex W3C trace id to 16 bytes, `V = FNV-1a-64(bytes) & 0xFF`, keep iff `(V*100)<(N*256)`; empty/missing `traceparent` always dropped. Monarch seeds by `inputs[].driver`: HTTP → KaiselRule (ingress and egress); `rabbitmq_message` → igris-rabbitmq (`IGRIS_RMQ_SAMPLE_PERCENTAGE`). RabbitMQ does not use Kaisel.
- `spec.maxQPSPerPod` — requests/sec Igris forwards per shadow pod replica (default 50). See Spike Guard below.
- `spec.mode` — `record` \| `replay` (default `record`)
- `spec.sessionID` — pin S3 session folder (required resolvable on replay)
- `spec.storage` — **required** BYOB S3 config (`type`, `bucketName`, `endpoint`, `region`, `credentialsSecretRef`, `retentionPolicy`)

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
- `status.replayState=completed` (no Igris completion API yet)

# Citations

- [ARCHITECTURE_SHIFT.md](/refactor/ARCHITACTURE_SHIFT.md) — telemetry-dependent strategy
- [async-record-replay.md](/refactor/async-record-replay.md) — S3-backed async Record & Replay ADR
- [pipeline/monarch/internal/controller/shadowtest_envoy.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_envoy.go) — Envoy YAML generation
- [pipeline/monarch/internal/controller/shadowtest_resources.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_resources.go) — Deployment patch
- [pipeline/monarch/internal/controller/shadowtest_replay_trigger.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_replay_trigger.go) — automated replay start
- [pipeline/monarch/internal/controller/shadowtest_s3_cleanup.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_s3_cleanup.go) — S3 prefix retention finalizer
- [pipeline/pkg/s3utils/deleter.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/pkg/s3utils/deleter.go) — `DeletePrefix` / `TestKeyPrefix`
