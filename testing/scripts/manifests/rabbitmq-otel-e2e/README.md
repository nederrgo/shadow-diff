# OTel RabbitMQ egress E2E manifests

End-to-end test for **zero-touch W3C trace propagation** across RabbitMQ consume/publish using OpenTelemetry Operator auto-instrumentation on a trace-unaware Node.js worker.

## Prerequisites

- Kind cluster with Monarch + Beru (`./testing/scripts/e2e-reset-kind.sh`)
- Monarch operator with **`MONARCH_MODE=dev`** (set by reset and test scripts)
- **cert-manager** and **OpenTelemetry Operator** installed (`e2e-reset-kind.sh` runs `testing/scripts/lib/otel-bootstrap.sh` by default; use `--skip-otel-bootstrap` only if already installed)
- `Instrumentation` CR pre-applied in shadow namespace before ShadowTest creates pods (handled by `e2e-otel-rabbitmq-test.sh`)

## Run

Full reset + test:

```bash
./testing/scripts/e2e-reset-kind.sh --run-otel-rabbitmq-test
```

Standalone (after reset):

```bash
./testing/scripts/e2e-reset-kind.sh
./testing/scripts/e2e-otel-rabbitmq-test.sh
```

Node.js hybrid (RabbitMQ ingress + Mongo OTLP + HTTP replay + RMQ Firehose — **minikube only**):

```bash
./testing/scripts/e2e-reset-minikube.sh   # if cluster not up
./testing/scripts/e2e-nodejs-hybrid-test.sh
```

## What it proves

1. Prod message published with **only** W3C `traceparent` (no `x-shadow-trace-id`) via RabbitMQ Management HTTP API
2. `igris-rabbitmq` multicasts to shadow brokers with trace headers
3. OTel-injected Node.js worker (`nodejs-test-worker`) consumes and publishes egress **without** app-level trace code
4. `egress-relay-rabbitmq` posts three role payloads to Beru
5. Beru completes RabbitMQ egress diff-of-diffs

## Expected success

Script output:

```
[SUCCESS] Beru reported no RabbitMQ egress regression for trace <32-hex-trace-id>
```

Beru log:

```
No egress regression for Trace <32-hex-trace-id> (rabbitmq)
```

## Files

| File | Purpose |
|------|---------|
| `prod-target-nodejs.yaml` | Prod deployment env for egress exchange/routing (no manual trace flag) |
| `prod-nodejs-worker.yaml` | Prod worker for `nodejs-hybrid-shadow` hybrid E2E |
| `shadowtest-otel-rmq.yaml` | ShadowTest with `rabbitmq_message` input, `otelInjection.language: nodejs` (helper images resolved by Monarch) |
| `shadowtest-nodejs-hybrid.yaml` | Node.js hybrid ShadowTest (RMQ ingress + mongo + record/replay) |

Shadow namespace (deterministic): `shadow-default-otel-rmq-test-shadow`
