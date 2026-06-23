# HTTP → RabbitMQ OTel E2E

Proves W3C `traceparent` correlation from **igris-http** ingress through OTel sidecar to **RabbitMQ Firehose** egress (egress-relay-rabbitmq → Beru).

Monarch deploys **igris-http** + **egress-relay-rabbitmq** when `inputs: http_request` and `dependencies` includes RabbitMQ (`MONARCH_MODE=dev` resolves helper images).

egress-relay deduplicates duplicate Firehose publishes (OTel `pika` double-wrap) by `(trace_id, span_id, payload)` within 100ms.

## Run (Kind or Minikube)

Prerequisites: `./testing/scripts/e2e-reset-kind.sh` or `./testing/scripts/e2e-reset-minikube.sh` (Beru + Monarch + OTel operator). Scripts auto-detect the cluster and load images accordingly.

```bash
./testing/scripts/e2e-http-otel-rmq-nodejs-test.sh
./testing/scripts/e2e-http-otel-rmq-python-test.sh
```

## Expected Beru logs (same trace id)

- `No regression for Trace <32-hex>` — ingress ext_proc
- `No egress regression for Trace <32-hex> (rabbitmq)` — Firehose relay

## Apps

| App | Path | OTel |
| --- | --- | --- |
| `http-rmq-test-app` | Node Express → `amqplib` publish | `http` + `amqplib` (zero-touch) |
| `http-rmq-python-worker` | Flask → `pika` publish | `flask` + `pika` (zero-touch; relay dedup) |
