---
type: Architecture Specification
title: Bats-Core Modular Testing Framework
description: Bats-based integration and E2E harness with per-file shared ShadowTest environments, settlement-based Beru assertions, and idempotent platform bootstrap.
resource: https://github.com/shadow-diff/monarch/tree/main/testing/bats
tags: [infrastructure, testing, bats, e2e, integration, monarch, beru]
timestamp: 2026-07-06T12:35:00Z
---

# Bats-Core Modular Testing Framework

Shadow-Diff E2E validation uses **bats-core** under [`testing/bats/`](https://github.com/shadow-diff/monarch/tree/main/testing/bats). The harness wraps existing shell helpers (`e2e-helpers.sh`, `pixie-bridge.sh`, `delete-shadowtest.sh`) and enforces the platform vs ShadowTest lifecycle from [/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md](/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md).

## Lifecycle per `.bats` file

| Bats hook | Phase | Responsibility |
|-----------|-------|----------------|
| `setup_file` | Platform + ShadowTest | `ensure_platform_ready`, prod deploy, ShadowTest CR, Pixie rule waits |
| `setup` | Isolation | `isolate_test_state` — fresh `BATS_TRACE_ID` per `@test` |
| `@test` | Validation | Traffic + `beru_wait_verdict_settled` |
| `teardown_file` | Teardown | `delete_shadowtest_and_verify` **then** prod undeploy (prod must stay up until ShadowTest finalizer completes RMQ queue cleanup) |

**CI optimization:** Multiple `@test` blocks share one ShadowTest CR. Phase 4 runs in `teardown_file` only — not after each test.

## Platform bootstrap (`lib/platform.bash`)

`ensure_platform_ready()` is idempotent and flock-guarded (`.cache/shadow-diff-bats/platform.lock`):

- Minikube (kvm2/virtualbox)
- Monarch CRDs + operator (`MONARCH_MODE=dev`)
- Beru (`beru-system`)
- Pixie Vizier + **continuous** pixie-stream-bridge (no per-test restart)
- Siphon RBAC

Escape hatches: `SKIP_PLATFORM_BOOTSTRAP`, `SKIP_BUILD`, `SKIP_LOAD`, `BATS_FORCE_PLATFORM_BOOTSTRAP`.

## Beru settlement assertions (`lib/beru_assert.bash`)

Beru UPSERTs `verdicts` on every report. For **log-based** checks (what `mirrorLegacyLogs` emits), use `beru_wait_log` with a per-test pattern:

```bash
beru_wait_log --grep="$(beru_log_egress_count_regression "$BATS_TRACE_ID" rabbitmq)"
beru_wait_log --grep="$(beru_log_no_egress_regression "$BATS_TRACE_ID" mongodb)"
beru_wait_log --grep='custom substring from beru-local logs'
```

Helpers match `pipeline/beru/internal/v2/engine/logs.go` wording. For API/SQLite verdict rows use `beru_wait_verdict_settled` (completeness + quiescence).

## Per-test isolation (`lib/test_isolation.bash`)

Default: trace UUID scoping. Optional `BATS_ISOLATE_MODE=wipe-beru|full` for table wipes between tests.

## Running

```bash
make test-bats-integration
make test-bats-e2e
make test-bats
```

### E2E suites (`testing/bats/e2e/`)

| File | Scenario |
|------|----------|
| `python_hybrid.bats` | RMQ ingress + Mongo + HTTP record/replay + dual egress regressions (Python) |
| `nodejs_hybrid.bats` | Same hybrid path (Node.js worker) |
| `http_otel_rmq_python.bats` | HTTP igris ingress → OTel Mongo + RMQ Firehose egress (Python) |
| `http_otel_rmq_nodejs.bats` | HTTP igris ingress → OTel Mongo + RMQ Firehose egress (Node.js) |

See [`testing/bats/README.md`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/README.md).

# Citations

- [/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md](/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md)
- [`testing/bats/lib/platform.bash`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/lib/platform.bash)
- [`pipeline/beru/internal/v2/engine/router.go`](https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/internal/v2/engine/router.go)
