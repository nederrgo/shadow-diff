# Igris

**Igris** is the **L2 — ingress hub** for Shadow-Diff. It accepts replayed or synthetic traffic and **multicasts** the same logical request to all three shadow roles — control-a, control-b, and candidate — so Beru can diff their responses.

Monarch deploys one of two variants depending on the `ShadowTest` input driver:

| Component | When Monarch uses it | Input |
| --------- | -------------------- | ----- |
| **[igris-http](igris-http/)** | HTTP or TCP ingress ShadowTests | Siphon replay, synthetic curl, or direct POST to Igris |
| **[igris-rabbitmq](igris-rabbitmq/)** | AMQP ingress ShadowTests (`inputs[].driver: rabbitmq_message`) | Prod RabbitMQ queue bound by Monarch |

See [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) for the full pipeline.

---

## Role in the pipeline

```
Prod traffic
    │
    ├─ HTTP/TCP ──► Siphon (optional) ──► igris-http ──► 3× shadow Services (:8888)
    │                                              └──► Envoy sidecar ──► app ──► Beru (ingress diff)
    │
    └─ AMQP ──────► prod shadow-diff queue ──► igris-rabbitmq ──► 3× shadow RabbitMQ brokers
                                                      └──► worker apps ──► egress-relay ──► Beru (egress diff)
```

Igris does **not** run diffing or store traces — it only **fans out** ingress with a shared trace id. Beru (via Envoy `ext_proc` for HTTP, or egress-relay for AMQP) performs correlation and diff-of-diffs.

---

## igris-http

Pluggable **HTTP and TCP** ingress hub.

### HTTP driver (`http_request`)

- Listens on ports defined in `/etc/igris/listeners.json` (Monarch writes this from `ShadowTest.spec.inputs`).
- Parses or generates a trace id from incoming headers (`x-shadow-trace-id`, then W3C `traceparent`).
- Returns **202 Accepted** immediately with trace headers echoing the id (async multicast).
- Clones method, path, body, and sanitized headers to three shadow URLs in parallel (`CONTROL_A_URL`, `CONTROL_B_URL`, `CANDIDATE_URL`).

Typical shadow target URLs point at each role's Service on **port 8888** (Envoy ingress listener), not the app port directly.

### TCP driver (`tcp_stream`)

- Accepts streaming TCP on configured listener ports.
- Opens relay connections to three shadow hosts (`CONTROL_A_ADDR`, etc.) on the **same port** as the listener.
- Used for non-HTTP protocols (e.g. Redis, Mongo wire protocol) where requests are byte streams rather than atomic HTTP messages.

### Layout

```
igris-http/
  cmd/igris/              main entrypoint
  internal/
    core/                 Hub, worker pool, multicast dispatch
    driver/http/          HTTP request driver (202 + clone)
    driver/tcpstream/     TCP stream relay driver
    config/               listeners.json + env validation
    trace/                W3C traceparent helpers
```

---

## igris-rabbitmq

**AMQP ingress multicaster** for RabbitMQ-driven ShadowTests.

### Flow

1. Monarch declares a prod broker queue `shadow-diff-<shadowtest-uid>` bound to the prod exchange/routing key from `spec.inputs[].amqp`.
2. **igris-rabbitmq** consumes that queue on the prod broker.
3. For each message it ensures **`x-shadow-trace-id`** and W3C **`traceparent`** on outbound headers (generating ids when missing).
4. Publishes the same body and routing key to the **`orders`** (or configured) exchange on **each** shadow RabbitMQ broker — one per role.

Shadow worker apps consume from their local broker; trace context must propagate on outbound HTTP (Envoy) and/or AMQP publish (egress-relay Firehose) for Beru to correlate.

### Layout

```
igris-rabbitmq/
  cmd/igris-rabbitmq/     main entrypoint
  internal/
    multicast/            prod consumer + 3-broker publisher
    config/               PROD_URL, SHADOW_QUEUE_NAME, shadow broker URLs
    trace/                header injection (x-shadow-trace-id + traceparent)
```

---

## Trace propagation

Both Igris variants inject the same correlation headers on outbound traffic:

