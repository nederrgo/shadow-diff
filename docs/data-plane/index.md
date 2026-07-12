---
type: Directory Index
title: Data Plane Hub
description: Index for Shadow-Diff data-plane specifications covering capture, analysis, and egress mock replay.
resource: https://github.com/shadow-diff/monarch/tree/main/docs/data-plane
tags: [index, data-plane, beru, recorder, shop, pixie]
timestamp: 2026-07-12T16:20:00Z
---

# Data Plane

Specifications for the Shadow-Diff data-plane pipeline: traffic capture, diff analysis, and egress mock replay.

## Document Map

* [/data-plane/egress-record-replay.md](/data-plane/egress-record-replay.md) — Always-on Recorder → Shop → Envoy egress replay; dual-branch Pixie egress (client + server) + Shop Put dedup.
* [/verification/hybrid-rmq-e2e-flow.md](/verification/hybrid-rmq-e2e-flow.md) — How hybrid bats exercise RMQ ingress + HTTP replay + egress regressions.
