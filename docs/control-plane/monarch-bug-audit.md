---

## type: Audit Report
title: Monarch Operator Bug and Security Audit
description: Consolidated correctness and security findings for pipeline/monarch from full-codebase review (2026-08-11). Severity-ranked; evidence paths point at controller and RBAC sources. Checklist tracks remediation.
resource: [https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch)
tags: [audit, bugs, security, control-plane, monarch, kaisel, rabbitmq, rbac, secrets]
timestamp: 2026-08-18T18:40:00Z

# Monarch Operator Bug and Security Audit

Read-only review of `pipeline/monarch` (controller, CRD types, gRPC hub, RBAC, manager manifests) on 2026-08-11. Findings below track remediation with checkboxes (`[x]` = fixed, `[ ]` = open).

Related specs: [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md), [/control-plane/monarch-security-model.md](/control-plane/monarch-security-model.md), [/control-plane/monarch-status-stream.md](/control-plane/monarch-status-stream.md), [/control-plane/shadowtest-teardown-edge-cases.md](/control-plane/shadowtest-teardown-edge-cases.md).

---



## Summary


| Severity | Count | Fixed |
| -------- | ----- | ----- |
| Critical | 2     | 2     |
| High     | 12    | 3     |
| Medium   | 12    | 1     |
| Low      | 6     | 0     |


**Highest-priority fixes:** status gRPC auth → H4 DB creds → prod AMQP controls → workload hardening *(C1 shadow NS + C2 Kaisel ownership + H5 Secret read done)*.

### Progress checklist

- [x] **C1** — Shadow namespace length reject + UID ownership
- [x] **C2** — Kaisel target pods via Deployment ownership (no label matching)
- [x] **H1** — Dead Failed + 30s retry aligned with sticky Failed
- [x] **H2** — Prod AMQP bind-only (no exchange create)
- [ ] **H3** — Status gRPC auth + TLS
- [ ] **H4** — Per-test Beru DB creds
- [x] **H5** — Narrow Secret RBAC (writes shadow-only; reads BERU_DB_SECRET Role + secretSourceNamespaces)
- [ ] **H6** — Prod AMQP allowlist / Secret creds
- [ ] **H7** — Image allowlist + PSS / securityContext
- [ ] **H8** — Sanitize `beruGRPCTimeout`
- [ ] **H9** — Gate `targetNamespace`
- [ ] **H10** — Normalize `SHADOW_MODE`
- [ ] **H11** — Baked iptables image
- [ ] **H12** — Unique soldier ContainerPort names
- [x] **M2** — Terminating NS on ensure *(covered by C1)*
- [ ] **M1, M3–M12, L1–L6** — Medium/Low backlog

---



## Critical



### C1. Shadow namespace name collision + no ownership check

- [x] Fixed

**Class:** isolation / multi-tenant  
**Evidence:** `internal/controller/shadowtest_helpers.go` (`shadowNamespaceForCR`, `sanitizeDNSLabel`); `shadowtest_resources.go` (`ensureShadowNamespace`, `validateExistingNamespace`); `shadowtest_controller.go` (length + collision call site)

**Original bug:** `shadow-<crNS>-<crName>` was silently truncated to 63 DNS chars with no hash. Distinct CRs could map to the same name (path-join ambiguity or truncation). If the namespace already existed, ensure returned success without checking `shadow-diff.io/shadowtest-uid` or `DeletionTimestamp`.

**Impact:** One ShadowTest could overwrite another’s Deployments/Secrets; delete of one could wipe a shared NS and the other test’s stack.

**How we fixed it:**

1. **No silent truncation** — `shadowNamespaceForCR` sanitizes (lowercase / invalid chars) via `sanitizeDNSLabel` but never truncates. Shared `sanitizeForDNS` still truncates for Deployments/ConfigMaps/etc.
2. **Controller length reject** — If the projected name is longer than 63 characters, reconcile sticky-fails at ValidatingInputs (`phase=Failed`, clear rename message) with `ctrl.Result{}, nil` so it does not requeue forever.
3. **UID ownership on ensure** — Create still stamps `shadow-diff.io/shadowtest-uid=<ShadowTest.metadata.uid>`. On Get (and after Create `AlreadyExists` races), `validateExistingNamespace` requires that label to match `st.UID`. Mismatch → `ErrNamespaceCollision` → sticky Failed via status patch only (**not** `markBootFailed`, which would delete the foreign namespace).
4. **Terminating requeue** — If `DeletionTimestamp` is set, return a plain error so the controller requeues until the NS is gone (also closes **M2**).

