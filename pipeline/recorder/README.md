# Recorder

**Recorder** is the **L4b — egress record/replay** prod-ingest service for Shadow-Diff. It accepts **production outbound HTTP** from Pixie egress OTLP export (primary) or legacy Siphon TCP relay, and seeds Beru's **egress mock store**. Shadow pods replay prod downstream responses through Envoy's egress proxy without manual `seed_mock` calls.

Recorder is the **prod auto-record** path. It is separate from **egress-relay-rabbitmq**, which observes **shadow** RabbitMQ publishes for AMQP egress diffing.

See [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) for how Recorder fits in the full pipeline.

---

## Role in the pipeline

```
Prod app outbound HTTP
    │
    ├── Pixie eBPF egress PxL → pixie-stream-bridge px.export → OTLP gRPC :4317 (gzip)  [primary]
    │
    └── (legacy) Siphon TCP relay :8080 — length-prefixed R/S frames
    │
    ▼
Recorder (shadow namespace)
    │  OTLP span attrs or TCP HTTP parse → filter by downstream host (Host header)
    ▼
Beru  POST /v1/record_egress
    │  in-memory mock store (keyed by request hash)
    ▼
Shadow app egress (HTTP_PROXY → Envoy :15001)
    └──► Beru returns recorded response (or 599 on miss)
```

| Stage | Component | What happens |
| ----- | --------- | ------------ |
| **Capture** | **Pixie** + **pixie-stream-bridge** | Egress PxL filters `http_events` by `recordAndReplayHosts`; OTLP traces include `http.host`, bodies, status |
| **Parse + store** | **Recorder** | Maps OTLP attrs → `RecordPayload`; or reassembles TCP R/S frame pairs; posts to Beru |
| **Replay** | **Envoy egress sidecar** | Shadow outbound HTTP is hashed and looked up in Beru's mock store |

**Ingress diff** (Igris → three shadows → Beru) and **AMQP egress diff** (egress-relay-rabbitmq) do not involve Recorder. Recorder only supports **HTTP egress recording** from prod.

---

## How it works

### 1. Pixie OTLP egress (primary)

**pixie-stream-bridge** runs a separate egress PxL script when `PixieStreamRule.spec.recorderOtelEndpoint` is set. Monarch points this at:

```
<shadowtest>-recorder.<shadow-namespace>.svc.cluster.local:4317
```

Recorder listens for **gzip-compressed OTLP gRPC** traces and maps span attributes to `beru.RecordPayload`:

| Span attribute | Record field |
| -------------- | ------------ |
| `http.host` / `server.address` | Host (allowlist filter) |
| `url.path` / `http.target` | Path |
| `http.request.method` | Method |
| `http.request.body` | Request body |
| `http.response.status_code` | Status |
| `http.response.body` | Response body |

Only hosts matching `spec.recordAndReplay` (via mounted `recordAndReplay.json`) are recorded. In-cluster downstream calls are visible on the **server-side** pod; prod workers must send the expected `Host` header (e.g. `user-service.prod.internal` while connecting to cluster DNS).

### 2. Legacy Siphon → Recorder TCP format

Each prod egress TCP flow opens a connection to Recorder `:8080`. Siphon sends **5-byte framed chunks**:

| Byte | Meaning |
| ---- | ------- |
| `R` | Request leg (client → server bytes) |
| `S` | Response leg (server → client bytes) |
| Next 4 bytes | Big-endian payload length |
| Remaining | Raw TCP payload (may split mid-HTTP) |

Recorder buffers both legs on **per-connection pipes** until request and response streams are attached, then starts an HTTP parser goroutine.

### 3. Request/response pairing (TCP path)

For each Siphon TCP connection, `SessionStore`:

1. Accumulates `R` frames into a request pipe and `S` frames into a response pipe.
2. Starts `parse.RunBidirectional` when both legs exist.
3. Reads sequential HTTP request/response pairs from the pipes (supports keep-alive / multiple transactions on one connection).
4. Evicts incomplete pairs after `RECORDER_PAIR_TIMEOUT` (default 30s).

### 4. Record-and-replay host filtering

Only transactions whose `Host` matches `spec.recordAndReplay` on the ShadowTest are recorded. Monarch writes the allowlist to `/etc/recorder/recordAndReplay.json`:

```json
[
  { "host": "httpbin.org", "ignore_paths": ["/uuid"] }
]
```

Wildcard hosts (`*.example.com`) are supported. Non-matching hosts are skipped.

### 5. Beru ingest

Matching records are posted asynchronously to:

```
POST {BERU_HTTP_URL}/v1/record_egress
```

Payload includes method, host, path, request body, response status/headers/body, and optional `ignore_paths` for hash stability. Beru stores the entry in the same mock map used by `POST /v1/seed_mock` and Envoy egress lookup.

---

## Layout

```
recorder/
  cmd/recorder/           main entrypoint (TCP :8080 + OTLP :4317 concurrently)
  internal/
    ingest/               TCP server, framing, session pairing
    parse/                HTTP request/response parser, host filter
    beru/                 POST /v1/record_egress client
    config/               env + recordAndReplay.json loader
    receiver/             OTLP gRPC trace ingest (Pixie egress export)
```

