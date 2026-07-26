---
type: Architectural Decision Record
title: Pixie and Recorder Removal
description: Why Shadow-Diff dropped its last third-party control plane, and the MongoDB egress diffing capability withdrawn along with it.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/kaisel
tags: [adr, data-plane, pixie, recorder, kaisel, mongodb, egress]
timestamp: 2026-07-26T10:30:00Z
---

# Pixie and Recorder Removal

> **Superseded in part by**
> [/data-plane/db-egress-capture-adr.md](/data-plane/db-egress-capture-adr.md):
> MongoDB egress diffing is restored by the shadow-soldier capture path, and the
> OTLP route this record retained has been retired.

## Context


Shadow-Diff captured production traffic through two independent stacks.

Kaisel — self-hosted eBPF, no external control plane — owned HTTP ingress, and
then HTTP egress once it gained per-connection request/response pairing.

Pixie owned what remained: HTTP egress spans (translated to Shop mocks by
Recorder) and **MongoDB egress spans** (sent to beru-local for diffing). It
brought a Vizier install, a cloud-connected control plane, a PxL query language,
a rendering gate (`pixie-gate`), and ~800 lines of bootstrap shell in the test
harness.

Once Kaisel seeded Shop directly, Recorder's only remaining job was translating
OTLP into JSON, and Pixie's only unique contribution was MongoDB capture.

The cost of keeping it was not theoretical. `platform_health_matrix` failed the
whole bats platform when `pixie-gate` was not Ready, so **12 of 13 cluster suites
could not start** on a machine without Pixie installed. The dependency was
blocking testing of components that had nothing to do with it.

## Decision

Delete the capture half; keep the analysis half.

| Removed | Kept |
|---|---|
| `pipeline/pixie-gate/` | Beru's OTLP receiver on `:4317` |
| `pipeline/recorder/` | Beru's MongoDB wire parser (`internal/otlp/mongo_parser.go`) |
| `PixieStreamRule` CRD and its reconcile | Beru's MongoDB diff (`internal/v2/diff/mongo_compare.go`) |
| `ShadowTest.spec.recorder` | `FromMongoEgress` report builder |
| Vizier bootstrap + PxL templates + bridge shell | beru-local's `:4317` port and Service entry |

The split is along the only line that matters: **the capture half was coupled to
a third party; the analysis half is not.** Beru's MongoDB code neither knows nor
cares what produced the spans it parses — it reads raw MongoDB wire bytes off an
OTLP attribute. Any future capture path can point at the same unchanged port.

## Consequences

### MongoDB egress diffing is withdrawn

This is the reason this record exists. There is no workaround and no partial
mode.

- A ShadowTest with a MongoDB dependency **still runs**. Ephemeral per-role
  MongoDB is provisioned by `shadowtest_dependencies.go`, independent of Pixie,
  and the shadow workers still read and write it.
- What is gone is the **verdict**: no MongoDB egress reports reach Beru, so no
  MongoDB egress regressions are detected. A trace that would previously have
  reported a Mongo count regression now reports nothing for that protocol.
- HTTP egress (Kaisel → Shop) and AMQP egress (`egress-relay-rabbitmq` → Beru)
  are unaffected.

Restoring it requires a new capture path only. The analysis half stays covered by
`internal/otlp/mongo_parser_test.go` and `internal/v2/diff/mongo_compare_test.go`,
which need neither a cluster nor a producer, so it cannot rot unnoticed while
dormant.

### The platform no longer depends on a third-party control plane

Every capture path is now self-hosted eBPF. There is no Vizier, no cloud
connection, no PxL, and no `pl` namespace. `ensure_platform_ready` no longer
gates on `pixie-gate`, which unblocks the integration suites.

### Tests that lost their subject

`testing/bats/integration/mongo_egress.bats` and its fixtures are deleted — they
exercised a capture path that no longer exists. The MongoDB-egress `@test` blocks
in the hybrid and http-ingress suites are removed for the same reason; their
HTTP-ingress and RabbitMQ-egress tests remain.

The seven E2E suites were rewritten to drop their Pixie setup. They are **not
verified by this change**: they could not start before it (hard Pixie gate), so
there is no baseline to compare against. Making them pass is separate work.

# Citations

- Kaisel egress capture: [/data-plane/kaisel-ebpf.md](/data-plane/kaisel-ebpf.md)
- Egress record and replay: [/data-plane/egress-record-replay.md](/data-plane/egress-record-replay.md)
- Beru MongoDB analysis (dormant): [`pipeline/beru/internal/otlp/`](../../pipeline/beru/internal/otlp/)
- MongoDB dependency provisioning (unaffected): [`pipeline/monarch/internal/controller/shadowtest_dependencies.go`](../../pipeline/monarch/internal/controller/shadowtest_dependencies.go)
