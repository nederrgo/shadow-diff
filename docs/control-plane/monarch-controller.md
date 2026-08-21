---
type: Architecture Specification
title: Monarch Controller — Envoy-Only Shadow Injection
description: Reconcile contract for record/replay ShadowTests; status/topology surface; S3 env; replay trigger; S3 prefix finalizer.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch
tags: [architecture, control-plane, monarch, envoy, shop, beru, record-replay, status, topology]
timestamp: 2026-08-18T22:15:00Z
---

# Monarch Controller — Envoy-Only Shadow Injection

Plan 1 of the [telemetry-dependent pivot](/refactor/ARCHITACTURE_SHIFT.md) removes all language-specific OpenTelemetry Operator injection from Monarch. Shadow pods are orchestrated purely through infrastructure: an unmodified app container plus a protocol-aware Envoy sidecar.

## Operating modes

Every ShadowTest is either `record` or `replay` (`spec.mode`; default `record`). `spec.storage` is required. There is no live-traffic reconcile path.

| Mode | Provisions | Garbage collects |
|------|------------|------------------|
| `record` | Bottom-up: unbound AMQP queue (if any) + Shop + Igris → Ready gate → KaiselRule → AMQP bind; (+ beru-local); mints `status.currentSessionID` when unset (and remints on replay→record when unpinned) | ABC Deployments/Services; clears `status.replayState` + `currentReplayExecutionID` |
| `replay` | Shop → Igris → ABC (+ deps); requires `spec.sessionID` or existing `status.currentSessionID`; mints `status.currentReplayExecutionID` and injects `REPLAY_EXECUTION_ID` on beru-local | KaiselRule |

Record mode opens eBPF (`KaiselRule`) only after Shop/Igris are Available, and `QueueBind`s the prod AMQP shadow queue to the named production exchange (which must already exist) only after KaiselRule exists — see [platform bootstrap lifecycle](/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md).

Monarch copies `storage.credentialsSecretRef` from the CR namespace into the shadow namespace (same name), then injects `OPERATING_MODE`, `S3_*`, `TEST_*`, `SESSION_ID`, and optional AWS `secretKeyRef` env into Igris, **igris-rabbitmq**, and Shop. Both HTTP Igris and igris-rabbitmq expose admin `:9090` (`IGRIS_ADMIN_ADDR`, Service port `9090`).

### Replay trigger

After beru-local (so `REPLAY_EXECUTION_ID` is live), Shop, the ingress hub (HTTP `*-igris` or AMQP `*-igris-rabbitmq`), and ABC Deployments are roll-ready (`ReadyReplicas >= desired` and `UpdatedReplicas == Replicas`), if `status.replayState` is empty Monarch `POST`s `http://<hub>.<shadow-ns>.svc:9090/v1/replay/start` (10s timeout, body-less). HTTP 202 or 409 sets `status.replayState=started`. See [/control-plane/replay-execution-isolation.md](/control-plane/replay-execution-isolation.md).

ABC role pods expose two readiness probes so kube Ready means the data plane can accept traffic: a TCP probe on Envoy for `servicePortFor` (default 8888), and when shadow-soldier is injected, an HTTP GET `/healthz` on soldier port `19191` (`0.0.0.0`; 200 after all DB proxy listeners bind). DB proxy routes stay on `127.0.0.1`. igris-http replay delivery also retries dial failures (1s/3s/5s) so a brief post-Ready race does not drop a role.

### S3 retention finalizer

Finalizer `shadow-diff.io/s3-cleanup` (alongside `shadowtest.finalizers.shadow-diff.io`) runs after the shadow namespace is gone:

- `retentionPolicy: Delete` — delete objects under prefix `shadow-diff/<CR-namespace>/<CR-name>/` only (never the BYOB bucket)
- `Retain` or unset — skip prefix cleanup

See [async record/replay ADR](/refactor/async-record-replay.md).

## Shadow namespace naming and ownership

Each ShadowTest gets an isolated namespace `shadow-<crNamespace>-<crName>` (DNS-sanitized: lowercase, invalid chars → `-`). The name is **never truncated**. If the projected name exceeds Kubernetes’ 63-character DNS label limit, reconcile sticky-fails at `ValidatingInputs` (`phase=Failed`) and asks the user to shorten the ShadowTest name.