See [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md) (Shadow namespace naming and ownership).

---



### C2. Empty / sparse target pod labels → Kaisel captures broadly

- [x] Fixed

**Class:** security (data exfiltration) / correctness  
**Evidence:** `internal/controller/shadowtest_kaisel.go` (`reconcileKaiselRule`); `shadowtest_target_pods.go` (`listPodsOwnedByDeployment`, `podOwnedByDeployment`); `shadowtest_controller.go` (`mapPodToShadowTests`)

**Original bug:** Monarch listed pods with `MatchingLabels(target.Spec.Template.Labels)`. Empty or shared labels (e.g. `app=api` on multiple Deployments) matched unrelated pods. Those PodIPs were written into the KaiselRule. `labelsMatch` on the pod watch had the same problem.

**Impact:** Unauthorized production traffic capture across apps/tenants in the target namespace.

**How we fixed it:**

1. **Ownership-only resolution** — `listPodsOwnedByDeployment` lists ReplicaSets owned by the target Deployment (`GetControllerOf`), lists all Pods in the target namespace, and keeps only pods whose controller ReplicaSet is in that set. No label or selector MatchingLabels.
2. **KaiselRule IPs** — `reconcileKaiselRule` uses the owned-pod list + existing Running/PodIP filters.
3. **Pod watch** — `mapPodToShadowTests` enqueues via `podOwnedByDeployment` (Pod → RS → Deployment chain). Removed `labelsMatch`.
4. **RBAC** — Manager SA can `get/list/watch` `apps/replicasets`.

See [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md) (Kaisel capture targets).

---



## High



### H1. Sticky `Failed` vs leftover 30s retry

- [x] Fixed

**Class:** correctness / ops  
**Evidence:** `shadowtest_controller.go` (`Get` target / `resolveSpecDefaults` / `ensureOldImage`); `shadowtest_fail_cleanup.go` (`markBootFailed`)

**Original bug:** Target-not-found, unresolvable spec defaults, and unpinable `oldImage` patched `phase=Failed` and returned `RequeueAfter: 30s` (poll until the Deployment exists). Sticky Failed at the top of `Reconcile` made that poll a no-op: the next run never called `Get(target)` again, and teardown waited 30s to start.

**Impact:** Operators saw a retry timer that did not retry. A live shadow stack whose target disappeared kept running for up to 30s after Failed.

**How we fixed it:**

1. Those three branches call `markBootFailed` on the same pass (Failed + Warning Event + KaiselRule / prod queue / shadow NS teardown).
2. While the namespace is terminating, cleanup still `RequeueAfter: 5s` until `Get` is NotFound.
3. Sticky Failed stays the recovery contract: delete the CR and re-apply. Auto-resume if the target appears later is a product feature, not this bug.

See [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md) (Boot failure gates).

---



### H2. Durable `ExchangeDeclare` on the production AMQP broker

- [x] Fixed

**Class:** security (prod side effect) / correctness  
**Evidence:** `internal/controller/shadowtest_rabbitmq.go` (`ensureProdShadowQueueDeclared`, `ensureProdShadowQueueBound`)

**Original bug:** Before queue declare/bind, Monarch ran an active `ExchangeDeclare` (durable, type from CR defaulting to `topic`) on `amqp.prodUrl`. A wrong name created durable prod topology that teardown never deleted. A type/durability/args mismatch against an existing exchange returned 406 and sticky-failed a valid capture.

**How we fixed it:**

1. **No prod exchange create** — `ensureProdShadowQueueDeclared` declares only the shadow queue. `ensureProdShadowQueueBound` `QueueBind`s to `amqp.exchange`.
2. **Missing exchange is terminal** — broker `NOT_FOUND` on bind goes through existing `markBootFailed` (sticky Failed + autopsy teardown). `status.message` includes the queue and exchange names.
3. **Shadow brokers unchanged** — `amqpExchangeType` still feeds igris-rabbitmq `ExchangeDeclare` on ephemeral role brokers.

See [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md) (Boot failure gates).

---



### H3. Status gRPC has no authn/authz or TLS

- [ ] Open

**Class:** security  
**Evidence:** `pkg/grpc/server.go` (`grpc.NewServer()`); `config/default/status_grpc_service.yaml`

