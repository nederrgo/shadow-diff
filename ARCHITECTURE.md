# Shadow-Diff — Architecture

Shadow-Diff is an open-source differential testing framework for Kubernetes. It replays captured or synthetic traffic across **three isolated shadow workloads** (two identical controls plus a candidate) and compares responses to find regressions while filtering non-deterministic noise.

This document describes how **Monarch**, **Igris**, and **Beru** fit together in the monorepo. For Monarch directory layout and development workflow, see [monarch/REPO_OVERVIEW.md](monarch/REPO_OVERVIEW.md).

---

## Monorepo layout

| Path | Module | Role |
|------|--------|------|
| [`monarch/`](monarch/) | `github.com/shadow-diff/monarch` | Kubernetes operator — `ShadowTest` CRD, shadow namespace, Deployments, Envoy sidecar config |
| [`igris/`](igris/) | `github.com/shadow-diff/igris` | HTTP multicaster — L7 fan-out to control-a, control-b, candidate |
| [`beru/`](beru/) | `github.com/shadow-diff/beru` | gRPC differ — ingest, correlation, diff-of-diffs |
| [`siphon/`](siphon/) | `github.com/shadow-diff/siphon` | AF_PACKET capture agent — kernel classic BPF filter, TCP reassembly, HTTP replay to Igris |

Each service is a **separate Go module** with its own `Dockerfile` and `Makefile`. The repo root [`Makefile`](Makefile) delegates builds and tests.

---

## The three-pod strategy

| Role | Image | Purpose |
|------|-------|---------|
| **Control A** | `spec.oldImage` | Baseline (old version) |
| **Control B** | `spec.oldImage` | Identical to A — surfaces dynamic / noisy fields |
| **Candidate** | `spec.newImage` | Version under test |

Monarch materializes these as Deployments in a dedicated shadow namespace. Beru compares their **ingress responses** per trace. Igris can **clone HTTP requests** to all three in parallel when traffic enters at L7.

---

## End-to-end data flow

```mermaid
flowchart TB
  subgraph prod [Production node]
    Cap[Siphon DaemonSet hostNetwork]
    BPF[Classic BPF libpcap filter]
    Cap --> BPF
  end
  subgraph ingress [HTTP ingress - Phase 3a]
    Client[Client or mirror]
    Igris[Igris multicaster]
  end
  subgraph cluster [Kubernetes cluster]
    CR[ShadowTest CR]
    Monarch[Monarch operator]
    SNS[Shadow namespace]
    A[control-a + Envoy]
    B[control-b + Envoy]
    C[candidate + Envoy]
    Beru[Beru gRPC]
  end

  Client --> Igris
  BPF -->|matched TCP frames| Cap
  Cap -->|reassembled streams| Igris
  Igris -->|async clone| A
  Igris -->|async clone| B
  Igris -->|async clone| C
  User[Cluster user] --> CR
  Monarch -->|reconcile| CR
  Monarch --> SNS
  SNS --> A
  SNS --> B
  SNS --> C
  A -->|ext_proc| Beru
  B -->|ext_proc| Beru
  C -->|ext_proc| Beru
```

```mermaid
sequenceDiagram
  participant C as Client
  participant I as Igris
  participant S as Shadow pod
  participant E as Envoy sidecar
  participant B as Beru

  C->>I: HTTP request
  I->>C: 202 Accepted + x-shadow-trace-id
  par multicast
    I->>S: clone to control-a
    I->>S: clone to control-b
    I->>S: clone to candidate
  end
  S->>E: app response
  E->>B: ext_proc / ReportTraffic by trace_id
  B->>B: diff-of-diffs when A, B, C complete
```

**Correlation header:** `x-shadow-trace-id` is set by Igris (or upstream), propagated through Envoy (`generate_request_id` / header mutation in Monarch-rendered config), and used by Beru to match the three ingress responses.

---

## Monarch (control plane)

Monarch is a **Kubebuilder / controller-runtime** operator. It runs as a manager `Deployment` and reconciles `ShadowTest` (`engine.shadow-diff.io/v1alpha1`).

### What it does

- Reads an existing **target Deployment** (`spec.targetDeployment`, `spec.targetNamespace`).
- Creates a **shadow namespace** and three **Deployments**: `<name>-control-a`, `<name>-control-b`, `<name>-candidate`.
- Injects an **Envoy sidecar** per pod with config that includes `ext_proc` to Beru, request ID / `x-shadow-trace-id` handling, and ingress on `spec.applicationPort`.
- Copies **literal `env` from the target’s first container only** (MVP); surfaces limitations in status.
- Ensures **Siphon** DaemonSet (`siphon-system`), lists production pod IPs, and pushes capture config (`POST /v1/config`) using node **hostIP** when Siphon runs with `hostNetwork`.