On create, Monarch labels the namespace with `shadow-diff.io/shadowtest-uid=<ShadowTest.metadata.uid>` (plus managed-by / name / CR-namespace labels). `ensureShadowNamespace` refuses to adopt an existing namespace unless that UID matches; a mismatch sticky-fails with a rename message and does **not** delete the foreign namespace. A namespace with `DeletionTimestamp` set returns a requeue error until deletion finishes.

Immediately after the namespace exists, Monarch reconciles `RoleBinding/monarch-shadow-workload` in that namespace, binding the manager ServiceAccount to ClusterRole `shadow-workload-role`. Kubernetes privilege-escalation prevention requires the creator to already hold the granted verbs **or** have `bind` on that ClusterRole; `manager-role` therefore includes `bind` on `resourceNames: [monarch-shadow-workload-role]` only. Workload mutations (`ConfigMap`/`Secret`/`Service`/`Deployment`) are authorized only through that binding, so the manager's cluster-wide RBAC stays read-only on prod namespaces.

Secret **reads** are not cluster-wide. The manager cache disables Secrets; `Get` hits the API server. `BERU_DB_SECRET` is authorized by a namespaced Role (`resourceNames` on that Secret). `storage.credentialsSecretRef` and AMQP `inputs[].amqp.credentialsSecretRef` require an install-time RoleBinding of ClusterRole `secret-source-reader` (`get` only) in the ShadowTest CR namespace — Helm `monarch.secretSourceNamespaces` (default `default`), or the e2e manifest `testing/bats/manifests/monarch-secret-source-rbac.yaml`. A CR in an unbound namespace fails at secret sync. S3 Secrets are copied into the shadow namespace; AMQP broker Secrets are not.

`ValidatingAdmissionPolicy/namespace-guard` (deployed with Monarch) denies the manager ServiceAccount from creating or deleting any `Namespace` whose name does not start with `shadow-`, and from creating/updating/deleting `RoleBinding` objects outside `shadow-*` namespaces. Native Kubernetes RBAC cannot express name-prefix rules on cluster-scoped `Namespace` objects; admission policy closes that gap. The controller does not create Secret-source RoleBindings; those stay install-time so a stolen SA cannot bind `secret-source-reader` into `kube-system`.

## AMQP ingress

`rabbitmq_message` inputs take a host-only `prodUrl` (`amqp(s)://host[:port][/vhost]`, no userinfo) plus `credentialsSecretRef` naming a Secret in the CR namespace with `username` and `password`. Monarch resolves the dial DSN at reconcile time for prod queue declare / bind / delete and for igris-rabbitmq `PROD_URL` (plain env `Value`). Userinfo in `prodUrl` fails validation.

## Kaisel capture targets

In record mode, Monarch provisions a `KaiselRule` whose `spec.targetIPs` are the Running pod IPs of `spec.targetDeployment`. Pods are resolved **only** via ownerReferences: Deployment → ReplicaSet → Pod. Label or selector matching is not used, so shared labels (e.g. two Deployments both using `app=api`) cannot widen capture to unrelated workloads. The pod watch path uses the same ownership chain to requeue the ShadowTest when target pod IPs change.

## Reconcile contract

### `spec.oldImage` pinning

When `spec.oldImage` is omitted, Monarch copies the target Deployment's primary container image on **first reconcile** (record or replay) and **persists it on the CR**. Later reconciles read `spec.oldImage` from etcd only; Monarch does not re-sync from the target. Users may override the baseline later via `kubectl patch spec.oldImage`; the next reconcile rolls control-a/b in replay mode.

Control-a and control-b always use `spec.oldImage`; candidate uses `spec.newImage`. Replay refuses to create ABC when `spec.oldImage` is still empty after pinning.

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

