---
type: Directory Index
title: Data Plane Hub
description: Index for Shadow-Diff data-plane specifications covering eBPF capture, diff analysis, and egress mock replay.
resource: https://github.com/shadow-diff/monarch/tree/main/docs/data-plane
tags: [index, data-plane, beru, shop, kaisel, ebpf, siphon, shadow-soldier]
timestamp: 2026-07-27T00:00:00Z
---

# Data Plane

Specifications for the Shadow-Diff data-plane pipeline: traffic capture, diff analysis, and egress mock replay.

## Document Map

* [/data-plane/kaisel-ebpf.md](/data-plane/kaisel-ebpf.md) — Self-hosted eBPF ingress and egress capture: AF_PACKET socket filter, kernel-side filtering, chunked perf transport, and per-connection request/response pairing for egress mocks.
* [/data-plane/kernel-sampling-adr.md](/data-plane/kernel-sampling-adr.md) — ADR: Kaisel decides traceparent sampling inside the eBPF filter; user space stays authoritative, the gate fails open, and the daemon gains a 5.17 kernel floor.
* [/data-plane/siphon-audit.md](/data-plane/siphon-audit.md) — Historical ADR: Siphon removed; Kaisel owns HTTP ingress admit/sample/forward.
* [/data-plane/beru-analysis.md](/data-plane/beru-analysis.md) — Beru v2 single-trace verdicts: completeness timeout, baseline void, compound diffs.
* [/data-plane/egress-record-replay.md](/data-plane/egress-record-replay.md) — Kaisel seeds Shop → Envoy egress replay; Shop buffers body and async-reports HTTP egress to Beru.
* [/data-plane/shadow-soldier.md](/data-plane/shadow-soldier.md) — Database egress capture: plain-text TCP proxy sidecar decoding MongoDB, PostgreSQL, Redis and MSSQL wire protocols, with fail-open piping and a bounded parser tap.
* [/data-plane/db-egress-capture-adr.md](/data-plane/db-egress-capture-adr.md) — ADR: database egress captured by an in-pod proxy; Beru's OTLP MongoDB route retired.
* [/data-plane/pixie-removal.md](/data-plane/pixie-removal.md) — ADR: Pixie and Recorder deleted; MongoDB egress diffing withdrawn while Beru's analysis half stays dormant.
* [/verification/hybrid-rmq-e2e-flow.md](/verification/hybrid-rmq-e2e-flow.md) — How hybrid bats exercise RMQ ingress + HTTP replay + egress regressions.
