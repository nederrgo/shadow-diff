---
type: Directory Index
title: Infrastructure Hub
description: Index for Shadow-Diff infrastructure, deployment, and testing harness specifications.
resource: https://github.com/shadow-diff/monarch/tree/main/docs/infrastructure
tags: [index, infrastructure, testing]
timestamp: 2026-08-18T18:40:00Z
---

# Infrastructure

Specifications for cluster infrastructure, deployment patterns, and the Bats testing harness.

## Document Map

* [/infrastructure/helm-charts.md](/infrastructure/helm-charts.md) — Helm install for `shadow-diff` (Monarch, Tusk, the-system) and `shadow-agent` (Kaisel); values, CRDs, `BERU_DB_SECRET`, `secretSourceNamespaces`, install order.
* [/infrastructure/bats-testing-framework.md](/infrastructure/bats-testing-framework.md) — Bats-core integration/E2E framework, record→replay CR switch, Postgres settlement (`beru_wait_verdict_settled`), Jest-like reporter (`BATS_PARALLEL_JOBS=1` only).
* [/infrastructure/bats-parallel-isolation-roadmap.md](/infrastructure/bats-parallel-isolation-roadmap.md) — **Future plan** (not implemented): full per-file isolation + `bats --jobs`; today has no native `--jobs` and Jest output only at jobs=1.
* [/verification/hybrid-rmq-e2e-flow.md](/verification/hybrid-rmq-e2e-flow.md) — Hybrid Node/Python RMQ E2E: record→replay + Postgres count regressions.
* [/verification/http-ingress-e2e-flow.md](/verification/http-ingress-e2e-flow.md) — HTTP ingress Node/Python/Go E2E: record→replay + Postgres verdict asserts.