### What it does not do

- Deploy **Beru** (apply `beru/deploy/` separately). **Igris** is deployed by Monarch into each shadow namespace; **Siphon** manifest is applied separately or via `ensureSiphonDaemonSet`.
- Multicast HTTP traffic (that is **Igris**).
- Run diffs (that is **Beru**).

### Reconcile loop (summary)

```mermaid
sequenceDiagram
  participant API as Kubernetes API
  participant R as ShadowTest reconciler
  participant T as Target Deployment
  participant NS as Shadow namespace

  API->>R: ShadowTest event
  R->>API: Get ShadowTest, finalizer
  R->>API: Get target Deployment
  R->>API: Ensure shadow namespace
  R->>R: Extract literal env
  R->>API: Create/patch control-a, control-b, candidate + Envoy CM
  R->>API: Patch status phase, shadowNamespace
```

| CRD concern | Spec fields |
|-------------|-------------|
| Target | `targetDeployment`, `targetNamespace` |
| Images | `oldImage`, `newImage` |
| Ports | `servicePort`, `applicationPort` |
| Beru | `beruGRPCAddress` (Envoy `ext_proc` cluster) |
| Igris listeners | `inputs[]` (`port`, `driver`); default `[{port: servicePort, driver: http_request}]` |
| Igris overrides | `igris.image`, `igris.replicas`, `igris.resources` |
| Siphon | `siphon.enabled`, `siphon.image`, `siphon.sampleRate` |

**Lifecycle:** A finalizer blocks CR deletion until the shadow namespace is cleaned up.

**Details:** [monarch/REPO_OVERVIEW.md](monarch/REPO_OVERVIEW.md), [monarch/DEPLOYMENT.md](monarch/DEPLOYMENT.md).

---

## Igris (universal traffic hub — driver architecture)

Igris is a **protocol-agnostic hub** with pluggable **input drivers**. The hub routes **atomic** traffic (HTTP) through a worker pool and **streaming** traffic (raw TCP) through per-connection goroutines with fan-out.

```mermaid
flowchart LR
  subgraph hub [Igris Hub]
    Registry[Driver registry]
    Pool[Worker pool - atomic only]
    StreamSem[TCP conn limit]
  end
  HTTPDriver[HTTP_REQUEST driver]
  TCPDriver[TCP_STREAM driver]
  Registry --> HTTPDriver
  Registry --> TCPDriver
  HTTPDriver --> Pool
  Pool --> HTTPShadows[3 shadow HTTP URLs]
  TCPDriver --> StreamSem
  StreamSem --> TCPShadows[3 shadow TCP hosts]
```

### Input drivers

| Driver | Type | Ingress unit | Dispatch |
|--------|------|--------------|----------|
| `http_request` | Atomic | One HTTP request | Worker pool → 3 parallel HTTP clones |
| `tcp_stream` | Streaming | One TCP connection | Goroutine + `io.MultiWriter` to 3 TCP targets |
| `async_message` | — | Reserved | Not implemented |

**HTTP driver:** `ParseMetadata` (trace ID), header redaction, **202 Accepted**, async multicast.

**TCP driver:** No redaction (shadow `NetworkPolicy` isolation); **idle timeout** (`IGRIS_TCP_IDLE_TIMEOUT`, default 5m) closes stale relays; **connection limit** (`IGRIS_MAX_TCP_CONNS`, default 1024).

### Configuration

| Source | Purpose |
|--------|---------|
| ConfigMap `listeners.json` | `[{"port":80,"driver":"http_request"},...]` — Monarch writes from `spec.inputs` |
| `IGRIS_LISTENERS_FILE` | Path to listeners file (default `/etc/igris/listeners.json`) |
| `CONTROL_*_URL` | HTTP multicast bases (`http://…:servicePort`) — always set |
| `CONTROL_*_ADDR` | TCP host bases (no port; Igris appends listener port) — always set |
| `IGRIS_WORKER_POOL_SIZE` | Worker pool size (optional) |
| `IGRIS_MAX_TCP_CONNS` | TCP stream semaphore (optional) |
| `IGRIS_TCP_IDLE_TIMEOUT` | Idle relay timeout (optional) |
| `IGRIS_TCP_DIAL_TIMEOUT` | Outbound TCP dial timeout (optional) |