| Header | Purpose |
| ------ | ------- |
| `x-shadow-trace-id` | Shadow-Diff trace id (32-char hex when derived from W3C) |
| `traceparent` | W3C Trace Context (`00-<trace-id>-<span-id>-01`) |

**Ingress priority:** `x-shadow-trace-id` → `traceparent` → generate new id.

Downstream shadow apps receive these headers on every cloned request/message. HTTP apps usually need no code changes (Envoy reports responses to Beru). AMQP workers need OTel auto-instrumentation or manual header copy on egress publish — see [pipeline/beru/README.md](../beru/README.md) and the OTel RabbitMQ E2E in [testing/scripts/manifests/rabbitmq-otel-e2e/README.md](../../testing/scripts/manifests/rabbitmq-otel-e2e/README.md).

---

## Build and test

From the repo root:

```sh
make igris-build              # → pipeline/igrises/igris-http/bin/igris
make igris-test
make igris-docker-build IGRIS_IMG=igris:dev

make igris-rabbitmq-build     # → pipeline/igrises/igris-rabbitmq/bin/igris-rabbitmq
make igris-rabbitmq-test
make igris-rabbitmq-docker-build IGRIS_RABBITMQ_IMG=igris-rabbitmq:dev
```

Per-component (from each subdirectory):

```sh
make build
make test
make docker-build
```

---

## Configuration

### igris-http (environment)

| Variable | Required | Description |
| -------- | -------- | ----------- |
| `CONTROL_A_URL` / `CONTROL_B_URL` / `CANDIDATE_URL` | Yes (HTTP) | Shadow Service base URLs for multicast |
| `CONTROL_A_ADDR` / `CONTROL_B_ADDR` / `CANDIDATE_ADDR` | Yes (TCP) | Shadow hostnames (port appended per listener) |
| `IGRIS_LISTENERS_FILE` | No | Path to listeners JSON (default `/etc/igris/listeners.json`) |
| `IGRIS_WORKER_POOL_SIZE` | No | Concurrent multicast workers (default `min(NumCPU×4, 32)`) |
| `IGRIS_MAX_BODY_SIZE` | No | HTTP body cap in bytes (default 512 KiB) |

**Listeners file** (written by Monarch):

```json
[
  { "port": 80, "driver": "http_request" },
  { "port": 8888, "driver": "http_request" }
]
```

### igris-rabbitmq (environment)

| Variable | Required | Description |
| -------- | -------- | ----------- |
| `PROD_URL` | Yes | Prod broker AMQP URL |
| `SHADOW_QUEUE_NAME` | Yes | Prod queue to consume (from `ShadowTest.status.amqpQueueName`) |
| `SHADOW_PUBLISH_EXCHANGE` | Yes | Exchange on shadow brokers (usually same as prod, e.g. `orders`) |
| `CONTROL_A_AMQP_URL` / `CONTROL_B_AMQP_URL` / `CANDIDATE_AMQP_URL` | Yes | Shadow broker URLs per role |

Monarch sets all of these when reconciling a `rabbitmq_message` ShadowTest.

---

## Monarch integration

Monarch deploys Igris into the **shadow namespace** created for each `ShadowTest`:

| Deployment | ShadowTest field | Input driver |
| ---------- | ---------------- | ------------ |
| `<name>-igris` | `spec.igris` | `http_request`, `tcp_stream`, etc. |
| `<name>-igris-rabbitmq` | `spec.igrisRabbitmq` | `rabbitmq_message` |

HTTP/TCP and AMQP paths are **mutually exclusive** for a given ShadowTest — AMQP tests skip HTTP Igris and use igris-rabbitmq instead.

Example AMQP ShadowTest: [testing/scripts/manifests/rabbitmq-otel-e2e/shadowtest-otel-rmq.yaml](../../testing/scripts/manifests/rabbitmq-otel-e2e/shadowtest-otel-rmq.yaml).

---

## Related reading

- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — layers, data flow, Envoy sidecar roles
- [pipeline/monarch/DEPLOYMENT.md](../monarch/DEPLOYMENT.md) — `spec.inputs`, `spec.igris`, `spec.igrisRabbitmq`
- [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) — E2E verification (HTTP ingress, RabbitMQ, OTel)
- [pipeline/beru/README.md](../beru/README.md) — where multicast traffic is analyzed