Metrics are auth-filtered; `:9090` is not. Empty filter streams every ShadowTest (names, namespaces, phases, session IDs) in plaintext to anyone who can reach the ClusterIP.

**Fix direction:** mTLS or TokenReview + RBAC; NetworkPolicy default-deny for `:9090`.

---



### H4. Shared Beru DB Secret copied into every shadow namespace

- [ ] Open

**Class:** security (credential blast radius)  
**Evidence:** `shadowtest_beru_db.go` (`syncBeruDBSecret`)

`BERU_DB_SECRET` is read and `CreateOrPatch`’d into each shadow NS. Principals who can read Secrets or exec into beru-local get shared Postgres credentials for durable diff history.

**Fix direction:** Per-test DB role/DSN, or a mount that cannot be dumped as a raw shared Secret identity across tenants.

---



### H5. Cluster-wide Secret `*` verbs on the manager SA

- [x] Fixed (writes already shadow-only; reads now namespaced)

**Class:** security (RBAC)  
**Evidence:** `config/rbac/role.yaml`, `config/rbac/secret_source_reader_role.yaml`, `config/rbac/beru_db_secret_role.yaml`, `cmd/main.go` (`Cache.DisableFor` Secrets); Helm `monarch.secretSourceNamespaces`

**Original bug:** Compromised controller SA could get/list/create/update/delete all Secrets cluster-wide. Write verbs were later scoped to shadow namespaces via `shadow-workload-role` + per-ns `RoleBinding`, but cluster-wide `get`/`list`/`watch` on Secrets remained for cred copy.

**How we fixed it:**

1. **No Secret informer** — `ctrl.Options.Client.Cache.DisableFor` Secrets so the manager does not `list`/`watch` Secrets cluster-wide.
2. **`manager-role` has no Secret verbs** — cluster-wide read is ConfigMaps/Services/Pods only.
3. **`BERU_DB_SECRET`** — namespaced Role `get` with `resourceNames` (Kustomize: `beru-postgres` in `monarch-system`; Helm: parsed from `monarch.beruDbSecret`).
4. **`credentialsSecretRef`** — ClusterRole `secret-source-reader` (`get` only) bound at install time into listed CR namespaces. Helm `monarch.secretSourceNamespaces` (default `default`). E2E applies `testing/bats/manifests/monarch-secret-source-rbac.yaml`. The manager SA does not `bind` this ClusterRole, so `namespace-guard` stays closed.

Remaining blast radius: `get` any Secret **name** in a listed CR namespace; H4 (shared Beru DSN copied into every shadow NS) is unchanged.

---



### H6. Prod AMQP: CR-controlled dial, bind, and credential exposure

- [ ] Open

**Class:** security  
**Evidence:** `shadowtest_rabbitmq.go`; `shadowtest_igris_rabbitmq.go` (`PROD_URL`); `api/v1alpha1/shadowtest_types.go` (`AMQPInputSpec.ProdURL`)

ShadowTest author supplies `prodUrl` (often with embedded creds), exchange, and routing key (e.g. `#`). Controller dials that URL (SSRF), declares/binds a shadow queue (prod message siphon). URL lands in etcd and pod env.

**Fix direction:** Allowlisted broker hosts; broker creds via Secret; admission policy on AMQP fields; redact URLs in status/events.

---



### H7. Arbitrary images + root `NET_ADMIN` init; no shadow pod securityContext

- [ ] Open

**Class:** security (privilege)  
**Evidence:** `shadowtest_resources.go` (iptables init); `shadowtest_constants.go` (`iptablesSetupScript`)

Anyone who can create a ShadowTest can schedule arbitrary images into a new NS with a root init that has `NET_ADMIN` and runs shell/`apt-get`. App/Envoy/Shop/Beru containers lack restricted `securityContext` (unlike the manager).

**Fix direction:** Image allowlist; static iptables image (no apt); restricted PSS on shadow namespaces; `runAsNonRoot` on app sidecars.

---



### H8. `spec.beruGRPCTimeout` unsanitized into Envoy YAML

- [ ] Open

**Class:** security (injection)  
**Evidence:** `shadowtest_helpers.go` (`beruGRPCTimeoutFor`); `shadowtest_envoy.go`

Unvalidated CR string is interpolated into Envoy config and can break out of the timeout field to rewrite listeners/routes.