---

## Build and test

From the repo root:

```sh
make recorder-build              # → pipeline/recorder/bin/recorder
make recorder-test
make recorder-docker-build RECORDER_IMG=recorder:dev
```

From this directory:

```sh
make build
make test
make docker-build RECORDER_IMG=recorder:dev
```

---

## Configuration

| Variable | Required | Default | Description |
| -------- | -------- | ------- | ----------- |
| `BERU_HTTP_URL` | Yes | — | Beru HTTP base URL (e.g. `http://beru.beru-system.svc.cluster.local:8080`) |
| `RECORDER_LISTEN_ADDR` | No | `:8080` | TCP address for legacy Siphon egress relay connections |
| `RECORDER_OTLP_GRPC_ADDR` | No | `:4317` | gRPC OTLP trace receiver (Pixie egress `px.export`) |
| `RECORDER_RECORD_AND_REPLAY_FILE` | No | `/etc/recorder/recordAndReplay.json` | JSON allowlist of record-and-replay hosts |
| `RECORDER_PAIR_TIMEOUT` | No | `30s` | Drop incomplete request/response pairs after this duration (TCP path) |
| `RECORDER_MAX_FRAME_BYTES` | No | `5242880` (5 MiB) | Max single frame payload from Siphon (TCP path) |

Monarch sets `BERU_HTTP_URL`, both listen addresses, and mounts the recordAndReplay ConfigMap when `spec.recordAndReplay` is non-empty.

---

## Monarch integration

Recorder is deployed into the **shadow namespace** when a ShadowTest defines **`spec.recordAndReplay`**:

| Resource | Name pattern | Purpose |
| -------- | ------------ | ------- |
| Deployment | `<shadowtest-name>-recorder` | Recorder pod |
| Service | `<shadowtest-name>-recorder` | Legacy TCP `:8080`; Pixie OTLP `:4317` |
| ConfigMap | `<shadowtest-name>-recorder-config` | `recordAndReplay.json` from `spec.recordAndReplay` |

Monarch also reconciles **`PixieStreamRule`** when egress recording is enabled:

| PixieStreamRule field | Set when |
| --------------------- | -------- |
| `recorderOtelEndpoint` | `spec.recordAndReplay` non-empty → shadow Recorder Service `:4317` |
| `recordAndReplayHosts` | Hostnames from `spec.recordAndReplay[].host` (egress PxL `req_host` filter) |
| `otelEndpoint` | HTTP ingress capture enabled (`spec.siphon`) → shadow Siphon `:4317` |

ShadowTest fields:

| Field | Effect |
| ----- | ------ |
| `spec.recordAndReplay[]` | Enables Recorder + Pixie egress export + Envoy egress proxy on shadow apps |
| `spec.recordAndReplay[].host` | Hostname allowlist for recording |
| `spec.recordAndReplay[].ignoreRequestPaths` | JSON paths excluded from request hash (volatile fields) |
| `spec.recorder.image` | Override container image (default `recorder:latest`) |

Example ShadowTest with recordAndReplay: [testing/scripts/manifests/e2e-shadowtest.yaml](../../testing/scripts/manifests/e2e-shadowtest.yaml). Hybrid (RMQ + Mongo + HTTP replay): [testing/scripts/manifests/rabbitmq-otel-e2e/shadowtest-python-hybrid.yaml](../../testing/scripts/manifests/rabbitmq-otel-e2e/shadowtest-python-hybrid.yaml).

---

## Verification

End-to-end prod record → shadow replay (manual seed path):

```sh
./testing/scripts/e2e-reset-kind.sh
./testing/scripts/e2e-record-replay.sh
```

Pixie egress → Recorder OTLP → Beru (prod outbound must use a `Host` matching `recordAndReplay`):

```sh
MINIKUBE_DRIVER=kvm2 ./testing/scripts/setup-local-pixie.sh
./testing/scripts/e2e-reset-minikube.sh --no-reset
./testing/scripts/start-pixie-stream-bridge.sh
./testing/scripts/e2e-pixie-egress-record-test.sh
```

Ultimate hybrid (RabbitMQ ingress + Mongo OTLP + HTTP record/replay + RMQ Firehose egress) on Minikube:

```sh
USE_PIXIE=1 ./testing/scripts/e2e-python-hybrid-test.sh
```

See [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) (Phase 4a.2 — prod egress auto-record).

Manual seeding (without Recorder) remains available via `POST /v1/seed_mock` — useful for tests and one-off mocks. See [pipeline/beru/README.md](../beru/README.md).

---

## Related reading

- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — prod HTTP auto-record vs AMQP egress diff
- [pipeline/siphon/](../siphon/) — OTLP ingress receiver (separate from Recorder egress path)
- [pipeline/monarch/DEPLOYMENT.md](../monarch/DEPLOYMENT.md) — `spec.recordAndReplay`, `PixieStreamRule`
- [pipeline/beru/README.md](../beru/README.md) — mock store, Envoy egress replay, `/v1/record_egress`
