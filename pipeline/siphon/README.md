# Siphon

**Siphon** is the **L1 — capture** agent for Shadow-Diff on **HTTP/TCP ingress** ShadowTests. It runs on Kubernetes nodes as a **DaemonSet** (co-located with the **NetObserv eBPF agent**), receives packets via a **gRPC Collector** on `127.0.0.1:9990`, performs **TCP reassembly**, and forwards bytes to **Igris (L2)** for ingress replay and optionally to **Recorder (L4b)** for prod egress auto-record.

Siphon does **not** participate in **RabbitMQ ingress** — AMQP capture uses broker-native routing (Monarch binds a prod shadow queue; see [igris-rabbitmq](../igrises/igris-rabbitmq/)). Synthetic HTTP tests can skip Siphon and send traffic directly to Igris.

See [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) for the full pipeline.

---

## Role in the pipeline

```
                    ┌──────────────────────────────────────────────┐
  Prod pod          │  siphon-agent pod (hostNetwork)               │
  traffic    ────►  │  NetObserv eBPF → gRPC Collector :9990 → Siphon │
                    │       │                                       │
                    │       ├─ ingress ──► Igris (L2)               │
                    │       └─ egress ───► Recorder (L4b)           │
                    └──────────────────────────────────────────────┘
```

| Path | Trigger | Destination | Purpose |
| ---- | ------- | ----------- | ------- |
| **Ingress replay** | TCP to prod pod IP:port (configured targets) | `igris_host:port` | Mirror captured request bytes to Igris for 3-way multicast |
| **Egress record** | Outbound TCP from prod pod when `recordAndReplay` + `recorder_host` set | Recorder Service | Framed `R`/`S` bytes → Beru mock store via Recorder |

Siphon is **L4-only on the capture path** — it does not parse HTTP. **Recorder** and **Igris** parse or relay raw TCP streams.

---

## How it works

### 1. Configuration from Monarch

Monarch merges all Ready ShadowTests and **POSTs** JSON to each agent:

```
POST http://<node-hostIP>:8080/v1/config
```

Per-target fields include prod pod IPs, target ports, Igris host, listener drivers (`http_request` / `tcp_stream`), optional `recordAndReplay`, and `recorder_host`. On first valid config, Siphon starts the **gRPC Collector** on `127.0.0.1:9990` and applies **user-space flow filters** from the target list (the co-located NetObserv agent sends `Collector.Send` RPCs with PCAP-framed payloads).

Global **`sample_rate`** (0–100) controls what fraction of **new TCP flows** are forwarded (sticky per 4-tuple).

### 2. Ingress capture (prod → Igris)

For packets destined to a configured **prod IP:port**:

1. TCP reassembly builds the client → pod request stream.
2. Siphon dials **`igris_host:port`** (same port as the prod target).
3. Reassembled request bytes are written to that TCP connection (raw replay).

Return-path traffic (pod → client) is **not** forwarded — Igris only needs the inbound request leg for multicast.

Driver hint (`http_request` vs `tcp_stream`) comes from Monarch's listener config and is used for logging; both paths relay bytes the same way.

### 3. Egress capture (prod → Recorder)

When `spec.recordAndReplay` is set on a ShadowTest, Monarch configures **`recorder_host`** and record-and-replay host allowlists. For outbound flows from a prod pod IP to a non-ingress destination:

1. Request and response legs are piped separately.
2. When both legs exist, Siphon dials Recorder and streams **length-prefixed frames** (`R` = request, `S` = response) — same wire format [Recorder](../recorder/README.md) expects.
3. HTTP `Host` matching against recordAndReplay rules filters which flows are recorded.

### 4. Control API

| Endpoint | Method | Purpose |
| -------- | ------ | ------- |
| `/v1/config` | POST | Apply targets, sample rate, recordAndReplay, recorder host |
| `/v1/status` | GET | Frames read, packets matched, active sessions, forward count |

Agents use **hostNetwork**; reach them via the node's **hostIP** on port **8080** (not ClusterIP).

---

## Layout

