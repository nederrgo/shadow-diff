# Dependency E2E (Phase 5a)

Verifies Monarch ephemeral Redis provisioning, per-role `REDIS_HOST` injection, database isolation, and Beru ingress diff.

## Prerequisites

- Kind cluster with Beru (`./testing/scripts/e2e-reset-kind.sh`)
- Monarch operator with **`MONARCH_MODE=dev`** so `igris-http:dev` is resolved without `spec.igris` on the CR
- The script **upgrades the ShadowTest CRD** (so `spec.dependencies` is not stripped) and **rebuilds/redeploys Monarch** by default. Set `SKIP_MONARCH_BUILD=1` / `SKIP_MONARCH_DEPLOY=1` only if the running operator already includes dependency provisioning.
- The script **recreates the ShadowTest CRD** after Monarch deploy (avoids a terminating/stale CRD that strips `spec.dependencies`). Do not run `make -C pipeline/monarch install` while the CRD is deleting.
- Verify manually: `kubectl explain shadowtest.spec.dependencies --api-version=engine.shadow-diff.io/v1alpha1`
- If `spec.dependencies` is set but there are no `redis-*` Deployments: the manager pod may still be an **old binary** under the same `monarch:dev` tag. Run:

  ```bash
  MONARCH_NO_CACHE=1 make -C pipeline/monarch docker-build IMG=monarch:dev
  kind load docker-image monarch:dev --name monarch-test
  kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  kubectl delete shadowtest db-test-shadow -n default --wait=true
  kubectl apply -f testing/scripts/manifests/dependency-e2e/shadowtest-deps.yaml
  ```

- `db-test-app:dev` built and loaded (the script builds/loads by default)

## Run

```bash
./testing/scripts/e2e-reset-kind.sh --no-reset
./testing/scripts/e2e-dependency-test.sh
```

Or in one step after a full reset:

```bash
./testing/scripts/e2e-reset-kind.sh --run-dependency-test
```

## Manifests

| File | Purpose |
|------|---------|
| `db-test-prod.yaml` | Production Deployment + Service (`db-test-prod`) |
| `shadowtest-deps.yaml` | ShadowTest with `spec.dependencies` (Redis per role); Siphon off |

## Test app

Built from [`testing/example-apps/db-test-app/`](../../../example-apps/db-test-app/) — static binary (`CGO_ENABLED=0`) with `POST /store` and `GET /store/{key}`.
