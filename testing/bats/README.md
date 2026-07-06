# Bats-Core testing framework for Shadow-Diff

Modular integration and E2E tests using [bats-core](https://github.com/bats-core/bats-core).

## Prerequisites

- Linux host with Minikube **kvm2** or **virtualbox** driver (Pixie eBPF)
- `kubectl`, `jq`, `openssl` (host `sqlite3` optional — only for `beru_sqlite_query`)
- Pixie Cloud account (`px auth login` or `PIXIE_API_KEY`)
- Container images built into Minikube docker (`make` targets below)

## Layout

```
testing/bats/
  integration/     # Lighter multi-@test files (mongo egress, …)
  e2e/             # Full hybrid pipeline scenarios
  lib/             # Shared helpers (platform, shadowtest, beru_assert, …)
  fixtures/        # Per-suite ShadowTest + prod YAML
  vendor/          # bats-core, bats-support, bats-assert (vendored; no nested .git)
```

Refresh vendor deps (if missing):

```bash
for repo in bats-core bats-support bats-assert; do
  rm -rf "testing/bats/vendor/$repo"
  git clone --depth 1 "https://github.com/bats-core/$repo.git" "testing/bats/vendor/$repo"
  rm -rf "testing/bats/vendor/$repo/.git"
done
```

## Lifecycle (per `.bats` file)

| Hook | Phase |
|------|-------|
| `setup_file` | Platform bootstrap + prod app + ShadowTest CR (once) |
| `setup` | Fresh trace UUID per `@test` (`isolate_test_state`) |
| `@test` | Traffic + `beru_wait_log` / `beru_wait_verdict_settled` |
| `teardown_file` | Delete ShadowTest + prod stack (once) |

The pixie-stream-bridge runs continuously — tests never `pkill` or restart it.

## Running

```bash
# From repo root
make test-bats-integration   # integration/*.bats
make test-bats-e2e           # e2e/*.bats (hybrid + http_otel_rmq)
make test-bats               # both

# Or directly (export env from bats.config — Bats 1.13 has no --config-file flag)
export BATS_PARALLEL_JOBS=1 BATS_TEST_TIMEOUT=900

# If images are already loaded into minikube docker, skip builds:
SKIP_BUILD=1 SKIP_LOAD=1 ./testing/bats/run-one.sh e2e/python_hybrid.bats -f 'RabbitMQ egress'
```

## Environment variables

| Variable | Default | Effect |
|----------|---------|--------|
| `SKIP_BUILD` | `0` | Skip `docker build` in `setup_file` |
| `SKIP_LOAD` | `0` | Skip image load into Minikube |
| `SKIP_PLATFORM_BOOTSTRAP` | `0` | Health-check only; no install |
| `BERU_QUIESCENCE_SEC` | `5` | Verdict settlement quiescence window |
| `BATS_ISOLATE_MODE` | `trace` | `trace`, `wipe-beru`, or `full` |
| `MONARCH_IMG`, `BERU_IMG`, … | `:dev` tags | Image overrides |

## Beru assertions

Beru only emits final `mirrorLegacyLogs` lines after all three roles report. Prefer **log waits** per test:

```bash
# Built-in patterns (match logs.go wording)
beru_wait_log --grep="$(beru_log_egress_count_regression "$BATS_TRACE_ID" rabbitmq)"
beru_wait_log --grep="$(beru_log_no_egress_regression "$BATS_TRACE_ID" mongodb)"

# Any custom substring
beru_wait_log --grep="Egress regression for Trace ${BATS_TRACE_ID} (http): Field"
```

For SQLite/API verdict rows use `beru_wait_verdict_settled` (completeness + quiescence).

See [docs/infrastructure/bats-testing-framework.md](/infrastructure/bats-testing-framework.md).
