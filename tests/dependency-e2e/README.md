# Dependency E2E (Phase 5a)

Verifies Monarch ephemeral Redis provisioning, per-role `REDIS_HOST` injection, database isolation, and Beru ingress diff.

## Prerequisites

- Kind cluster with Beru (and Igris) images loaded (`./scripts/e2e-reset-kind.sh`)
- The script **upgrades the ShadowTest CRD** (so `spec.dependencies` is not stripped) and **rebuilds/redeploys Monarch** by default. Set `SKIP_MONARCH_BUILD=1` / `SKIP_MONARCH_DEPLOY=1` only if the running operator already includes dependency provisioning.
- The script **recreates the ShadowTest CRD** after Monarch deploy (avoids a terminating/stale CRD that strips `spec.dependencies`). Do not run `make -C monarch install` while the CRD is deleting.
- Verify manually: `kubectl explain shadowtest.spec.dependencies --api-version=engine.shadow-diff.io/v1alpha1`
- If `spec.dependencies` is set but there are no `redis-*` Deployments: the manager pod may still be an **old binary** under the same `monarch:dev` tag. Run:
  ```bash
  MONARCH_NO_CACHE=1 make -C monarch docker-build IMG=monarch:dev
  kind load docker-image monarch:dev --name monarch-test
  kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  kubectl delete shadowtest db-test-shadow -n default --wait=true
  kubectl apply -f tests/dependency-e2e/shadowtest-deps.yaml
  ```
- `db-test-app:dev` built and loaded (the script builds/loads by default)

## Run

```bash
./scripts/e2e-reset-kind.sh --no-reset
./examples/e2e-dependency-test.sh
```

Or in one step after a full reset:

```bash
./scripts/e2e-reset-kind.sh --run-dependency-test
```

## Manifests

| File | Purpose |
|------|---------|
| `db-test-prod.yaml` | Production Deployment + Service (`db-test-prod`) |
| `shadowtest-deps.yaml` | ShadowTest with `spec.dependencies` (Redis per role) |

## Test app

Built from [`examples/db-test-app/`](../../examples/db-test-app/) — static binary (`CGO_ENABLED=0`) with `POST /store` and `GET /store/{key}`.
