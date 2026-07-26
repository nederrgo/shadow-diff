---
type: Directory Index
title: Data Plane Hub
description: Index for Shadow-Diff data-plane specifications covering capture, analysis, and egress mock replay.
resource: https://github.com/shadow-diff/monarch/tree/main/docs/data-plane
tags: [index, data-plane, beru, recorder, shop, pixie, kaisel, ebpf, siphon]
timestamp: 2026-07-26T09:00:00Z
---

# Data Plane

Specifications for the Shadow-Diff data-plane pipeline: traffic capture, diff analysis, and egress mock replay.

## Document Map

* [/data-plane/kaisel-ebpf.md](/data-plane/kaisel-ebpf.md) — Self-hosted eBPF ingress and egress capture: AF_PACKET socket filter, kernel-side filtering, chunked perf transport, and per-connection request/response pairing for egress mocks.
* [/data-plane/siphon-audit.md](/data-plane/siphon-audit.md) — Historical ADR: Siphon removed; Kaisel owns HTTP ingress admit/sample/forward.
* [/data-plane/beru-analysis.md](/data-plane/beru-analysis.md) — Beru v2 single-trace verdicts: completeness timeout, baseline void, compound diffs.
* [/data-plane/egress-record-replay.md](/data-plane/egress-record-replay.md) — Kaisel eBPF pairing and Pixie/Recorder both seeding Shop → Envoy egress replay; Shop Put first-2xx dedup.
* [/verification/hybrid-rmq-e2e-flow.md](/verification/hybrid-rmq-e2e-flow.md) — How hybrid bats exercise RMQ ingress + HTTP replay + egress regressions.
