---
type: Architectural Decision Record
title: Shared Beru Removed in Favour of Per-ShadowTest beru-local
description: Decision to delete the beru-system Deployment and the spec.beruGRPCAddress escape hatch, leaving one beru-local per shadow namespace backed by a shared PostgreSQL.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller
tags: [data-plane, beru, adr, monarch, storage, deprecation]
timestamp: 2026-08-02T06:40:00Z
---

# Shared Beru Removed in Favour of Per-ShadowTest beru-local

## Context

Shadow-Diff supported two ways to run the L5 analysis sink: a shared `beru-system`
Deployment pointed at by `spec.beruGRPCAddress`, or a `beru-local` pod that Monarch
provisions inside each shadow namespace.

The shared instance existed to aggregate diff history across ShadowTests and to outlive
any single test — `beru-local` stores SQLite on an in-memory EmptyDir that dies with its
namespace. In practice it was never the default path: `beruGRPCAddressFor` returns the
shadow-namespace address whenever the field is empty, no bats suite or fixture ever set
it, the bats platform health gate for it had already been dropped, and its volume was an
`emptyDir: {}` rather than the persistent volume the documentation described.

Pointing every `beru-local` at a shared PostgreSQL (`BERU_DB_SECRET`) delivers the
aggregation and durability a shared pod was meant to provide, and does it better: a
database is built for concurrent writers, where one pod shared across tests is a single
point of failure and a noisy-neighbour risk.

## Decision

Delete the shared Beru entirely:

| Removed | Note |
| --- | --- |
| `pipeline/beru/deploy/` | Namespace, Deployment, Service, and the `beru-egress` NetworkPolicy |
| `spec.beruGRPCAddress` | The only mechanism for targeting an external Beru |
| `spec.beruIngestAddress` | Had no production reader |
| `usesLocalBeru`, `reconcileLocalBeruIfNeeded`, `beruIngestAddressFor` | Existed only to branch on the above |

Monarch now always provisions one `beru-local` per ShadowTest and gates reconcile on its
readiness unconditionally.

## Consequences

**Storage moved down a layer.** Compute is per-test and ephemeral; history is shared and
durable in PostgreSQL, partitioned by `shadow_test_name` and `session_id`. Without
`BERU_DB_SECRET`, `beru-local` stays on tmpfs SQLite and its verdicts die with the
namespace.

**Removing a CRD field is a breaking change.** Strict field validation rejects any
manifest still carrying `beruGRPCAddress` or `beruIngestAddress`. A ShadowTest persisted
with either reconciles onto `beru-local` silently — its Envoy `ext_proc` cluster is
repointed, which rolls the shadow Deployments.

**UI is cluster-wide The System.** Beru has no embedded dashboard. The System ShadowDiff
page reads shared Postgres projection tables (`shadow_sessions` / `traces` /
`diff_reports`) through Tusk, so history remains browsable after a ShadowTest namespace
is gone.

**A latent bug went with it.** `beruIngestURLFor`'s external branch returned port `8080`
for in-pod sidecars, which the shadow pod's iptables rules REDIRECT into Envoy's egress
listener. The remaining path always uses the `8081` ingest port.

**No NetworkPolicy ships any more.** `beru-egress` only ever covered `beru-system`; shadow
namespaces were never selected by it. Since NetworkPolicy is deny-only, nothing is blocked
by its absence. Monarch holds no `networking.k8s.io` RBAC, so egress hardening belongs to a
cluster policy engine targeting `app.kubernetes.io/managed-by: monarch`.

# Citations

* [/data-plane/index.md](/data-plane/index.md) — Data-plane document map
* [/data-plane/beru-postgres-storage.md](/data-plane/beru-postgres-storage.md) — The storage backends that replace shared-pod aggregation
* [/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md](/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md) — Where beru-local sits in the reconcile order
* [pipeline/monarch/internal/controller/shadowtest_beru_local.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller/shadowtest_beru_local.go) — beru-local provisioning
