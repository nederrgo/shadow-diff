---
type: Architecture Specification
title: Bats-Core Modular Testing Framework
description: Bats-based integration and E2E harness with per-file shared ShadowTest environments, settlement-based Beru assertions, Jest-like reporter for BATS_PARALLEL_JOBS=1, and idempotent platform bootstrap.
resource: https://github.com/shadow-diff/monarch/tree/main/testing/bats
tags: [infrastructure, testing, bats, e2e, integration, monarch, beru]
timestamp: 2026-07-30T15:45:00Z
---

# Bats-Core Modular Testing Framework

Shadow-Diff E2E validation uses **bats-core** under [`testing/bats/`](https://github.com/shadow-diff/monarch/tree/main/testing/bats). The harness co-locates all shell helpers, setup scripts, and manifests inside `testing/bats/` and enforces the platform vs ShadowTest lifecycle from [/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md](/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md).

## Lifecycle per `.bats` file

| Bats hook | Phase | Responsibility |
|-----------|-------|----------------|
| `setup_file` | Platform + ShadowTest | `ensure_platform_ready`, prod deploy, ShadowTest CR, KaiselRule waits |
| `setup` | Isolation | `isolate_test_state` — fresh `BATS_TRACE_ID` per `@test` |
| `@test` | Validation | Live traffic: `beru_wait_verdict_settled`; seed-only UI: `beru_assert_verdict_status` |
| `teardown_file` | Teardown | `delete_shadowtest_and_verify` **then** prod undeploy (prod must stay up until ShadowTest finalizer completes RMQ queue cleanup) |

**CI optimization:** Multiple `@test` blocks share one ShadowTest CR. Phase 4 runs in `teardown_file` only — not after each test.

**Mode stack suites:** `lifecycle_record.bats` / `lifecycle_replay.bats` apply once in `setup_file`, assert mode-specific Deployments / KaiselRule / `OPERATING_MODE`, then delete after Ready in the last `@test`.

## Directory layout

```
testing/bats/
  lib/                    # bats facade layer (platform, cluster, shadowtest, beru_assert, reporter, …)
  helpers/                # shared bash libraries sourced by lib/ and setup scripts
  setup/                  # scripts exec'd during setup_file / teardown_file
  manifests/              # Kubernetes YAML for prod stack + ShadowTest CRs
  e2e/                    # .bats E2E suites
  integration/            # .bats integration suites
  fixtures/               # per-suite CR YAML
  vendor/                 # bats-core, bats-support, bats-assert (vendored)
  package.json            # tap-mocha-reporter pin (Jest-like output)
  debug-mongo-egress.sh   # interactive 5-layer egress diagnostic


testing/tools/            # standalone developer utilities (not called by bats)
  e2e-reset-minikube.sh   # bootstrap a local minikube cluster from scratch
```

## Platform bootstrap (`lib/platform.bash`)

`ensure_platform_ready()` is idempotent and flock-guarded (`.cache/shadow-diff-bats/platform.lock`):

- Minikube (kvm2/virtualbox)
- Monarch CRDs + operator (`MONARCH_MODE=dev`)
- Kaisel DaemonSet (no per-test restart)
- Kaisel DaemonSet (`pipeline/kaisel/deploy/`, also via `e2e-reset-minikube.sh`)

Escape hatches: `SKIP_PLATFORM_BOOTSTRAP`, `SKIP_BUILD`, `SKIP_LOAD`, `BATS_FORCE_PLATFORM_BOOTSTRAP`.

When the platform is already healthy, image build/load is skipped. Suites that need non-core `:dev` images (e.g. `egress-relay-rabbitmq`, `shadow-soldier`) call `bats_ensure_dev_image` in `setup_file` so a missing tag is built into the cluster docker daemon instead of failing with `ErrImagePull`.

## Jest-like reporter (`lib/reporter.bash`)

[`run.sh`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/run.sh) / [`run-one.sh`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/run-one.sh) call `bats_invoke`, which may pipe bats TAP through `tap-mocha-reporter spec`.

**Hard limit:** Jest-like output works **only when `BATS_PARALLEL_JOBS=1`**. Parallel mode still uses a custom multi-process runner (not native `bats --jobs`); interleaved TAP cannot feed one reporter. See [/infrastructure/bats-parallel-isolation-roadmap.md](/infrastructure/bats-parallel-isolation-roadmap.md).

| `BATS_REPORTER` | Effect |
|-----------------|--------|
| *(unset)* | Auto `spec` when `BATS_PARALLEL_JOBS=1`; otherwise bats default |
| `spec` | Force Jest-like (falls back if jobs>1); **colors on by default** |
| `pretty` / `tap` | Bats built-in formatters |
| `off` | Unchanged bats invocation |

Install once: `npm ci --prefix testing/bats`. Prefer Linux `node` for ANSI colors (`BATS_NO_COLOR=1` to disable). Override binary with `BATS_NODE`.

## Beru settlement assertions (`lib/beru_assert.bash`)

Beru UPSERTs `verdicts` on every report. For **log-based** checks (what `mirrorLegacyLogs` emits), use `beru_wait_log` with a per-test pattern:

```bash
beru_wait_log --grep="$(beru_log_egress_count_regression "$BATS_TRACE_ID" rabbitmq)"
beru_wait_log --grep="$(beru_log_no_egress_regression "$BATS_TRACE_ID" mongodb)"
beru_wait_log --grep='custom substring from beru-local logs'
```

Helpers match `pipeline/beru/internal/v2/engine/logs.go` wording. For live-traffic API/SQLite verdict rows use `beru_wait_verdict_settled` (completeness + quiescence). Seed-only suites (`integration/beru/verdict_ui.bats`) use `beru_assert_verdict_status` — history is static, so no drip wait.

## Per-test isolation (`lib/test_isolation.bash`)

Default: trace UUID scoping. Optional `BATS_ISOLATE_MODE=wipe-beru|full` for table wipes between tests.

## Running

```bash
npm ci --prefix testing/bats   # once, for Jest-like reporter
make test-bats-integration
BATS_PARALLEL_JOBS=1 make test-bats-e2e   # Jest-like on TTY
make test-bats
```

### Integration suites (`testing/bats/integration/`)

| File | Scenario |
|------|----------|
| `monarch/http_input.bats` | HTTP replay stack Ready (igris-http, Shop, ABC roles, rabbitmq+mongo deps; Kaisel Disabled) |
| `monarch/ambiguous_ports.bats` | Multi-port target → `Failed` with `applicationPort` message |
| `monarch/boot_failure.bats` | Boot gate: bad igris-rabbitmq image → `Failed`; KaiselRule + shadow NS + prod AMQP queue torn down; kubectl apply cannot inject `status.phase` |
| `monarch/amqp_queue_failure.bats` | Prod AMQP `QueueDeclare` (conflicting args) → same `markBootFailed` autopsy/teardown as deployment boot fail; `QueueBind` covered by unit tests |
| `monarch/lifecycle_record.bats` | `mode=record` stack: KaiselRule + igris + shop, no ABC; delete after Ready |
| `monarch/lifecycle_replay.bats` | `mode=replay` stack: ABC + igris + shop, no KaiselRule, `replayState=started` |
| `monarch/lifecycle_mode_switch.bats` | Live `spec.mode` patch: record→replay removes KaiselRule / adds ABC; replay→record removes ABC / adds KaiselRule |
| `monarch/lifecycle_s3_retention.bats` | `retentionPolicy=Retain` keeps S3 prefix on CR delete; `Delete` scrubs `shadow-diff/<ns>/<name>/` |
| `monarch/deps_update.bats` | Live `spec.dependencies` add → dep Deployments + shadow app pod rollout with injected env |
| `mongo_egress.bats` | Mongo egress path (integration) |

Helpers: `monarch_wait_shadowtest_bringup_started`, `monarch_wait_shadowtest_cleaned`, `monarch_wait_dependency_available`, `monarch_assert_shadow_app_env`, `monarch_wait_amqp_queue_name`, `monarch_assert_prod_queue_absent`, `monarch_declare_conflicting_prod_queue`, `monarch_scale_controller` in `lib/monarch_assert.bash`.

### E2E suites (`testing/bats/e2e/`)

| File | Scenario |
|------|----------|
| `python_hybrid.bats` | RMQ ingress + Mongo + HTTP record/replay + dual egress regressions (Python) |
| `nodejs_hybrid.bats` | Same hybrid path (Node.js worker) |
| `http_otel_rmq_python.bats` | HTTP igris ingress → OTel Mongo + RMQ Firehose egress (Python) |
| `http_otel_rmq_nodejs.bats` | HTTP igris ingress → OTel Mongo + RMQ Firehose egress (Node.js) |
| `http_ingress_rmq_go.bats` | HTTP igris ingress → OTel Mongo + RMQ Firehose egress (Go) |
| `kaisel-capture/kaisel_capture.bats` | Record-mode Kaisel capture (+ S3); hybrid tests patch `mode=replay` mid-test for ABC/Shop/Beru (`make test-bats-kaisel`) |

Hybrid suite flow map: [/verification/hybrid-rmq-e2e-flow.md](/verification/hybrid-rmq-e2e-flow.md).  
HTTP ingress suite flow map: [/verification/http-ingress-e2e-flow.md](/verification/http-ingress-e2e-flow.md).

See [`testing/bats/README.md`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/README.md).

# Citations

- [/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md](/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md)
- [/infrastructure/bats-parallel-isolation-roadmap.md](/infrastructure/bats-parallel-isolation-roadmap.md)
- [/verification/hybrid-rmq-e2e-flow.md](/verification/hybrid-rmq-e2e-flow.md)
- [/verification/http-ingress-e2e-flow.md](/verification/http-ingress-e2e-flow.md)
- [`testing/bats/lib/platform.bash`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/lib/platform.bash)
- [`testing/bats/lib/reporter.bash`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/lib/reporter.bash)
- [`testing/bats/manifests/`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/manifests)
- [`pipeline/beru/internal/v2/engine/router.go`](https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/internal/v2/engine/router.go)