```
siphon/
  cmd/siphon/              main entrypoint
  internal/
    api/                   HTTP control plane (/v1/config, /v1/status)
    config/                target maps, capture filter inputs, egress rules
    capture/               gRPC Collector (NetObserv), PCAP record decode, TCP assembler
    assembly/              stream factory — ingress dial Igris, egress pipes
    egress/                session pairing + relay to Recorder
    forward/               connection pools to Igris and Recorder
    session/               flow sampling decisions (sample_rate)
  deploy/
    daemonset.yaml         reference DaemonSet (Monarch normally owns image/config)
    rbac.yaml              cluster bootstrap for siphon-system
```

---

## Build and test

Pure Go build (`CGO_ENABLED=0`).

From the repo root:

```sh
make siphon-build              # → pipeline/siphon/bin/siphon
make siphon-test
make siphon-docker-build SIPHON_IMG=siphon:dev
```

From this directory:

```sh
make build
make test
make docker-build SIPHON_IMG=siphon:dev
```

---

## Configuration

### Process environment

| Variable | Default | Description |
| -------- | ------- | ----------- |
| `SIPHON_API_ADDR` | `:8080` | Control API listen address |
| `SIPHON_PCAP_ADDR` | `127.0.0.1:9990` | gRPC Collector listen address (NetObserv `TARGET_HOST`/`TARGET_PORT`) |
| `SIPHON_SESSION_TTL` | `5m` | Flow sampling session TTL |
| `SIPHON_SESSION_MAX` | `100000` | Max tracked flows |
| `SIPHON_IGRIS_MAX_CONNS` | `512` | Max pooled connections per Igris destination |

### `/v1/config` payload (from Monarch)

| Field | Description |
| ----- | ----------- |
| `sample_rate` | Percentage of new TCP flows to capture (0–100) |
| `targets[].shadowtest` | ShadowTest name (for logging) |
| `targets[].target_ips` | Production pod IPs to watch |
| `targets[].target_ports` | Ports to capture (e.g. prod app `:80`) |
| `targets[].igris_host` | Igris Service cluster DNS or IP |
| `targets[].listeners` | Port → driver map (`http_request`, `tcp_stream`) |
| `targets[].recorder_host` | Recorder Service host:port (egress record) |
| `targets[].recordAndReplay` | Host allowlist for egress recording |
| `targets[].exclude_ips` | IPs never recorded on egress |

---

## Monarch integration

| Resource | Namespace | Notes |
| -------- | --------- | ----- |
| DaemonSet `siphon-agent` | `siphon-system` | **One cluster-wide** agent; `hostNetwork: true`, `NET_RAW` + `NET_ADMIN` |
| ShadowTest `spec.siphon` | — | `enabled`, `image`, `sampleRate` |

Monarch:

1. Resolves prod pod IPs → `status.captureTargets`
2. Merges config from all Ready ShadowTests
3. POSTs to each agent's hostIP:8080
4. Sets **`status.siphonPhase`**: `Ready`, `Degraded`, or `Disabled`

**Bootstrap once per cluster:**

```sh
kubectl apply -f pipeline/siphon/deploy/rbac.yaml
```

Do not manually `kubectl set image` on the DaemonSet — patch **`spec.siphon.image`** on the ShadowTest so Monarch reconciles the image.

RabbitMQ ShadowTests set **`spec.siphon.enabled: false`** — no BPF capture on the AMQP path.

---

## Verification

HTTP ingress + Siphon (full E2E stack):

```sh
./testing/scripts/e2e-reset-kind.sh
./testing/scripts/e2e-pipeline-test.sh
```

Prod egress auto-record (Siphon → Recorder → Beru):

```sh
./testing/scripts/e2e-record-replay.sh
```

Check agent health:

```sh
# hostIP from a siphon-agent pod
kubectl get pods -n siphon-system -l app.kubernetes.io/name=siphon-agent -o wide
curl -s "http://<node-hostIP>:8080/v1/status" | jq .
```

See [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md).

---

## Related reading

- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — L1 capture vs AMQP routing
- [pipeline/igrises/README.md](../igrises/README.md) — L2 ingress hub (Igris)
- [pipeline/recorder/README.md](../recorder/README.md) — L4b prod egress parse → Beru
- [pipeline/monarch/DEPLOYMENT.md](../monarch/DEPLOYMENT.md) — `spec.siphon`, `status.siphonPhase`
