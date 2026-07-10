# Handoff: testing/scripts/ → testing/bats/ Migration

## What was done

The old `testing/scripts/` directory has been fully removed. All shell infrastructure has been consolidated into `testing/bats/` (the canonical bats-core test runner) and a new `testing/tools/` directory for standalone developer utilities.

## New directory layout

```
testing/
  bats/
    lib/                    # bats facade layer (unchanged)
    helpers/                # bash libraries sourced by lib/ and setup scripts
      cluster-minikube.sh
      e2e-helpers.sh
      pixie-bridge.sh
      siphon-config.sh
      e2e-http-otel-rmq.sh
    setup/                  # scripts exec'd by bats setup_file / teardown_file
      delete-shadowtest.sh
      setup-local-pixie.sh
      start-pixie-stream-bridge.sh
    manifests/              # Kubernetes YAML (prod stacks, ShadowTest CRs, pixie-bridge RBAC)
    e2e/                    # .bats E2E suites (python_hybrid, nodejs_hybrid, http_otel_rmq_*)
    integration/            # .bats integration suites (mongo_egress)
    fixtures/               # per-suite CR YAML
    vendor/                 # bats-core, bats-support, bats-assert
    pixie-stream-bridge.sh  # long-running Pixie eBPF export loop
    debug-mongo-egress.sh   # 5-layer Pixie → Beru diagnostic script

  tools/                    # standalone developer utilities (NOT called by bats)
    e2e-reset-minikube.sh   # bootstrap a local minikube cluster from scratch
    send-json-trace.sh      # send synthetic gRPC ReportTraffic to Beru for debugging

  example-apps/             # test worker images (unchanged)
```

## Key path changes (old → new)

| Old | New |
|-----|-----|
| `testing/scripts/helpers/*.sh` | `testing/bats/helpers/*.sh` |
| `testing/scripts/setup/*.sh` | `testing/bats/setup/*.sh` |
| `testing/scripts/manifests/` | `testing/bats/manifests/` |
| `testing/scripts/pixie-stream-bridge.sh` | `testing/bats/pixie-stream-bridge.sh` |
| `testing/scripts/debug-mongo-egress.sh` | `testing/bats/debug-mongo-egress.sh` |
| `testing/scripts/setup/e2e-reset-minikube.sh` | `testing/tools/e2e-reset-minikube.sh` |
| `testing/scripts/send-json-trace.sh` | `testing/tools/send-json-trace.sh` |

## What was deleted

- `e2e/http-ingress/e2e-http-otel-rmq-{nodejs,python}-test.sh` — superseded by `bats/e2e/http_otel_rmq_*.bats`
- `e2e/rabbit-ingress/e2e-{nodejs,python}-hybrid-test.sh` — superseded by `bats/e2e/*_hybrid.bats`
- `integration/verify-{mongo,rabbitmq}-egress.sh` — superseded by `bats/integration/mongo_egress.bats`
- `helpers/otel-bootstrap.sh` — orphaned (not referenced anywhere)
- `helpers/e2e-reset-deploy.sh` — inlined into `testing/tools/e2e-reset-minikube.sh`
- `helpers/docker.sh` — WSL Docker credential workaround; all Makefiles now call `docker build` directly

## All source/exec paths updated

The following files had `testing/scripts/` references rewritten to their new locations:
- `testing/bats/lib/{cluster,platform,pixie,http_otel_rmq,shadowtest,traffic}.bash`
- `testing/bats/e2e/*.bats` (MANIFEST_DIR and kubectl apply paths)
- `testing/bats/helpers/` and `testing/bats/setup/` scripts (internal cross-sources)
- All `pipeline/*/Makefile` files (docker.sh → docker)
- `CLAUDE.md`, `pipeline/*/README.md`, `docs/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md`, `docs/verification/VERIFICATION.md`, `.claude/settings.local.json`

## To verify

```bash
# No stale testing/scripts references should remain
grep -rn "testing/scripts" . --exclude-dir=.git --exclude="log.md"

# Run the integration suite
make test-bats-integration

# Run the full E2E suite (requires minikube + platform up)
make test-bats-e2e
```

## Potential follow-up

- `testing/tools/e2e-reset-minikube.sh` no longer has a `--run-otlp-ingress-test` / `--run-record-replay` flag path (those standalone test scripts were deleted). If those manual flow paths are needed, wire them to `make test-bats-e2e` targets.
- `docs/verification/VERIFICATION.md` has several checklist items (`- [ ]`) that referenced old script names; they now reference `make test-bats-e2e` but may need more specific bats target names as the suite grows.
- The bats `lib/` files lazy-source from `testing/bats/helpers/` — if you add new helpers, place them there and add a `bats_source_X_helpers()` wrapper in the relevant `lib/` file.
