# RMQ + Mongo + RMQ egress E2E manifests

Kind E2E for **RabbitMQ ingress** (Igris `traceparent` multicast), **Mongo write** (log-verified), and **RabbitMQ egress** (Beru diff).

## Prerequisites

- Kind cluster with Monarch + Beru (`beru-system` and per-shadow `beru-local`)
- Monarch `MONARCH_MODE=dev`
- Images: `rmq-mongo-worker:dev`, `igris-rabbitmq:dev`, `egress-relay-rabbitmq:dev`, `monarch:dev`, `beru:dev`

## Run

```bash
./testing/scripts/e2e-rmq-mongo-test.sh
```

Or after minikube reset:

```bash
./testing/scripts/e2e-reset-minikube.sh --run-rmq-mongo-test
```

## Prod trigger

Publish with W3C `traceparent` only (Phase 3 Igris contract):

```json
{"headers":{"traceparent":"00-<32-hex-trace>-<16-hex-span>-01"}}
```

## Success criteria

| Check | Evidence |
|-------|----------|
| Igris multicast | All three roles log `trace=<32-hex>` |
| Mongo write | All three roles log `mongo insert ok trace=` |
| RMQ egress | All three roles log `rmq egress published` |
| Beru ingress | `No regression for Trace <hex>` (beru-local logs) |
| Beru RMQ egress | `No egress regression for Trace <hex> (rabbitmq)` (beru-local logs; egress-relay default) |

Mongo Beru egress diff is **not** asserted in v1 (Envoy mongo wire ingest is Phase 2b).

## Files

| File | Purpose |
|------|---------|
| `prod-rabbitmq.yaml` | Prod broker |
| `prod-target.yaml` | Prod stub with `RMQ_EGRESS_EXCHANGE` env |
| `shadowtest-rmq-mongo.yaml` | ShadowTest with mongo + rabbitmq dependencies |
