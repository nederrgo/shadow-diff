# OTel RabbitMQ egress E2E manifests

End-to-end fixtures for **zero-touch W3C trace propagation** across RabbitMQ consume/publish (hybrid bats suites).

## Prerequisites

- Minikube with Monarch + Beru (`./testing/tools/e2e-reset-minikube.sh`)
- Monarch operator with **`MONARCH_MODE=dev`** (set by reset / bats platform bootstrap)
- Pixie Vizier + pixie-gate for Mongo/HTTP capture paths used by hybrid suites

## Run

```bash
./testing/tools/e2e-reset-minikube.sh --no-reset
make test-bats-e2e
# or:
make test-bats-one FILE=e2e/rabbitmq-ingress/nodejs_hybrid.bats
make test-bats-one FILE=e2e/rabbitmq-ingress/python_hybrid.bats
```

See [/verification/hybrid-rmq-e2e-flow.md](../../../docs/verification/hybrid-rmq-e2e-flow.md).

## What it proves

1. Prod message published with W3C `traceparent` via RabbitMQ
2. `igris-rabbitmq` multicasts to shadow brokers with trace headers
3. Workers consume and publish egress (Mongo + HTTP + RMQ)
4. `egress-relay-rabbitmq` posts three role payloads to Beru
5. Beru completes RabbitMQ / Mongo egress diff-of-diffs (intentional candidate regressions where asserted)

## Expected success

Beru log patterns (see bats helpers):

```
Egress count regression for Trace <32-hex-trace-id> (rabbitmq)
Egress count regression for Trace <32-hex-trace-id> (mongodb)
```

## Files

| File | Purpose |
|------|---------|
| `prod-target-nodejs.yaml` | Prod deployment env for egress exchange/routing |
| `prod-nodejs-worker.yaml` | Prod worker for `nodejs-hybrid` hybrid E2E |
| `shadowtest-otel-rmq.yaml` | ShadowTest with `rabbitmq_message` input |
| `shadowtest-nodejs-hybrid.yaml` | Node.js hybrid ShadowTest (RMQ ingress + mongo + record/replay) |

Shadow namespace (deterministic): `shadow-default-<shadowtest-name>`
