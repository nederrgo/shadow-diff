# HTTP + Mongo + RMQ egress E2E manifests

Kind/minikube E2E for **HTTP ingress** (igris-http multicast), **Mongo write** (log-verified), and **RabbitMQ egress** (Beru diff).

## Run

```bash
./testing/scripts/e2e-http-mongo-test.sh
```

Or after minikube reset:

```bash
./testing/scripts/e2e-reset-minikube.sh --run-http-mongo-test
```

## Trigger

In-cluster `POST` to igris-http `:8888/work` with W3C `traceparent` only (Phase 3 contract).

## Success criteria

| Check | Evidence |
|-------|----------|
| Igris multicast | All three roles log `http work trace=<32-hex>` |
| Mongo write | All three roles log `mongo insert ok trace=` |
| RMQ egress | All three roles log `rmq egress published` |
| Beru ingress | `No regression for Trace <hex>` (beru-local) |
| Beru RMQ egress | `No egress regression for Trace <hex> (rabbitmq)` (beru-local) |

Unlike `rmq-mongo-worker`, there is **no synthetic loopback HTTP** — the Igris multicast *is* the ingress path through Envoy ext_proc.
