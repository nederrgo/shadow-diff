# RabbitMQ E2E (Phase 5b)

Verifies Monarch prod shadow queue provisioning, `igris-rabbitmq` multicast (W3C **`traceparent`** + **`x-shadow-trace-id`**), shadow worker consumption, and Beru ingress/egress correlation.

## Prerequisites

- Kind cluster with Beru and Monarch (`./testing/scripts/e2e-reset-kind.sh`)
- Images built/loaded: `monarch:dev`, `igris-rabbitmq:dev`, `egress-relay-rabbitmq:dev`, `rmq-test-worker:dev`, `recorder:dev` (when `spec.downstreams` is set)
- Monarch operator with **`MONARCH_MODE=dev`** (set by `e2e-reset-kind.sh` and `e2e-rabbitmq-test.sh`) so helper images resolve to `:dev` tags without CR image overrides
- Monarch **must** include Phase 5b controller code. If status says `unsupported Igris driver "rabbitmq_message"`, rebuild and restart:

```bash
MONARCH_NO_CACHE=1 make -C pipeline/monarch docker-build IMG=monarch:dev
kind load docker-image monarch:dev --name <your-kind-cluster>
kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
kubectl rollout status deployment/monarch-controller-manager -n monarch-system
```

## Run

```bash
./testing/scripts/e2e-reset-kind.sh --no-reset
./testing/scripts/e2e-rabbitmq-test.sh
```

Or:

```bash
./testing/scripts/e2e-reset-kind.sh --run-rabbitmq-test
```

## Manifests

| File | Purpose |
|------|---------|
| `prod-rabbitmq.yaml` | Production RabbitMQ broker |
| `prod-target.yaml` | Stub target Deployment for ShadowTest |
| `shadowtest-rmq.yaml` | AMQP-only ShadowTest (no image overrides — Monarch resolves helpers) |

## Verify

- `igris-rabbitmq` logs show multicast without trace header errors
- Worker logs: `trace=<legacy-id>` after publish with `x-shadow-trace-id`
- Worker logs: `trace=<32-hex>` after traceparent-only publish
- Beru: `No regression for Trace <legacy-id>` and `No regression for Trace <32-hex>`