**Fix direction:** Parse as `time.Duration` or strict regex (`^[0-9]+(ms|s)$`) before render.

---



### H9. Cross-namespace `targetNamespace` with no authorization gate

- [ ] Open

**Class:** security  
**Evidence:** `shadowtest_helpers.go` (`targetNamespaceFor`)

A CR in tenant A can target Deployments in tenant B/prod. Combined with C2, this enables unauthorized traffic mirroring.

**Fix direction:** Admission webhook: `targetNamespace` must equal CR namespace or an explicit allowlist.

---



### H10. `beru-local` gets raw `spec.mode`, not normalized operating mode

- [ ] Open

**Class:** correctness  
**Evidence:** `shadowtest_beru_local.go` (`SHADOW_MODE: st.Spec.Mode`) vs `operatingMode(st)` used elsewhere

Empty mode yields `SHADOW_MODE=""` while Shop/Igris treat empty as `record` — Postgres session / Beru behavior can diverge.

**Fix direction:** Set `SHADOW_MODE` from `operatingMode(st)`.

---



### H11. iptables init installs packages on every pod start

- [ ] Open

**Class:** reliability / supply chain / security  
**Evidence:** `shadowtest_constants.go` (`iptablesSetupScript`)

`apt-get update && apt-get install iptables` on every shadow role pod. Fails air-gapped; slow/flaky otherwise; mutable root network path.

**Fix direction:** Prebuilt minimal image with iptables baked in; digest-pin.

---



### H12. Duplicate soldier `ContainerPort` names for same protocol

- [ ] Open

**Class:** correctness  
**Evidence:** `shadowtest_soldier.go` (`Name: sanitizeForDNS(rt.Protocol)`)

Two Postgres (or Redis) deps on different ports both get the same port name. Kubernetes rejects duplicate names → Deployment never rolls out.

**Fix direction:** Unique names (include dep name or listen port).

---



## Medium



### M1. Failed status patch errors ignored before teardown

- [ ] Open

**Evidence:** `shadowtest_fail_cleanup.go` (`markBootFailed` `_ = r.patchStatusCore`)

If the Failed write fails, CR may stay non-Failed while the stack is deleted → recreate flap.

---



### M2. Terminating namespace not handled on ensure

- [x] Fixed *(by C1)*

**Evidence:** `shadowtest_resources.go` (`ensureShadowNamespace`, `validateExistingNamespace`)

**Original bug:** Ensure succeeded on a deleting NS; later Creates failed messily instead of a clean requeue.

**How we fixed it:** `validateExistingNamespace` returns a requeue error when `DeletionTimestamp` is set (see C1).

---



### M3. Storage / Beru Secret overwrite by name in shadow NS

- [ ] Open

**Evidence:** `shadowtest_s3_env.go`, `shadowtest_beru_db.go`

`CreateOrPatch` by name with no collision/ownership guard; under C1, one test’s credentials can replace another’s.

---



### M4. S3 cleanup SSRF from controller

- [ ] Open

**Evidence:** `shadowtest_s3_cleanup.go`

On delete with `retentionPolicy: Delete`, manager dials attacker-controlled `storage.endpoint` with AWS keys.

---



### M5. Prod queue delete fails open if broker unreachable

- [ ] Open

**Evidence:** `shadowtest_rabbitmq.go` (`deleteProdShadowQueue`); intentional ADR in [/control-plane/shadowtest-teardown-edge-cases.md](/control-plane/shadowtest-teardown-edge-cases.md)

Dial failure skips delete; bound prod queue may drain until `x-expires` (10m). Documented trade-off; still a security/ops gap if TTL is insufficient.

---



### M6. Hardcoded DB connection strings drop credentials

- [ ] Open

**Evidence:** `shadowtest_dependencies.go` (`loopbackDependencyEnvValue`)

Proxied postgres/mongo URIs omit user/password/DB name apps often need.

---



### M7. Egress iptables only redirects ports 80 and 8080

- [ ] Open

**Evidence:** `shadowtest_constants.go` (`iptablesSetupScript`)

HTTPS (443) and other HTTP ports bypass Envoy/Shop mock — record/replay egress gaps.

---



### M8. Pod watch mapper lists every ShadowTest

- [ ] Open

**Evidence:** `shadowtest_controller.go` (`mapPodToShadowTests`)

Cluster-wide list on pod IP changes — costly at scale.

---



### M9. Floating public image tags

