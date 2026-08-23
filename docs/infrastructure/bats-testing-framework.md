---
type: Architecture Specification
title: Bats-Core Modular Testing Framework
description: Bats-based integration and E2E harness with per-file shared ShadowTest environments, record→replay CR switch, Postgres settlement via beru_wait_verdict_settled, Jest-like reporter for BATS_PARALLEL_JOBS=1, and idempotent platform bootstrap.
resource: https://github.com/shadow-diff/monarch/tree/main/testing/bats
tags: [infrastructure, testing, bats, e2e, integration, monarch, beru, postgres, record-replay]
timestamp: 2026-08-22T17:30:00Z
---

# Bats-Core Modular Testing Framework

Shadow-Diff E2E validation uses **bats-core** under [`testing/bats/`](https://github.com/shadow-diff/monarch/tree/main/testing/bats). The harness co-locates all shell helpers, setup scripts, and manifests inside `testing/bats/` and enforces the platform vs ShadowTest lifecycle from [/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md](/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md).

## Lifecycle per `.bats` file

| Bats hook | Phase | Responsibility |
|-----------|-------|----------------|
| `setup_file` | Platform + suite stack | `ensure_platform_ready`, then ShadowTest CR **or** standalone beru+Postgres |
| `setup` | Isolation | `isolate_test_state` — fresh `BATS_TRACE_ID` per `@test` |
| `@test` | Validation | Record: MinIO + pin trace + one `kaisel_switch_to_replay`; replay: reuse pinned trace + `beru_wait_verdict_settled`; seed-only: `beru_assert_verdict_status` |
| `teardown_file` | Teardown | ShadowTest suites: delete CR, scrub Postgres (`beru_cleanup_shadow_test_postgres`) unless `BATS_KEEP` / `BATS_KEEP_POSTGRES`, then prod undeploy; postgres_verdict: scrub rows + delete `beru-verdict` |

**CI optimization:** Multiple `@test` blocks share one ShadowTest CR. Phase 4 runs in `teardown_file` only — not after each test.

**Mode stack suites:** `lifecycle_record.bats` / `lifecycle_replay.bats` apply once in `setup_file`, assert mode-specific Deployments / KaiselRule / `OPERATING_MODE`, then delete after Ready in the last `@test`.

## Shared record→replay cycle (`lib/record_replay_suite.bash`)

Primary hybrid and HTTP-otel E2E files (`python_hybrid.bats`, `nodejs_hybrid.bats`, `http_otel_rmq_*.bats`, `http_ingress_rmq_go.bats`) run **one** record→replay transition per file instead of repeating it in every replay `@test`:

1. **Record `@test`** — publish one prod message, assert MinIO session objects, `bats_pin_suite_trace`, then `kaisel_switch_to_replay` (HTTP-otel suites also call `bats_http_otel_firehose_ready`).
2. **Replay `@test`s** — `bats_use_suite_trace` restores the pinned trace/order id; `bats_assert_replay_mode`; assertions only (verdicts, worker logs).

| Helper | Role |
|--------|------|
| `bats_pin_suite_trace trace_id [order_id]` | Persist `RECORDED_TRACE_ID` / `RECORDED_ORDER_ID` in the suite state file |
| `bats_use_suite_trace` | Export pinned ids for replay `@test`s; fails if record `@test` did not run |
| `bats_assert_replay_mode` | Guard: `spec.mode=replay` and `replayState=started` |

Replay `@test`s must run after the record `@test` in the same file (default bats order). Filtering with `-f` on a replay test alone will fail at `bats_use_suite_trace`. Sampling suites and `kaisel_capture.bats` still use per-scenario cycles (`e2e_http_record_then_replay`, etc.).

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
  e2e-reset-kind.sh       # bootstrap a local Kind cluster from scratch
```

Local E2E uses **Kind** (host docker + `kind load`). One-shot bootstrap: [`testing/tools/e2e-reset-kind.sh`](https://github.com/shadow-diff/monarch/tree/main/testing/tools/e2e-reset-kind.sh); host Postgres DSN `localhost:15432`. See [/infrastructure/minikube-to-kind-migration.md](/infrastructure/minikube-to-kind-migration.md).

## Platform bootstrap (`lib/platform.bash`)

`ensure_platform_ready()` is idempotent and flock-guarded (`.cache/shadow-diff-bats/platform.lock`):

- Kind (host docker + `kind load`)
- Monarch CRDs + operator (`MONARCH_MODE=dev`)
- Kaisel DaemonSet (no per-test restart)
- Kaisel DaemonSet (`pipeline/kaisel/deploy/`, also via `e2e-reset-kind.sh`)

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

Beru UPSERTs Postgres `verdicts` on every report. **E2E pipeline end** is `beru_wait_verdict_settled`: wait until `raw_reports` has three distinct `shadow_role`s for the protocol (HTTP defaults to `direction=ingress`), then quiesce on `verdicts.updated_at` via `kubectl exec` / `psql` into the bats Postgres fixture (`--via=postgres`, default). Pass `--via=api` to use beru-local `GET /api/v1/traces/{id}?protocol=` instead (each poll spawns an ephemeral curl pod on Kind).

```bash
beru_wait_verdict_settled "$BATS_TRACE_ID" http --expect-status=MATCH
beru_wait_verdict_settled "$BATS_TRACE_ID" rabbitmq \
  --expect-status=MISMATCH --expect-count-regression=1
```

Record-phase helpers: `e2e_assert_session_objects`, `kaisel_ensure_record_mode`, `kaisel_switch_to_replay`, `e2e_http_record_then_replay` in `lib/kaisel.bash` / `lib/minio.bash`. Shared-cycle helpers: `bats_pin_suite_trace`, `bats_use_suite_trace`, `bats_assert_replay_mode` in `lib/record_replay_suite.bash`.

**Teardown scrub:** `bats_teardown_suite` calls `beru_cleanup_shadow_test_postgres` after CR delete (sessions, executions, traces, verdicts, raw_reports, noise_filters, shadow_tests). Skip with `BATS_KEEP=1` (leave CR) or `BATS_KEEP_POSTGRES=1` (delete CR, keep rows for The System /diffs).

`beru_wait_log` remains for debug / legacy log greps. Seed-only suites (`integration/beru/postgres_verdict.bats`) use `beru_assert_verdict_status` — history is static, so no drip wait. That suite targets standalone `svc/beru-verdict` via `BERU_SVC`/`BERU_NS` and cleans Postgres with `beru_cleanup_trace_postgres` / `beru_cleanup_shadow_test_postgres`. The poison-pill scenario uses `beru_wait_dead_letter`.

## Per-test isolation (`lib/test_isolation.bash`)

Default: trace UUID scoping. Optional `BATS_ISOLATE_MODE=full` for dependency resets between tests.

## Running

```bash
npm ci --prefix testing/bats   # once, for Jest-like reporter
make test-bats-integration
BATS_PARALLEL_JOBS=1 make test-bats-e2e   # Jest-like on TTY
make test-bats-e2e-smoke                  # Python HTTP-otel + Python RMQ hybrid only
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
| `beru/postgres_verdict.bats` | Standalone `beru-verdict` + Postgres fixture: seed-reports → WAL flush → API verdict assert; poison-pill DLQ (Postgres scale-to-0); per-test row cleanup |

Helpers: `monarch_wait_shadowtest_bringup_started`, `monarch_wait_shadowtest_cleaned`, `monarch_wait_dependency_available`, `monarch_assert_shadow_app_env`, `monarch_wait_amqp_queue_name`, `monarch_assert_prod_queue_absent`, `monarch_declare_conflicting_prod_queue`, `monarch_scale_controller` in `lib/monarch_assert.bash`.

### E2E suites (`testing/bats/e2e/`)

`make test-bats-e2e` runs `http-ingress/` + `rabbitmq-ingress/` + `record/`. `make test-bats-e2e-smoke` runs only `http_otel_rmq_python.bats` + `python_hybrid.bats` (Kind smoke path). Kaisel deep capture stays on `make test-bats-kaisel`.

| File | Scenario |
|------|----------|
| `rabbitmq-ingress/python_hybrid.bats` | One CR: record MinIO → replay; Postgres RMQ/Mongo count `MISMATCH` (Python) |
| `rabbitmq-ingress/nodejs_hybrid.bats` | Same hybrid path (Node.js) |
| `http-ingress/http_otel_rmq_*.bats` | One CR: HTTP record→replay; Postgres `MATCH` for http/rabbitmq/mongodb |
| `http-ingress/http_sampling.bats` | 10% sampling: MinIO keep + Postgres `MATCH`; drop absent from shadows |
| `record/record_http.bats` | Record-only MinIO ingress/egress object proof |
| `kaisel-capture/kaisel_capture.bats` | Deep Kaisel capture; replay HTTP egress `MATCH` via `beru_wait_http_egress_match` |

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
- [`testing/bats/lib/record_replay_suite.bash`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/lib/record_replay_suite.bash)
- [`testing/bats/manifests/`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/manifests)
- [`pipeline/beru/internal/v2/engine/router.go`](https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/internal/v2/engine/router.go)