- `spec.beruGRPCTimeout` — ext_proc gRPC timeout (default `10s`); the target is always `beru-local` in the shadow namespace
- `spec.beru.image` — overrides the beru-local container image
- `spec.samplePercentage` — shared prod sampling gate (1-100, default 100) for all input types. Rule (package `github.com/shadow-diff/sample`): decode the 32-hex W3C trace id to 16 bytes, `V = FNV-1a-64(bytes) & 0xFF`, keep iff `(V*100)<(N*256)`; empty/missing `traceparent` always dropped. Monarch seeds by `inputs[].driver`: HTTP → KaiselRule (ingress and egress); `rabbitmq_message` → igris-rabbitmq (`IGRIS_RMQ_SAMPLE_PERCENTAGE`). RabbitMQ does not use Kaisel.
- `spec.maxQPSPerPod` — requests/sec Igris forwards per shadow pod replica (default 50). See Spike Guard below.
- `spec.oldImage` — control-a/b baseline image; pinned from target on first reconcile when unset (see above)
- `spec.mode` — `record` \| `replay` (default `record`)
- `spec.sessionID` — pin S3 session folder (required resolvable on replay)
- `spec.storage` — **required** BYOB S3 config (`type`, `bucketName`, `endpoint`, `region`, `credentialsSecretRef`, `retentionPolicy`)
- `inputs[].amqp.prodUrl` — host-only `amqp(s)://host[:port][/vhost]`; `inputs[].amqp.credentialsSecretRef` names the broker Secret (`username` / `password`) in the CR namespace

## Spike Guard (ingress load shedding)

Shadow pods run at fixed low replica counts with no autoscaling, so Monarch and the Igris hubs protect them from production traffic spikes:

| Mechanism | Where | Behavior |
|---|---|---|
| Concurrency cap | igris-http | `IGRIS_MAX_CONCURRENCY = shadowRoleReplicas × spec.maxQPSPerPod` (default 50), computed by Monarch and passed as an env var. Requests over the cap get `429` before trace resolution or multicast — never forwarded to shadow pods. |
| Per-message TTL | igris-rabbitmq | Every mirrored AMQP message published to the 3 shadow brokers carries `Expiration: "10000"` (10s) — stale backlog expires instead of being processed by a stuck shadow consumer. |
| Prod shadow queue bound | Monarch (`shadowtest_rabbitmq.go`) | `x-max-length: 500`, `x-overflow: drop-head` on the prod-side shadow queue declare — oldest messages are dropped once the queue backs up. `x-expires: 600000` (10m idle TTL) deletes the queue if it has no consumers — leak fail-safe when teardown cannot reach the prod broker. |

`shadowRoleReplicas` (currently `1`, one shared constant for control-a/b/candidate) is the single source of truth for both the shadow Deployment replica count and this capacity calc, so they can't drift.

## Status surface

`ShadowTest.status` is the topology contract consumed by Tusk (BFF) and the live node graph. The types live in `pipeline/monarch/api/v1alpha1` and are imported directly by consumers as `github.com/shadow-diff/monarch/api/v1alpha1`.

### `status.bootStep`

Coarse position in the boot sequence. Record and replay walk **disjoint sub-paths**, so the constants are the union of both — consumers must not assume every step occurs.

| Value | Covers | Modes |
|-------|--------|-------|
| `ValidatingInputs` | spec validation, shadow NS length/ownership, target Deployment lookup, session/secret sync | both |
| `ProvisioningSinks` | beru-local, Shop, ingress hub, replay dependencies | both |
| `ActivatingEgressTap` | KaiselRule eBPF capture | record |
| `BindingAMQP` | prod shadow queue `QueueBind` | record |
| `ProvisioningShadow` | control-a / control-b / candidate roles, replay trigger | replay |
| `Ready` | converged | both |
| `Failed` | terminal boot failure | both |

### `status.components`

| Field | True when |
|-------|-----------|
| `beruReady` | beru-local Deployment Available |
| `shopReady` | Shop Deployment Available |
| `igrisReady` | ingress hub (`*-igris` or `*-igris-rabbitmq`) Available |
| `kaiselRuleActive` | KaiselRule reconciled with capture phase `Ready` |
| `amqpBound` | prod shadow queue bound, or the ShadowTest declares no AMQP ingress |
| `ingressDrivers` | resolved `spec.inputs[].driver` values (e.g. `http_request`, `rabbitmq_message`); Tusk gates broker-tap nodes from this list |
| `shadowRolesReady` | map of `control-a`/`control-b`/`candidate` → Deployment readiness; empty in record mode |
| `targetDeployment` | resolved from `spec.targetDeployment` |

