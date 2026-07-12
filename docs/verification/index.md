---
type: Directory Index
title: Verification Hub
description: Index for Shadow-Diff verification guides and E2E flow specifications.
resource: https://github.com/shadow-diff/monarch/tree/main/docs/verification
tags: [index, verification, e2e, bats]
timestamp: 2026-07-12T16:25:00Z
---

# Verification

Operational verification guides and E2E flow maps for Shadow-Diff pipelines.

## Document Map

* [/verification/VERIFICATION.md](/verification/VERIFICATION.md) — Step-by-step Monarch / Beru / Igris verification commands.
* [/verification/hybrid-rmq-e2e-flow.md](/verification/hybrid-rmq-e2e-flow.md) — Node/Python hybrid bats flow: RMQ ingress, HTTP record/replay, Mongo + RMQ egress regressions.
* [/verification/http-ingress-e2e-flow.md](/verification/http-ingress-e2e-flow.md) — Node/Python/Go http-ingress bats flow: Pixie → Siphon → igris-http, clean Mongo + RMQ egress diffs.