- [ ] Open

**Evidence:** `shadowtest_constants.go`, `shadowtest_images.go`, `config/manager/manager.yaml`

`debian:bookworm-slim`, envoy `:latest`-style tags, `controller:latest` — mutable supply chain.

---



### M10. AMQP credentials in CR / pod env / possible Event leakage

- [ ] Open

**Evidence:** types `ProdURL`; igris-rabbitmq env; `markBootFailed` Events with message text

Broker passwords visible to CR get / pod inspect; dial errors may echo URLs.

---



### M11. Only KaiselRule gets an owner reference

- [ ] Open

**Evidence:** `shadowtest_kaisel.go` (`SetControllerReference`)

Shadow workloads rely on NS delete for GC; orphans if NS delete is blocked.

---



### M12. `primaryContainerPort` only inspects the first container

- [ ] Open

**Evidence:** `shadowtest_helpers.go`

Multi-container targets without a named `http` port on container[0] fail or pick the wrong port.

---



## Low



### L1. Envoy admin bound to `0.0.0.0:9901`

- [ ] Open

**Evidence:** `shadowtest_envoy.go` (`envoyYAMLTemplate`)

Cluster peers who can reach the pod IP can hit Envoy admin.

---



### L2. Ingress ext_proc `failure_mode_allow: true`

- [ ] Open

**Evidence:** `shadowtest_envoy.go`

If beru-local is down, ingress continues without recording (diff integrity). Egress correctly fail-closed. Likely intentional.

---



### L3. No NetworkPolicy for status gRPC

- [ ] Open

**Evidence:** `config/default/kustomization.yaml`; only metrics NP exists

---



### L4. gRPC hub drops updates when subscriber buffer is full

- [ ] Open

**Evidence:** `pkg/grpc/hub.go`

Documented snapshot semantics; slow UI clients can see stale topology briefly.

---



### L5. Scaffolding TODOs in manager manifests

- [ ] Open

**Evidence:** `config/manager/manager.yaml`

Affinity/resources still marked `TODO(user)`.

---



### L6. `mintID` falls back to `0000` on `rand.Read` failure

- [ ] Open

**Evidence:** `shadowtest_s3_env.go`

Tiny collision risk under entropy failure.

---



## What looks solid

- Beru **8080 vs 8081** ingest split is consistent and tested (iptables redirect of 8080).
- Diff-of-diffs Envoy wiring: ingress fail-open to beru; egress fail-closed to Shop.
- Proxied vs Service-DNS env injection split for shadow-soldier vs relays.
- Proxied dependency **port collision** validation.
- Record/replay sequencing (Shop before ABC; replay execution id before beru-local).
- Delete / sticky teardown / S3 finalizer ordering (see teardown ADR).
- Manager pod: `runAsNonRoot`, drop ALL, read-only root, restricted PSS on `monarch-system`.
- Metrics endpoint authn/authz; HTTP/2 disabled by default on metrics/webhook TLS.
- Prod AMQP: bind-only to an existing exchange; shadow queues have `x-max-length` + `x-expires` mitigations.

---



## Suggested fix order

1. ~~**C1** — Unique shadow NS + UID ownership check~~ **done**
2. ~~**C2** — Kaisel selector / ownership resolution~~ **done**
3. **H3** — Auth + TLS (or NP + TokenReview) on status gRPC
4. ~~**H5** — Narrow Secret RBAC~~ **done** / **H4** — per-test DB creds
5. **H6** — Secret for broker URL; allowlist hosts
6. **H7 / H8 / H11** — Image allowlist, PSS, timeout validation, baked iptables image
7. **H9** — Admission on `targetNamespace`
8. ~~**H1** — Sticky Failed recovery~~ **done** / **H10 / H12** — `SHADOW_MODE`, soldier port names
9. Medium/Low backlog as capacity allows

---



## Citations

- Controller package: `pipeline/monarch/internal/controller/`
- Status gRPC: `pipeline/monarch/pkg/grpc/`
- Manager RBAC: `pipeline/monarch/config/rbac/role.yaml`
- Manager Deployment: `pipeline/monarch/config/manager/manager.yaml`
- CRD types: `pipeline/monarch/api/v1alpha1/shadowtest_types.go`
- Teardown ADR: [/control-plane/shadowtest-teardown-edge-cases.md](/control-plane/shadowtest-teardown-edge-cases.md)

