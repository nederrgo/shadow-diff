# Recorder

**Recorder** is the **L4b — egress record/replay** prod-ingest service for Shadow-Diff. It parses **production outbound HTTP** captured by Siphon (L1) and seeds Beru's **egress mock store**. Shadow pods can then replay prod downstream responses through Envoy's egress proxy without manual `seed_mock` calls.

Recorder is the **prod auto-record** path. It is separate from **egress-relay-rabbitmq**, which observes **shadow** RabbitMQ publishes for AMQP egress diffing.

See [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) for how Recorder fits in the full pipeline.

---

## Role in the pipeline

```
Prod app outbound HTTP
    │
    ▼
Siphon (BPF/TCP reassembly on prod node)
    │  length-prefixed frames: R = request, S = response
    ▼
Recorder (shadow namespace)
    │  pair streams → parse HTTP → filter by downstream host
    ▼
Beru  POST /v1/record_egress
    │  in-memory mock store (keyed by request hash)
    ▼
Shadow app egress (HTTP_PROXY → Envoy :15001)
    └──► Beru returns recorded response (or 599 on miss)
```

| Stage | Component | What happens |
| ----- | --------- | ------------ |
| **Capture** | **Siphon** | Mirrors prod egress TCP bytes; relays framed `R`/`S` chunks to `recorder_host` |
| **Parse + store** | **Recorder** | Reassembles request/response pairs, parses HTTP, posts matching transactions to Beru |
| **Replay** | **Envoy egress sidecar** | Shadow outbound HTTP is hashed and looked up in Beru's mock store |

**Ingress diff** (Igris → three shadows → Beru) and **AMQP egress diff** (egress-relay-rabbitmq) do not involve Recorder. Recorder only supports **HTTP egress recording** from prod.

---

## How it works

### 1. Siphon → Recorder wire format

Each prod egress TCP flow opens a connection to Recorder. Siphon sends **5-byte framed chunks**:

| Byte | Meaning |
| ---- | ------- |
| `R` | Request leg (client → server bytes) |
| `S` | Response leg (server → client bytes) |
| Next 4 bytes | Big-endian payload length |
| Remaining | Raw TCP payload (may split mid-HTTP) |

Recorder buffers both legs on **per-connection pipes** until request and response streams are attached, then starts an HTTP parser goroutine.

### 2. Request/response pairing

For each Siphon TCP connection, `SessionStore`:

1. Accumulates `R` frames into a request pipe and `S` frames into a response pipe.
2. Starts `parse.RunBidirectional` when both legs exist.
3. Reads sequential HTTP request/response pairs from the pipes (supports keep-alive / multiple transactions on one connection).
4. Evicts incomplete pairs after `RECORDER_PAIR_TIMEOUT` (default 30s).

### 3. Downstream filtering

Only transactions whose `Host` matches `spec.downstreams` on the ShadowTest are recorded. Monarch writes the allowlist to `/etc/recorder/downstreams.json`:

```json
[
  { "host": "httpbin.org", "ignore_paths": ["/uuid"] }
]
```

Wildcard hosts (`*.example.com`) are supported. Non-matching hosts are skipped (response discarded, parser continues).

### 4. Beru ingest

Matching pairs are posted asynchronously to:

```
POST {BERU_HTTP_URL}/v1/record_egress
```

Payload includes method, host, path, request body, response status/headers/body, and optional `ignore_paths` for hash stability. Beru stores the entry in the same mock map used by `POST /v1/seed_mock` and Envoy egress lookup.

---

## Layout

```
recorder/
  cmd/recorder/           main entrypoint
  internal/
    ingest/               TCP server, framing, session pairing
    parse/                HTTP request/response parser, host filter
    beru/                 POST /v1/record_egress client
    config/               env + downstreams.json loader
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
| `RECORDER_LISTEN_ADDR` | No | `:8080` | TCP address for Siphon egress relay connections |
| `RECORDER_DOWNSTREAMS_FILE` | No | `/etc/recorder/downstreams.json` | JSON allowlist of downstream hosts |
| `RECORDER_PAIR_TIMEOUT` | No | `30s` | Drop incomplete request/response pairs after this duration |
| `RECORDER_MAX_FRAME_BYTES` | No | `5242880` (5 MiB) | Max single frame payload from Siphon |

Monarch sets `BERU_HTTP_URL`, listen addr, and mounts the downstreams ConfigMap when `spec.downstreams` is non-empty. Siphon receives `recorder_host` (DNS:port of the Recorder Service) via Monarch's `/v1/config` POST.

---

## Monarch integration

Recorder is deployed into the **shadow namespace** when a ShadowTest defines **`spec.downstreams`**:

| Resource | Name pattern | Purpose |
| -------- | ------------ | ------- |
| Deployment | `<shadowtest-name>-recorder` | Recorder pod |
| Service | `<shadowtest-name>-recorder` | Siphon dials this on port 8080 |
| ConfigMap | `<shadowtest-name>-recorder-config` | `downstreams.json` from `spec.downstreams` |

ShadowTest fields:

| Field | Effect |
| ----- | ------ |
| `spec.downstreams[]` | Enables Recorder + Siphon egress relay + Envoy egress proxy on shadow apps |
| `spec.downstreams[].host` | Hostname allowlist for recording |
| `spec.downstreams[].ignoreRequestPaths` | JSON paths excluded from request hash (volatile fields) |
| `spec.recorder.image` | Override container image (default `recorder:latest`) |

Monarch waits for Recorder to be Available before marking the ShadowTest Ready and pushes `recorder_host` into Siphon config.

Example ShadowTest with downstreams: [testing/scripts/manifests/e2e-shadowtest.yaml](../../testing/scripts/manifests/e2e-shadowtest.yaml).

---

## Verification

End-to-end prod record → shadow replay (no manual `seed_mock`):

```sh
./testing/scripts/e2e-reset-kind.sh
./testing/scripts/e2e-record-replay.sh
```

See [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) (Phase 4a.2 — prod egress auto-record).

Manual seeding (without Recorder) remains available via `POST /v1/seed_mock` — useful for tests and one-off mocks. See [pipeline/beru/README.md](../beru/README.md).

---

## Related reading

- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — prod HTTP auto-record vs AMQP egress diff
- [pipeline/siphon/](../siphon/) — BPF capture and egress relay to Recorder
- [pipeline/monarch/DEPLOYMENT.md](../monarch/DEPLOYMENT.md) — `spec.downstreams`, `spec.recorder`
- [pipeline/beru/README.md](../beru/README.md) — mock store, Envoy egress replay, `/v1/record_egress`