Legacy `addon: http` in `listeners.json` maps to `http_request`. Standalone default: `[{"port":8080,"driver":"http_request"}]`.

### Shutdown (graceful drain)

On **SIGINT/SIGTERM**:

1. **`StopAccepting`** on all drivers (HTTP `Shutdown`, TCP listener close).
2. **`WaitPendingAtomic`** — drain HTTP multicasts.
3. **`WaitPendingStreams`** — drain TCP relays.
4. Stop worker pool and exit.

`terminationGracePeriodSeconds` on the Igris pod is **35s** (Monarch default).

### Monarch integration

Monarch deploys Igris with **mixed-mode env** (all six `CONTROL_*_URL` + `CONTROL_*_ADDR` vars). `spec.inputs[].driver` is auto-inferred when omitted (`servicePort` and ports 80/443/8080 → `http_request`; else `tcp_stream`). Shadow **Services** expose every port in `spec.inputs` (plus Envoy `ingress` on `servicePort`).

**Code:** [`igris/internal/core/`](igris/internal/core/), [`igris/internal/driver/`](igris/internal/driver/), [`monarch/internal/controller/shadowtest_igris.go`](monarch/internal/controller/shadowtest_igris.go).

---

## Siphon (capture agent — Phase 3b)

Siphon is a **pure Go** node agent (`siphon-system` DaemonSet) that mirrors production TCP traffic into the shadow pipeline. It does **not** load custom eBPF programs into the kernel; it uses **classic BPF** (the same expression language as tcpdump/libpcap), compiled in userspace and attached to an **AF_PACKET** (`TPacket`) socket.

### Why classic BPF, not raw eBPF

| Approach | Shadow-Diff choice |
|----------|-------------------|
| **Raw eBPF** (TC/XDP/cgroup skb programs) | Not used — higher operational complexity, verifier limits, separate build/deploy per kernel |
| **Classic BPF via libpcap** | **Current** — `pcap.CompileBPFFilter` + `gopacket/afpacket` `SetBPF`; portable on typical Linux nodes with `CAP_NET_RAW` |

Filtering happens **in the kernel** (dropped packets never reach userspace). **Sticky sampling**, **TCP reassembly**, and **forwarding to Igris** stay in Go (`gopacket/tcpassembly`).

### Capture pipeline

```mermaid
flowchart LR
  wire[Node interfaces eth0 veth*] --> bpf[Kernel classic BPF]
  bpf --> tpacket[afpacket TPacket]
  tpacket --> sample[Sticky 4-tuple sample]
  sample --> asm[tcpassembly]
  asm --> pool[TCP pool to Igris]
```

1. **Monarch** (or the E2E script) `POST`s target pod **IPv4** + **ports** to `http://<node hostIP>:8080/v1/config` (Siphon uses `hostNetwork: true`).
2. **`BuildBPFFilter`** builds e.g. `tcp and ( (host 10.244.0.21 and port 80) or ... )` — `host` matches **both directions** (client→pod and pod→client) for reassembly.
3. Filter is compiled with **`captureSnapLen`** (8192, shared with `OptFrameSize`) and attached per interface.
4. On Kind without `cni0`, **`SIPHON_INTERFACE=any`** selects `eth0` plus all `veth*` peers carrying pod traffic.
5. Sampled request-path streams are forwarded as raw bytes to **Igris** on the listener port that matches the captured destination port (e.g. **80**).

### Control API

| Endpoint | Purpose |
|----------|---------|
| `POST /v1/config` | `sample_rate`, `targets[]` (`target_ips`, `target_ports`, `igris_host`, `listeners`) |
| `GET /v1/status` | `frames_read`, `packets`, `requests_forwarded`, `interfaces`, `targets_count` |

`ApplyBPFFilter()` hot-updates all open handles when config changes (prod pod IP rollout) without restarting the DaemonSet.

### Deployment

| Setting | Value |
|---------|--------|
| `hostNetwork` | `true` |
| Capabilities | `CAP_NET_RAW`, `CAP_NET_ADMIN` (not full `privileged`) |
| `runAsUser` | `0` |
| `SIPHON_INTERFACE` | `any` (Kind: `eth0` + `veth*`); avoid `cni0` if absent on node |

**Monarch:** `spec.siphon` (`enabled`, `image`, `sampleRate`); pushes global merged config; `status.siphonPhase` / `status.captureTargets`. Config POST uses **`pod.Status.HostIP`** when `hostNetwork` is enabled.

