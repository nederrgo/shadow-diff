---
type: Operations Guide
title: Platform Bootstrap and ShadowTest Lifecycle
description: One-time Monarch + Kaisel + shared PostgreSQL install; record then replay ShadowTest lifecycles; teardown of beru-local with the shadow namespace.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch
tags: [operations, control-plane, monarch, kaisel, kaiselrule, shadowtest, deployment, record-replay, beru, s3]
timestamp: 2026-08-17T18:48:00Z
---

# Platform Bootstrap and ShadowTest Lifecycle

Shadow-Diff splits **platform install** (once per cluster) from **ShadowTest lifecycle** (on demand). Every ShadowTest is either `record` or `replay` (`spec.mode`; default `record`). `spec.storage` (BYOB S3) is required. There is no live-traffic reconcile path — capture writes sessions to S3; replay loads them later.

Monarch and Kaisel are cluster infrastructure: they survive ShadowTest deletion. Monarch runs **beru-local** inside each shadow namespace, and deleting the ShadowTest deletes that namespace — the pod goes with it. Verdicts are written to shared PostgreSQL (`BERU_DB_SECRET` on the manager) and outlive the test ([/data-plane/beru-postgres-storage.md](/data-plane/beru-postgres-storage.md)). Durable capture artifacts live under the S3 prefix `shadow-diff/<cr-ns>/<cr-name>/`.

## Mental model

| Layer | Installed | Lifecycle |
|-------|-----------|-----------|
| **Monarch** operator | Once (`monarch-system`) | Survives all ShadowTests |
| **Kaisel** DaemonSet | Once (`kaisel-system`) | Survives all ShadowTests; watches `KaiselRule` CRs |
| **Object storage** | BYOB bucket (MinIO in local E2E) | Sessions under `shadow-diff/<ns>/<name>/`; cleaned on delete only if `retentionPolicy: Delete` |
| **PostgreSQL** (BYO) | Required (`BERU_DB_SECRET` on the manager) | Shared by every beru-local; survives ShadowTest delete |
| **beru-local** | Per ShadowTest | Lives in shadow namespace; **gone on teardown** |
| **ShadowTest** CR | Per test | `record` and/or `replay` → Ready → Delete |

Monarch writes a **`KaiselRule`** in **record** mode (target pod IPs + forward URLs). Kaisel updates eBPF maps in place — no daemon restart when pod IPs change. In **replay** mode Monarch deletes the `KaiselRule` (no live capture).

```mermaid
sequenceDiagram
  participant Ops as Platform_Operator
  participant Monarch
  participant Kaisel as Kaisel_DaemonSet
  participant S3 as Object_Storage
  participant User

  Ops->>Monarch: Install Monarch + Kaisel once
  Note over Ops,S3: BYOB bucket and shared PostgreSQL ready

  User->>Monarch: apply ShadowTest mode=record
  Monarch->>Monarch: sinks Shop+Igris then KaiselRule then AMQP bind
  Kaisel->>Monarch: POST ingress→Igris / egress→Shop
  Monarch->>S3: Igris/Shop flush session JSONL

  User->>Monarch: apply ShadowTest mode=replay sessionID=…
  Monarch->>Monarch: Shop + Igris + ABC (+ deps); delete KaiselRule
  Monarch->>Monarch: POST Igris /v1/replay/start
  Note over Monarch: beru-local diffs A/B/C

  User->>Monarch: kubectl delete ShadowTest
  Monarch->>Monarch: delete KaiselRule + shadow namespace
  Note over Monarch: beru-local destroyed with namespace
  Monarch->>S3: prefix cleanup if retentionPolicy=Delete
```

Reconcile contract details: [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md). Async record/replay ADR: [/refactor/async-record-replay.md](/refactor/async-record-replay.md).

---

## Phase 1 — Platform bootstrap (one time)

Install these **before** any ShadowTest. Prefer Helm for production clusters; Kustomize remains the path used by local E2E scripts.

### Option A — Helm

```bash
helm upgrade --install shadow-diff deploy/charts/shadow-diff \
  --namespace monarch-system --create-namespace
helm upgrade --install shadow-agent deploy/charts/shadow-agent \
  --namespace kaisel-system --create-namespace
```

Chart details and values: [/infrastructure/helm-charts.md](/infrastructure/helm-charts.md). The `shadow-diff` chart installs CRDs, Monarch, Tusk, and the-system; wire Postgres via `postgres.*` / `monarch.beruDbSecret` so beru-local can boot.

### Option B — Kustomize

### 1. Monarch operator

```bash
make -C pipeline/monarch install    # ShadowTest + KaiselRule CRDs
make -C pipeline/monarch deploy IMG=<registry>/monarch:<tag>
```

Set `MONARCH_MODE=dev` on the controller Deployment when using local `:dev` helper images (Minikube/Kind E2E).

### 2. Object storage (BYOB)