### `status.conditions`

Standard Kubernetes conditions carrying `observedGeneration`, keyed by type: `Ready`, `Progressing`, `Degraded`. Exactly one is True at a time, mirroring `status.phase`.

Every status write funnels through `patchStatusCore`, which snapshots the object, applies its mutators, and skips the API round-trip when the result is semantically equal — a converged ShadowTest re-reconciled by a Deployment or Pod watch event performs no write.

`kubectl get shadowtests` prints `Phase`, `Boot Step` and `Age`.

## Boot failure gates

While any Monarch-managed Deployment is not Available, Monarch requeues every 5s (`Progressing`). Terminal boot failure is declared when:

- a pod (app or init) reports `CrashLoopBackOff`, `ImagePullBackOff`, `ErrImagePull`, `CreateContainerConfigError`, `InvalidImageName`, or `ErrImageNeverPull`
- a container exits non-zero
- Deployment `ProgressDeadlineExceeded`
- the Deployment is still not Available after **7m** from creation (covers RabbitMQ `trace_on` startup-probe budget; CrashLoop/ImagePull still fail immediately)
- prod AMQP shadow queue `QueueDeclare` or `QueueBind` fails (record Phase 1 / Phase 3), including a missing `amqp.exchange` on the production broker
- target Deployment is missing, spec defaults cannot be resolved from it, or `spec.oldImage` cannot be pinned
- projected shadow namespace name exceeds 63 characters, or the name is already owned by another ShadowTest UID (sticky Failed via status patch only — the foreign namespace is not deleted)

On terminal failure Monarch:

1. Sets `status.phase=Failed`, `status.bootStep=Failed` and `status.message` (which component/pod and why), preserves the `status.components` snapshot observed at the moment of failure, and emits a Warning Event
2. Tears down runtime resources with the same steps as delete (KaiselRule, prod AMQP shadow queue if any, shadow namespace) — **without** removing CR finalizers or running S3 cleanup
3. Leaves the ShadowTest CR in place as an autopsy. Further reconciles are **sticky**: `phase=Failed` means do not recreate the stack

Retry: `kubectl delete shadowtest …` and re-apply. Spec-only edits do not clear Failed.

Teardown unreachable-broker / S3-retry trade-offs: [/control-plane/shadowtest-teardown-edge-cases.md](/control-plane/shadowtest-teardown-edge-cases.md).

## Beru wire ingest (Plan 2)

Beru exposes `POST /api/v1/ingest/wire` on `:8080` (`BERU_HTTP_ADDR`). Envelopes decode to `NetworkEventEnvelope` → `FromWireEnvelope` → existing `TraceRouter.Route` → Postgres `raw_reports`. OTLP Mongo span export is deprecated; callers should use wire ingest.

## Out of scope

- Envoy mongo_listener → Beru HTTP POST (Phase 2b access log)
- Ingress migration from `ext_proc` to `beru_ingest`
- `status.replayState=completed` (no Igris completion API yet)

# Citations

- [ARCHITECTURE_SHIFT.md](/refactor/ARCHITACTURE_SHIFT.md) — telemetry-dependent strategy
- [async-record-replay.md](/refactor/async-record-replay.md) — S3-backed async Record & Replay ADR
- [pipeline/monarch/internal/controller/shadowtest_envoy.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_envoy.go) — Envoy YAML generation
- [pipeline/monarch/api/v1alpha1/shadowtest_types.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/api/v1alpha1/shadowtest_types.go) — `BootStep`, `ComponentStatus`, `ShadowTestStatus`
- [pipeline/monarch/internal/controller/shadowtest_resources.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_resources.go) — Deployment patch, `patchStatusCore` status writer
- [pipeline/monarch/internal/controller/shadowtest_replay_trigger.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_replay_trigger.go) — automated replay start
- [pipeline/monarch/internal/controller/shadowtest_s3_cleanup.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_s3_cleanup.go) — S3 prefix retention finalizer
- [pipeline/pkg/s3utils/deleter.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/pkg/s3utils/deleter.go) — `DeletePrefix` / `TestKeyPrefix`