**Code:** [`siphon/internal/capture/`](siphon/internal/capture/) (`bpf.go`, `capture.go`), [`siphon/internal/api/`](siphon/internal/api/), [`monarch/internal/controller/shadowtest_siphon.go`](monarch/internal/controller/shadowtest_siphon.go).

### Port alignment (E2E / http-https-echo)

| Hop | Port | Notes |
|-----|------|--------|
| Production app | **80** | BPF `target_ports`; Siphon → Igris listener **80** |
| Envoy ingress (shadow) | **8888** | Igris multicasts to shadow Services here |
| Shadow app (`applicationPort`) | **80** | Must match app listen port after Monarch copies prod `HTTP_PORT=80` |

See [`examples/e2e-shadowtest.yaml`](examples/e2e-shadowtest.yaml) and [`scripts/e2e-reset-kind.sh`](scripts/e2e-reset-kind.sh).

---

## Beru (differ — Phase 2b)

Beru is a **gRPC** server that receives traffic reports from Envoy sidecars and runs **diff-of-diffs** analysis.

### APIs

| API | Purpose |
|-----|---------|
| **`TrafficReporter.ReportTraffic`** | Manual / direct reports (role, trace_id, payload) |
| **Envoy `ext_proc`** | Observe ingress response headers/body; correlate by `x-shadow-trace-id` and `SHADOW_ROLE` |

### Diff-of-diffs logic

1. **Diff(Control A, Control B)** → noise fields (change on identical builds).
2. **Diff(Control A, Candidate)** → total changes.
3. **Regressions** ≈ total changes minus noise.

The ingest **store** holds pending traces (TTL / max size from env e.g. `BERU_TRACE_TTL`, `BERU_MAX_PENDING_TRACES`) until all three roles report or timeout.

**Code:** [`beru/cmd/beru/main.go`](beru/cmd/beru/main.go), [`beru/internal/envoyextproc/`](beru/internal/envoyextproc/), [`beru/internal/ingest/`](beru/internal/ingest/).

---

## Deployment boundaries

| Component | Typical install |
|-----------|-----------------|
| Monarch | `make -C monarch deploy IMG=...` → `monarch-system` |
| Beru | `kubectl apply -f beru/deploy/` → `beru-system` |
| Igris | Deployed by Monarch into each shadow namespace; image via `spec.igris.image` (default `igris:latest`) |
| Siphon | `kubectl apply -f siphon/deploy/daemonset.yaml` → `siphon-system`; image via `spec.siphon.image` |

ShadowTest spec field **`beruGRPCAddress`** must match the Beru Service DNS name Envoy uses in generated YAML.

**Kind E2E:** [`scripts/e2e-reset-kind.sh`](scripts/e2e-reset-kind.sh) (reset + deploy) and [`examples/e2e-pipeline-test.sh`](examples/e2e-pipeline-test.sh) (traffic test; config via node **hostIP**).

---

## Technology stack

| Layer | Technologies |
|-------|----------------|
| Control plane | Go, Kubebuilder, controller-runtime |
| HTTP multicast | Go, `net/http`, `log/slog` |
| Shadow proxy | Envoy v1.26, `ext_proc`, ConfigMaps from Monarch |
| Analysis | Go, gRPC, protobuf |
| Capture | Go + `gopacket/afpacket`, **classic BPF** (libpcap compile), `tcpassembly`, sticky sampling, TCP forward to Igris |

---

## MVP scope and roadmap

| Area | Current MVP | Not yet / design only |
|------|-------------|------------------------|
| Monarch env copy | First container, literal `env` only | Full `envFrom`, volumes, HPA |
| Traffic capture | Siphon: classic BPF + AF_PACKET → Igris (`siphon-system`, `hostNetwork`) | Raw eBPF programs, TLS decrypt, IPv6 BPF |
| Siphon sampling | Global `sample_rate` + sticky 4-tuple map (TTL cap) | Per-ShadowTest rates in agent |
| Beru storage | In-memory store | Redis / Postgres per design docs |
| Igris | HTTP + TCP drivers, mixed-mode env | `async_message`, metrics |

---

## Related reading

- [README.md](README.md) — quick start and component table
- [VERIFICATION.md](VERIFICATION.md) — Monarch, Beru, and Igris verification steps
- [monarch/REPO_OVERVIEW.md](monarch/REPO_OVERVIEW.md) — Monarch file layout
- [monarch/DEPLOYMENT.md](monarch/DEPLOYMENT.md) — operator install
- [project-files/architacture.md](project-files/architacture.md) — early design notes (partially superseded by this doc)