Every ShadowTest needs `spec.storage` (S3-compatible bucket + credentials Secret). Monarch never creates the bucket. Local E2E uses MinIO under `monarch-system` via [`testing/tools/e2e-reset-minikube.sh`](https://github.com/shadow-diff/monarch/tree/main/testing/tools/e2e-reset-minikube.sh) (bucket `shadow-diff-local`, Secret `shadow-diff-s3`).

### 3. Kaisel DaemonSet

```bash
kubectl apply -k pipeline/kaisel/deploy/
```

In record mode Monarch creates a `KaiselRule` with target pod IPs, ports, `igrisBaseURL` / egress Shop URL, and `samplePercentage`. Kaisel POSTs admitted ingress to Igris and egress pairs to Shop.

### 4. Shared PostgreSQL

Set `BERU_DB_SECRET` on the Monarch manager to a Secret with `DB_*` keys. Monarch replicates it into each shadow namespace and mounts it on beru-local. A missing env or Secret fails the ShadowTest. Verdicts persist keyed by `shadow_test_name` and `session_id`. See [/data-plane/beru-postgres-storage.md](/data-plane/beru-postgres-storage.md).

### Bootstrap verification

```bash
kubectl get pods -n monarch-system
kubectl get pods -n kaisel-system -l app=kaisel
```

---

## Phase 2 — Record mode

Capture production traffic into an S3 session. **No** control-a / control-b / candidate Deployments.

```bash
kubectl apply -f my-shadowtest-record.yaml   # spec.mode: record (or omit; default)
kubectl wait --for=condition=Ready shadowtest/my-app-shadow -n default --timeout=300s
```

Required on the CR: `spec.storage`, `targetDeployment`, images as needed. Monarch mints `status.currentSessionID` (`session-<unix>`) when unset (or honor `spec.sessionID`).

### Create order (record)

Bottom-up: sinks and unbound queue first, then eBPF tap, then AMQP bind. From `reconcileRecordMode` when `spec.mode` is `record` (or empty).

1. Finalizers on the CR (`shadowtest.finalizers.shadow-diff.io`, `shadow-diff.io/s3-cleanup`) — requeue
2. Validate inputs / dependencies / storage; load target Deployment; resolve defaults
3. Shadow namespace `shadow-<ns>-<name>`
4. Mint/patch `status.currentSessionID` (when unset)
5. Copy storage Secret from CR namespace → shadow namespace
6. Mode GC — clear `status.replayState`; delete leftover ABC Deployments/Services
7. beru-local Service, then Deployment — wait until Ready
8. **Phase 1 — sinks (taps closed):**
   - AMQP: declare durable shadow queue **unbound** + patch `status.amqpQueueName`
   - Shop Service + Deployment
   - Igris (HTTP ConfigMap→Deployment→Service, or igris-rabbitmq Deployment→Service)
   - Gate: Shop and Igris `AvailableReplicas > 0` — else `RequeueAfter: 2s`
9. **Phase 2 — eBPF tap:** create/apply `KaiselRule` (ingress + egress sinks are online)
10. **Phase 3 — async ingress:** AMQP `QueueBind` to the named prod exchange (must already exist; HTTP-only: no-op)
11. Status → `Ready`

Record does not provision control-a/b/candidate, shadow dependencies, or egress-relay-rabbitmq.

### What Monarch provisions (record)

| Resource | Notes |
|----------|--------|
| Shadow namespace | `shadow-<crNamespace>-<crName>` |
| beru-local | Always, one per shadow namespace |
| Unbound AMQP shadow queue | Declared in Phase 1 when AMQP input; bound in Phase 3; igris-rabbitmq consumes → S3 |
| Igris (HTTP/TCP or AMQP hub) | Writes ingress JSONL to S3 (`OPERATING_MODE=record`) |
| Shop | Writes egress JSONL to S3; Kaisel seeds via `POST /v1/record_egress` |
| `KaiselRule` | After sinks Ready (Phase 2) |
| Mode GC | Deletes ABC Deployments/Services; clears `status.replayState` |

S3 layout: `s3://<bucket>/shadow-diff/<ns>/<name>/sessions/<session-id>/{ingress,egress}/`.

### Concurrent ShadowTests

Supported. Each test gets its own shadow namespace, `KaiselRule`, and beru-local (when local). Kaisel merges active rules into one eBPF map set. Avoid pointing multiple ShadowTests at the same prod Deployment unless duplicate capture is intentional.

---

## Phase 3 — Replay mode

Deterministic A/B/C diff against a recorded session. **No** `KaiselRule` / live capture.

```bash
# Same CR name with mode=replay, or a new CR pointing at a prior session:
kubectl apply -f my-shadowtest-replay.yaml
# Requires resolvable session: spec.sessionID or status.currentSessionID
kubectl wait --for=condition=Ready shadowtest/my-app-shadow -n default --timeout=300s
```

### Create order (replay)

From `ShadowTestReconciler.Reconcile` when `spec.mode` is `replay`. Requires a resolvable session (`spec.sessionID` or `status.currentSessionID`).

1. Finalizers on the CR (`shadowtest.finalizers.shadow-diff.io`, `shadow-diff.io/s3-cleanup`) — requeue
2. Validate inputs / dependencies / storage; load target Deployment; resolve defaults
3. Shadow namespace `shadow-<ns>-<name>`
4. Resolve/patch `status.currentSessionID` from `spec.sessionID` or existing status
5. Copy storage Secret from CR namespace → shadow namespace
6. Mode GC — delete `KaiselRule` (no live capture)
7. beru-local Service, then Deployment — wait until Ready
8. Shadow dependencies (when declared): per dep × role Deployment + Service — wait until Ready
9. Shop Service, then Deployment — wait until Ready (preloads egress mocks from S3 before ABC)
10. Igris / ingress relays — wait until Ready:
    - AMQP: igris-rabbitmq Deployment → Service (admin `:9090`; loads ingress JSONL from S3) → egress-relay-rabbitmq — **no** prod shadow queue
    - HTTP/TCP: Igris ConfigMap → Deployment → Service; egress-relay if RabbitMQ deps need it
11. Per role (`control-a`, `control-b`, `candidate`): Envoy ConfigMap → Deployment (app + Envoy sidecar [+ shadow-soldier]) → Service — wait until Ready (Envoy TCP readiness on `servicePort`; soldier has no probe — loopback-only listeners)
12. Replay trigger — if `status.replayState` empty and Shop/hub/ABC are roll-ready (`ReadyReplicas >= desired`): `POST http://<igris|igris-rabbitmq>.<shadow-ns>.svc:9090/v1/replay/start` → `replayState=started`
13. Status → `Ready`

### What Monarch provisions (replay)

| Resource | Notes |
|----------|--------|
| Shadow namespace + beru-local | Same as record for analytics default |
| Dependencies | Per-role Mongo/RabbitMQ/… when declared |
| Shop → Igris → ABC | Shop preloads egress mocks from S3; HTTP Igris or igris-rabbitmq multicasts ingress from S3 |
| Envoy sidecars | Ingress ext_proc → Beru; egress ext_proc → Shop |
| Mode GC | Deletes `KaiselRule` |
| Replay trigger | When Shop/hub/ABC are roll-ready (`ReadyReplicas >= desired`; ABC Envoy TCP probe on ingress port) and `status.replayState` empty: `POST …:9090/v1/replay/start` → `replayState=started` |

beru-local runs diff-of-diffs on the three roles. Session JSONL in S3 is the durable capture input; verdicts live in shared PostgreSQL.

---

## Phase 4 — Deleting a ShadowTest

```bash
kubectl delete shadowtest my-app-shadow -n default
```

Monarch (`reconcileDelete`):

1. Deletes the prod AMQP shadow queue (if RabbitMQ input)
2. Deletes the `KaiselRule`
3. Deletes the shadow namespace (Igris, Shop, ABC, deps, **beru-local**, Secrets, …)
4. After the namespace is gone: S3 prefix cleanup when `storage.retentionPolicy: Delete` (`shadow-diff/<ns>/<name>/` only — never the BYOB bucket); `Retain` or unset skips cleanup
5. Removes finalizers `shadowtest.finalizers.shadow-diff.io` and `shadow-diff.io/s3-cleanup`

**Leave the Kaisel DaemonSet running.** Removing the `KaiselRule` drops its addresses from the eBPF maps.

Rows already written to a shared PostgreSQL are untouched.

### Boot failure vs delete

Terminal boot failure tears down KaiselRule + shadow namespace the same way, but **keeps** the CR (`phase=Failed`) and **skips** S3 cleanup / finalizer removal. Retry: delete the CR and re-apply. See [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md).

Teardown edge cases (unreachable prod broker vs unreachable S3, `x-expires` leak fail-safe, why those trade-offs): [/control-plane/shadowtest-teardown-edge-cases.md](/control-plane/shadowtest-teardown-edge-cases.md).

### Delete race note

Prefer waiting for Ready before delete in automation. An in-flight create that already passed the deletion check can still create resources after `kubectl delete`; re-delete if orphans appear. Once `deletionTimestamp` is set, later reconciles stay on `reconcileDelete`.

---

## Anti-patterns

| Avoid | Prefer |
|-------|--------|
| Treating beru-local as cluster-wide durable storage | Read verdicts before delete, or use shared Beru + PV / export; keep sessions in S3 |
| Rollout-restart Kaisel before each test | Leave it running; wait on `KaiselRule.status.phase` in record mode |
| Expecting ABC pods in `mode: record` | Record is capture-only; spin replay for A/B/C |
| Omitting `spec.storage` | Required for every ShadowTest |
| Folding eBPF capture into Monarch | Keep Monarch unprivileged; Kaisel owns capture |

---

## Related

- [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md) — record/replay reconcile, S3 finalizer, boot failure gates
- [/refactor/async-record-replay.md](/refactor/async-record-replay.md) — S3-backed async Record & Replay ADR
- [/data-plane/kaisel-ebpf.md](/data-plane/kaisel-ebpf.md) — capture daemon, privilege model
- [/infrastructure/bats-testing-framework.md](/infrastructure/bats-testing-framework.md) — bats platform vs ShadowTest hooks
- [pipeline/kaisel/deploy/](https://github.com/shadow-diff/monarch/tree/main/pipeline/kaisel/deploy) — RBAC, ConfigMap, DaemonSet
