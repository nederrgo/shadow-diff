# RabbitMQ E2E (Phase 5b)

Verifies Monarch prod shadow queue provisioning, `igris-rabbitmq` multicast (W3C **`traceparent`** + **`x-shadow-trace-id`**), shadow worker consumption, and Beru ingress/egress correlation.

## Prerequisites

- Kind cluster with Beru and Monarch (`./scripts/e2e-reset-kind.sh`)
- Images: `monarch:dev`, `igris-rabbitmq:dev`, `rmq-test-worker:dev`, `recorder:dev` (required when `spec.downstreams` is set)
- Monarch **must** include Phase 5b controller code. If status says `unsupported Igris driver "rabbitmq_message"`, rebuild and restart:

```bash
MONARCH_NO_CACHE=1 make -C monarch docker-build IMG=monarch:dev
kind load docker-image monarch:dev --name <your-kind-cluster>
kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
kubectl rollout status deployment/monarch-controller-manager -n monarch-system
```

## Run

```bash
./scripts/e2e-reset-kind.sh --no-reset
./examples/e2e-rabbitmq-test.sh
```

Or:

```bash
./scripts/e2e-reset-kind.sh --run-rabbitmq-test
```

## Manifests

| File | Purpose |
|------|---------|
| `prod-rabbitmq.yaml` | Production RabbitMQ broker |
| `prod-target.yaml` | Stub target Deployment for ShadowTest |
| `shadowtest-rmq.yaml` | AMQP-only ShadowTest |

## Verify

- `igris-rabbitmq` logs show multicast without trace header errors
- Worker logs: `trace=<legacy-id>` after publish with `x-shadow-trace-id`
- Worker logs: `trace=<32-hex>` after traceparent-only publish
- Beru: `No regression for Trace <legacy-id>` and `No regression for Trace <32-hex>`
