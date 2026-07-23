---
type: Directory Index
title: Infrastructure Hub
description: Index for Shadow-Diff infrastructure, deployment, and testing harness specifications.
resource: https://github.com/shadow-diff/monarch/tree/main/docs/infrastructure
tags: [index, infrastructure, testing]
timestamp: 2026-07-23T10:34:00Z
---

# Infrastructure

Specifications for cluster infrastructure, deployment patterns, and the Bats testing harness.

## Document Map

* [/infrastructure/bats-testing-framework.md](/infrastructure/bats-testing-framework.md) — Bats-core integration/E2E framework, shared ShadowTest per file, Beru settlement assertions, Jest-like reporter (`BATS_PARALLEL_JOBS=1` only).
* [/infrastructure/bats-parallel-isolation-roadmap.md](/infrastructure/bats-parallel-isolation-roadmap.md) — **Future plan** (not implemented): full per-file isolation + `bats --jobs`; today has no native `--jobs` and Jest output only at jobs=1.
* [/verification/hybrid-rmq-e2e-flow.md](/verification/hybrid-rmq-e2e-flow.md) — Hybrid Node/Python RMQ E2E suite data flow and per-`@test` assertions.
* [/verification/http-ingress-e2e-flow.md](/verification/http-ingress-e2e-flow.md) — HTTP ingress Node/Python/Go E2E suite data flow and per-`@test` assertions.
