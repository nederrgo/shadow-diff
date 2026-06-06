# egress-relay-rabbitmq

**egress-relay-rabbitmq** is the **L4a — analysis ingest** service for **AMQP egress diffing** in Shadow-Diff. It subscribes to **RabbitMQ Firehose** on each **shadow broker** (control-a, control-b, candidate), extracts trace ids from published message headers, and posts egress reports to Beru so Beru can run **diff-of-diffs** on outbound AMQP payloads across the three roles.

This observes **shadow** broker publishes only — not production traffic. It is separate from **Recorder (L4b)**, which records prod HTTP egress via Siphon, and from **Envoy ingress `ext_proc`**, which handles HTTP response diffing.

See [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) for the full pipeline.

---

## Role in the pipeline

```
Shadow worker (app)
    │  amqplib publish (egress exchange)
    ▼
Shadow RabbitMQ broker (per role)
    │  Firehose: amq.rabbitmq.trace / publish.#
    ▼
egress-relay-rabbitmq (one reconnect loop per role)
    │  POST /api/v1/egress/diff  { trace_id, workload, protocol, payload }
    ▼
Beru (L5)
    └── diff-of-diffs when control-a, control-b, and candidate reports arrive
```

| Step | Component | What happens |
| ---- | --------- | ------------ |
| 1 | **igris-rabbitmq (L2)** | Multicasts prod message to three shadow brokers with trace headers |
| 2 | **Shadow worker (L3)** | Consumes ingress, publishes egress JSON to broker |
| 3 | **Shadow RabbitMQ** | Firehose traces outbound `basic.publish` events |
| 4 | **egress-relay-rabbitmq (L4a)** | Reads Firehose, extracts trace id + body, forwards to Beru |
| 5 | **Beru (L5)** | Compares payloads → `No egress regression for Trace … (rabbitmq)` |

Trace ids are read from the **original application headers** embedded in Firehose metadata: `x-shadow-trace-id` first, then W3C `traceparent`. Workers can propagate context via OTel auto-instrumentation or manual header copy.

---

## How it works

### 1. Firehose subscription

For each shadow broker URL (`CONTROL_A_AMQP_URL`, `CONTROL_B_AMQP_URL`, `CANDIDATE_AMQP_URL`), the relay:

1. Dials the broker and declares an **exclusive, auto-delete** queue.
2. Binds to exchange **`amq.rabbitmq.trace`** with routing key **`publish.#`** (outbound publishes only).
3. Consumes Firehose events in a reconnect loop with exponential backoff.

Monarch enables Firehose on shadow RabbitMQ dependency pods (`rabbitmqctl trace_on` in startup probe). Without that, the relay receives no events.

### 2. Event handling

For each Firehose message with routing key `publish.*`:

1. Parse nested **`properties.headers`** from the trace envelope (original app headers).
2. Extract **trace id** (`x-shadow-trace-id` or `traceparent`).
3. Validate message **body** as JSON (the published application payload).
4. POST to Beru:

```json
{
  "trace_id": "<32-hex-or-shadow-id>",
  "workload": "control-a",
  "protocol": "rabbitmq",
  "payload": { ... }
}
```

### 3. Beru diff-of-diffs

Beru buffers one report per `(trace_id, workload)`. When all three roles have reported, it runs the same diff-of-diffs pattern as HTTP ingress:

1. Diff(control-a, control-b) → noise
2. Diff(control-a, candidate) → regressions

Success log: `No egress regression for Trace <id> (rabbitmq)`.

---

## Layout

```
egress-relay-rabbitmq/
  cmd/egress-relay-rabbitmq/   main entrypoint
  internal/
    consumer/                  Firehose subscribe + reconnect per role
    firehose/                  Trace envelope parsing, trace id extraction
    beru/                      POST /api/v1/egress/diff client
    trace/                     W3C traceparent helpers
    config/                    broker URLs, Beru endpoint, reconnect delays
```

---

## Build and test

From the repo root:

```sh
make egress-relay-rabbitmq-build              # → pipeline/egress-relay-rabbitmq/bin/egress-relay-rabbitmq
make egress-relay-rabbitmq-test
make egress-relay-rabbitmq-docker-build EGRESS_RELAY_RABBITMQ_IMG=egress-relay-rabbitmq:dev
```

From this directory:

```sh
make build
make test
make docker-build EGRESS_RELAY_RABBITMQ_IMG=egress-relay-rabbitmq:dev
```

---

## Configuration

| Variable | Required | Default | Description |
| -------- | -------- | ------- | ----------- |
| `CONTROL_A_AMQP_URL` | Yes | — | Shadow broker AMQP URL for control-a |
| `CONTROL_B_AMQP_URL` | Yes | — | Shadow broker AMQP URL for control-b |
| `CANDIDATE_AMQP_URL` | Yes | — | Shadow broker AMQP URL for candidate |
| `BERU_HTTP_URL` | Yes | — | Beru HTTP base (e.g. `http://beru.beru-system.svc.cluster.local:8080`) |
| `BERU_EGRESS_DIFF_PATH` | No | `/api/v1/egress/diff` | Egress diff ingest path |
| `RECONNECT_MIN_DELAY` | No | `1s` | Initial broker reconnect delay |
| `RECONNECT_MAX_DELAY` | No | `30s` | Max broker reconnect delay |

Monarch sets broker URLs from shadow RabbitMQ dependency Services and `BERU_HTTP_URL` from the ShadowTest's Beru HTTP endpoint.

---

## Monarch integration

Deployed in the **shadow namespace** when the ShadowTest uses **`inputs[].driver: rabbitmq_message`**:

| Resource | Name pattern |
| -------- | ------------ |
| Deployment | `<shadowtest-name>-egress-relay-rabbitmq` |

ShadowTest fields:

| Field | Effect |
| ----- | ------ |
| `spec.egressRelayRabbitmq.image` | Override container image (default `egress-relay-rabbitmq:latest`) |
| `spec.egressRelayRabbitmq.replicas` | Default `1` |

Monarch waits for this Deployment to be Available before marking the ShadowTest Ready (`waiting for egress-relay-rabbitmq` if the image is missing on Kind).

Example ShadowTest: [testing/scripts/manifests/rabbitmq-otel-e2e/shadowtest-otel-rmq.yaml](../../testing/scripts/manifests/rabbitmq-otel-e2e/shadowtest-otel-rmq.yaml).

**Prerequisite:** Shadow RabbitMQ brokers must expose Firehose (Monarch configures `RABBITMQ_ENABLED_PLUGINS_FILE` and `trace_on` startup on dependency containers).

---

## Verification

RabbitMQ egress diff E2E (manual trace propagation):

```sh
./testing/scripts/e2e-rabbitmq-egress-test.sh
```

OTel zero-touch AMQP egress (traceparent via OTel `amqplib` instrumentation):

```sh
./testing/scripts/e2e-otel-rabbitmq-test.sh
```

Expected Beru log:

```
No egress regression for Trace <trace-id> (rabbitmq)
```

See [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md).

---

## Related reading

- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — L4a AMQP egress vs L4b HTTP record/replay
- [pipeline/igrises/README.md](../igrises/README.md) — igris-rabbitmq (L2 AMQP ingress)
- [pipeline/recorder/README.md](../recorder/README.md) — L4b prod HTTP egress record (different path)
- [pipeline/beru/README.md](../beru/README.md) — egress diff API and diff-of-diffs
- [pipeline/monarch/DEPLOYMENT.md](../monarch/DEPLOYMENT.md) — `spec.egressRelayRabbitmq`
