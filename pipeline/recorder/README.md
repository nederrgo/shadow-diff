# Recorder

**Recorder** is the **L4b** prod-ingest service for Shadow-Diff HTTP egress record/replay. It accepts **production outbound HTTP** from Pixie egress OTLP export (primary) or legacy TCP framing, and seeds **Shop**'s mock store. Shadow pods replay responses via Envoy `shop_ext_proc`.

Recorder is **always** deployed by Monarch (no `spec.recordAndReplay` field). It is separate from **egress-relay-rabbitmq** (shadow AMQP Firehose → Beru).

See [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) for how Recorder fits in the full pipeline.

---

## Role in the pipeline

```
Prod app outbound HTTP
    │
    ├── Pixie eBPF egress PxL → pixie-gate px.export → OTLP gRPC :4317 (gzip)  [primary]
    │
    └── (legacy) Siphon TCP relay :8080 — length-prefixed R/S frames
    │
    ▼
Recorder (shadow namespace, always-on)
    │  OTLP span attrs → Shop POST /v1/record_egress (all hosts)
    ▼
Shop mock store
    ▼
Shadow app → Envoy :10001 → shop_ext_proc → recorded response (or 599)
```

| Stage | Component | What happens |
| ----- | --------- | ------------ |
| **Capture** | **Pixie** + **pixie-gate** | Egress PxL on prod-ns `http_events` (see server-side caveat in docs); OTLP to Recorder |
| **Parse + store** | **Recorder** | Maps OTLP attrs → payload; posts **all** spans to Shop |
| **Replay** | **Envoy** + **Shop** | Shadow egress `:10001` → Shop gRPC mock lookup |

**Ingress diff** (Igris → three shadows → Beru) and **AMQP egress diff** (egress-relay-rabbitmq) do not involve Recorder. Recorder only supports **HTTP egress recording** from prod.

---

## How it works

### 1. Pixie OTLP egress (primary)

**pixie-gate** runs a separate egress PxL script when `PixieStreamRule.spec.recorderOtelEndpoint` is set. Monarch points this at:

```
<shadowtest>-recorder.<shadow-namespace>.svc.cluster.local:4317
```

Recorder listens for **gzip-compressed OTLP gRPC** traces and maps span attributes to `beru.RecordPayload`:

| Span attribute | Record field |
| -------------- | ------------ |
| `http.host` / `server.address` | Host (normalized, no allowlist) |
| `url.path` / `http.target` | Path |
| `http.request.method` | Method |
| `http.request.body` | Request body |
| `http.response.status_code` | Status |
| `http.response.body` | Response body |

Recorder forwards **every** HTTP span to Shop (no host allowlist / no `recordAndReplay.json`). In-cluster calls are often visible on the **server-side** pod in Pixie; workers should still set a stable logical `Host` for Shop keying. See [/data-plane/egress-record-replay.md](../../docs/data-plane/egress-record-replay.md).

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

### 4. Shop ingest (always-on)

All records are posted asynchronously to Shop:

```
POST {SHOP_HTTP_URL}/v1/record_egress
```

Payload includes method, host, path, request body, response status/headers/body, and optional `ignore_paths` for hash stability. Shop stores the entry in the same mock map used by `POST /v1/seed_mock` and Envoy egress lookup.

---

## Layout

```
recorder/
  cmd/recorder/           main entrypoint (TCP :8080 + OTLP :4317 concurrently)
  internal/
    ingest/               TCP server, framing, session pairing
    parse/                HTTP request/response parser
    shop/                 POST /v1/record_egress client (Shop)
    config/               env loader
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
| `SHOP_HTTP_URL` | Yes | — | Shop HTTP base URL (e.g. `http://shop.<shadow-ns>.svc.cluster.local:8080`) |
| `RECORDER_LISTEN_ADDR` | No | `:8080` | TCP address for legacy Siphon egress relay connections |
| `RECORDER_OTLP_GRPC_ADDR` | No | `:4317` | gRPC OTLP trace receiver (Pixie egress `px.export`) |
| `RECORDER_PAIR_TIMEOUT` | No | `30s` | Drop incomplete request/response pairs after this duration (TCP path) |
| `RECORDER_MAX_FRAME_BYTES` | No | `5242880` (5 MiB) | Max single frame payload from Siphon (TCP path) |

Monarch sets `SHOP_HTTP_URL` and both listen addresses. There is no `recordAndReplay.json` ConfigMap.

---

## Monarch integration

Recorder is **always** deployed into the **shadow namespace**:

| Resource | Name pattern | Purpose |
| -------- | ------------ | ------- |
| Deployment | `<shadowtest-name>-recorder` | Recorder pod |
| Service | `<shadowtest-name>-recorder` | Legacy TCP `:8080`; Pixie OTLP `:4317` |

`PixieStreamRule.recorderOtelEndpoint` is always set to the shadow Recorder Service `:4317`.

Optional: `spec.recorder.image` overrides the container image (default via `MONARCH_MODE`).

Hybrid fixtures: `testing/bats/fixtures/e2e/rabbit-ingress-nodejs/shadowtest.yaml` (no `recordAndReplay` field).

---

## Verification

```sh
MINIKUBE_DRIVER=kvm2 ./testing/bats/setup/setup-local-pixie.sh
SKIP_BUILD=1 SKIP_LOAD=1 make test-bats-e2e
```

See [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) and [/data-plane/egress-record-replay.md](../../docs/data-plane/egress-record-replay.md).

Manual seeding (without Recorder): Shop `POST /v1/seed_mock`.

---

## Related reading

- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — prod HTTP auto-record vs AMQP egress diff
- [/data-plane/egress-record-replay.md](../../docs/data-plane/egress-record-replay.md) — always-on Shop+Recorder; Pixie server-side caveat
- [pipeline/siphon/](../siphon/) — OTLP ingress receiver (separate from Recorder egress path)
- [pipeline/monarch/DEPLOYMENT.md](../monarch/DEPLOYMENT.md) — ShadowTest / PixieStreamRule
- [pipeline/shop/README.md](../shop/README.md) — mock store + Envoy egress replay
