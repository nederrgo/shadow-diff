# RabbitMQ E2E (Phase 5b)

Verifies Monarch prod shadow queue provisioning, `igris-rabbitmq` multicast (W3C **`traceparent`**), shadow worker consumption, and Beru ingress/egress correlation.

## Prerequisites

- Minikube with Monarch + Beru (`./testing/tools/e2e-reset-minikube.sh`)
- Images built into minikube docker: `monarch:dev`, `igris-rabbitmq:dev`, `egress-relay-rabbitmq:dev`, and the suite worker images
- Monarch operator with **`MONARCH_MODE=dev`** (set by `e2e-reset-minikube.sh` and bats `ensure_platform_ready`) so helper images resolve to `:dev` tags without CR image overrides
- Monarch **must** include Phase 5b controller code. If status says `unsupported Igris driver "rabbitmq_message"`, rebuild and restart:

```bash
eval "$(minikube docker-env)"
MONARCH_NO_CACHE=1 make -C pipeline/monarch docker-build IMG=monarch:dev
kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
kubectl rollout status deployment/monarch-controller-manager -n monarch-system
```

## Run

```bash
./testing/tools/e2e-reset-minikube.sh --no-reset
make test-bats-e2e
# or one file:
make test-bats-one FILE=e2e/rabbitmq-ingress/nodejs_hybrid.bats
```

## Manifests

| File | Purpose |
|------|---------|
| `prod-rabbitmq.yaml` | Production RabbitMQ broker |
| `prod-target.yaml` | Stub target Deployment for ShadowTest |
| `shadowtest-rmq.yaml` | AMQP-only ShadowTest (no image overrides — Monarch resolves helpers) |

## Verify

- `igris-rabbitmq` logs show multicast without trace header errors
- Worker logs include the expected trace id after publish with `traceparent`
- Beru: `No regression for Trace <id>`
