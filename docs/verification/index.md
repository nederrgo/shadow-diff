---
type: Directory Index
title: Verification Hub
description: Index for Shadow-Diff verification guides and E2E flow specifications.
resource: https://github.com/shadow-diff/monarch/tree/main/docs/verification
tags: [index, verification, e2e, bats, postgres, record-replay, stress]
timestamp: 2026-08-23T10:00:00Z
---

# Verification

Operational verification guides and E2E flow maps for Shadow-Diff pipelines.

## Document Map

* [/verification/VERIFICATION.md](/verification/VERIFICATION.md) — Step-by-step Monarch / Beru / Igris verification commands.
* [/verification/hybrid-rmq-e2e-flow.md](/verification/hybrid-rmq-e2e-flow.md) — Node/Python hybrid bats: record→replay, Postgres RMQ/Mongo count regressions, sampling.
* [/verification/http-ingress-e2e-flow.md](/verification/http-ingress-e2e-flow.md) — Node/Python/Go http-ingress bats: record→replay, Postgres MATCH for http/RMQ/Mongo.
* [/verification/stress-load-test.md](/verification/stress-load-test.md) — Standalone stress suite: deterministic load, eBPF/S3 zero-loss, replay Postgres integrity.
