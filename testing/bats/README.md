# Bats-Core testing framework for Shadow-Diff

Modular integration and E2E tests using [bats-core](https://github.com/bats-core/bats-core).

## Prerequisites

- Linux host with Minikube **kvm2** or **virtualbox** driver (eBPF capture)
- `kubectl`, `jq`, `openssl`
- Container images built into Minikube docker (`make` targets below)
- For Jest-like output: Node/npm once — `npm ci --prefix testing/bats`

## Layout

```
testing/bats/
  integration/     # Lighter multi-@test files (mongo egress, beru postgres, …)
  e2e/             # Full hybrid pipeline scenarios
  lib/             # Shared helpers (platform, shadowtest, beru_assert, reporter, …)
  fixtures/        # Per-suite ShadowTest + prod YAML (or standalone beru)
  vendor/          # bats-core, bats-support, bats-assert (vendored; no nested .git)
  package.json     # tap-mocha-reporter pin
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
| `setup_file` | Platform bootstrap + suite stack (ShadowTest, or standalone beru+Postgres) |
| `setup` | Fresh trace UUID per `@test` (`isolate_test_state`) |
| `@test` | Traffic / seed + `beru_wait_log` / `beru_assert_verdict_status` |
| `teardown_file` | Tear down suite stack (once); skipped when `BATS_KEEP=1` |

The Kaisel DaemonSet runs continuously — tests never `pkill` or restart it.

### Standalone Beru + Postgres verdict suite

`integration/beru/postgres_verdict.bats` deploys `beru-verdict` in `monarch-system` against the bats Postgres fixture (no ShadowTest). It seeds the same histories as `pipeline/beru/internal/v2/diff/diff_test.go` via `POST /api/v1/debug/seed-reports`, asserts via the HTTP API, and deletes Postgres rows per test. One scenario scales Postgres to 0 so a poison WAL batch is discarded after 3 flush failures (asserted via beru logs — distroless has no `tar`/`cat` for file reads), then restores Postgres, restarts beru (remigrate wiped emptyDir), and seeds a clean MATCH.

```bash
# Rebuild/load beru if needed, then:
BATS_KEEP=1 ./testing/bats/run-one.sh integration/beru/postgres_verdict.bats

# After tests finish, inspect via slim API or The System /diffs:
kubectl -n monarch-system port-forward svc/beru-verdict 8080:8080
# → GET http://localhost:8080/api/v1/traces/<id>?protocol=mongodb

# Cleanup when done:
kubectl delete -f testing/bats/fixtures/integration/beru-postgres-verdict/beru.yaml
```

## Running

```bash
# Once (Jest-like reporter)
npm ci --prefix testing/bats

# From repo root
make test-bats-integration   # integration/*.bats
BATS_PARALLEL_JOBS=1 make test-bats-e2e   # Jest-like on TTY
make test-bats               # both

# Or directly
export BATS_PARALLEL_JOBS=1 BATS_TEST_TIMEOUT=900

# If images are already loaded into minikube docker, skip builds:
SKIP_BUILD=1 SKIP_LOAD=1 ./testing/bats/run-one.sh e2e/rabbitmq-ingress/python_hybrid.bats -f 'RabbitMQ egress'
```

### Jest-like reporter (jobs=1 only)

`run.sh` / `run-one.sh` pipe bats TAP through `tap-mocha-reporter spec` when:

- `BATS_PARALLEL_JOBS=1` (required — multi-process parallel interleaves TAP), or
- you set `BATS_REPORTER=spec`

(`make` often fails a TTY check, so auto mode keys off jobs=1 only — not whether stdout looks like a terminal.)

**Colors are on by default** (`FORCE_COLOR=1` / `TAP_COLORS=1`). Prefer a Linux `node` (`/usr/bin/node`); Windows `node.exe` under WSL often prints without ANSI. Opt out with `BATS_NO_COLOR=1`.

**Native `bats --jobs` is not used yet.** Parallelism today is a custom multi-process runner. See [docs/infrastructure/bats-parallel-isolation-roadmap.md](/infrastructure/bats-parallel-isolation-roadmap.md).

```bash
BATS_REPORTER=spec make test-bats-one FILE=e2e/http-ingress/http_otel_rmq_python.bats
BATS_REPORTER=tap make test-bats-e2e          # classic TAP
BATS_NO_COLOR=1 BATS_REPORTER=spec ...        # monochrome
BATS_PARALLEL_JOBS=2 make test-bats-e2e       # parallel; NO Jest reporter
```

## Environment variables

| Variable | Default | Effect |
|----------|---------|--------|
| `SKIP_BUILD` | `0` | Skip `docker build` in `setup_file` |
| `SKIP_LOAD` | `0` | Skip image load into Minikube |
| `SKIP_PLATFORM_BOOTSTRAP` | `0` | Health-check only; no install |
| `BERU_QUIESCENCE_SEC` | `5` | Verdict settlement quiescence window |
| `BATS_ISOLATE_MODE` | `trace` | `trace` or `full` (dependency reset) |
| `BATS_PARALLEL_JOBS` | `1` | Multi-process file parallelism; Jest reporter only when `1` |
| `BATS_REPORTER` | auto | `spec` \| `pretty` \| `tap` \| `off` (see above) |
| `BATS_NODE` | auto | Override Node binary (prefer Linux `/usr/bin/node` for colors) |
| `BATS_NO_COLOR` | `0` | Set `1` to disable Jest-like ANSI colors |
| `MONARCH_IMG`, `BERU_IMG`, … | `:dev` tags | Image overrides |

## Beru assertions

Beru only emits final `mirrorLegacyLogs` lines after all three roles report. Prefer **log waits** per test:

```bash
# Built-in patterns (match logs.go wording)
beru_wait_log --grep="$(beru_log_egress_count_regression "$BATS_TRACE_ID" rabbitmq)"
beru_wait_log --grep="$(beru_log_no_egress_regression "$BATS_TRACE_ID" mongodb)"

# Shop → Beru HTTP egress (kaisel-capture E2E)
beru_wait_http_egress_match "$trace_id" --signature="http:GET:/dep/echo?beru=…"

# Any custom substring
beru_wait_log --grep="Egress regression for Trace ${BATS_TRACE_ID} (http): Field"
```

For API verdict rows use `beru_wait_verdict_settled` (completeness + quiescence) or seed-only `beru_assert_verdict_status`.
HTTP egress API queries need `?protocol=http&direction=egress` (`beru_http_get_trace`).
Postgres cleanup: `beru_cleanup_trace_postgres` / `beru_cleanup_shadow_test_postgres`.

See [docs/infrastructure/bats-testing-framework.md](/infrastructure/bats-testing-framework.md).
