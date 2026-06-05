# RabbitMQ egress E2E fixtures

ShadowTest and prod manifests for Firehose → egress-relay-rabbitmq → Beru egress diffing.

## Full E2E (Kind)

From repo root (requires Beru + Monarch from `testing/scripts/e2e-reset-kind.sh`):

```bash
./testing/scripts/e2e-rabbitmq-egress-test.sh
```

## Quick verify (stack already up)

```bash
./testing/scripts/verify-rabbitmq-egress.sh
```

## What it exercises

1. Shadow RabbitMQ brokers with `trace_on` and `egress-relay-rabbitmq`
2. `rmq-test-worker` publishes JSON egress to `egress-events` with trace headers
3. Beru logs: `No egress regression for Trace <id> (rabbitmq)`
4. **W3C mode:** prod publish with `traceparent` only; worker sets `RMQ_EGRESS_TRACEPARENT_ONLY=1` so egress AMQP carries only `traceparent` (no `x-shadow-trace-id`); egress-relay and Beru resolve the 32-char trace id from `traceparent`.
