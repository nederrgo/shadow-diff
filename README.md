# Shadow-Diff

Monorepo for **differential testing on Kubernetes**: replay traffic across three isolated shadow workloads (two controls + a candidate) and compare responses to find regressions while filtering non-deterministic noise.

**Full system design:** [ARCHITECTURE.md](ARCHITECTURE.md)  
**Step-by-step verification:** [VERIFICATION.md](VERIFICATION.md)

---

## Components

| Directory | Component | Role | Status |
|-----------|-----------|------|--------|
| [`monarch/`](monarch/) | **Monarch** | Kubernetes operator — `ShadowTest` CRD, shadow namespace, Envoy sidecars, Igris + Siphon wiring | **MVP** — ingress replay, egress proxy, Siphon DaemonSet + config push |
| [`igris/`](igris/) | **Igris** | Traffic hub — HTTP/TCP drivers, multicasts to control-a, control-b, candidate | **MVP** — HTTP + TCP drivers |
| [`beru/`](beru/) | **Beru** | Differ + egress mock store — gRPC ingress diff-of-diffs, HTTP egress replay/recording | **MVP** — `ext_proc`, `seed_mock`, `record_egress` |
| [`recorder/`](recorder/) | **Recorder** | Egress HTTP parser — framed TCP from Siphon, `POST` Beru `/v1/record_egress` | **MVP** — Phase 4a.2 |
| [`siphon/`](siphon/) | **Siphon** | Node capture agent — classic BPF, TCP reassembly, ingress forward to Igris, egress relay to Recorder | **MVP** — ingress capture + egress auto-record (Phase 4a.2) |
| [`project-files/`](project-files/) | Docs | Early design notes (partially superseded by ARCHITECTURE.md) | Reference |

---

## Monarch (control plane)

Monarch is a **Kubebuilder / controller-runtime** operator. It reconciles `ShadowTest` (`engine.shadow-diff.io/v1alpha1`) and materializes the shadow stack:

- **Shadow namespace** with three Deployments: `<name>-control-a`, `<name>-control-b`, `<name>-candidate`
- **Envoy sidecars** — ingress `ext_proc` to Beru, optional egress proxy when `spec.downstreams` is set
- **Igris** in the shadow namespace (image/replicas from `spec.igris`)
- **Siphon DaemonSet** in `siphon-system` (image from `spec.siphon`) and **`POST /v1/config`** to each agent using node **hostIP** (Siphon runs with `hostNetwork`)

Monarch deploys **Recorder** (when `spec.downstreams` is set) and pushes a merged Siphon config per Ready ShadowTest: prod pod IPs, Igris listener ports, **`spec.downstreams`**, **`recorder_host`**, and exclude IPs for Igris/Beru/Recorder ClusterIPs.

Monarch does **not** deploy Beru — apply [`beru/deploy/`](beru/deploy/) separately.

See [monarch/DEPLOYMENT.md](monarch/DEPLOYMENT.md) and [monarch/REPO_OVERVIEW.md](monarch/REPO_OVERVIEW.md).

---

## Siphon (capture agent)

Siphon runs as a **DaemonSet** on production nodes (`hostNetwork`, `CAP_NET_RAW`). It uses **classic BPF** (libpcap compile + AF_PACKET) — not custom eBPF programs — to filter TCP for target prod pod IPs and ports.

| Path | What it does |
|------|----------------|
| **Ingress (Phase 3b)** | Reassembles HTTP from prod → forwards to Igris on the matching listener port |
| **Egress (Phase 4a.2)** | Captures prod outbound TCP to `spec.downstreams` flows, relays framed bytes to **Recorder** |

Control API on `:8080`: `POST /v1/config`, `GET /v1/status` (`targets_count`, `downstreams_count`, `recorder_host_configured`).

**Kind E2E:** Monarch owns the DaemonSet image and config. Apply only RBAC bootstrap (`siphon/deploy/rbac.yaml`); do not `kubectl set image` manually — patch `spec.siphon.image` on the ShadowTest (or use [`scripts/e2e-reset-kind.sh`](scripts/e2e-reset-kind.sh), which sets `$SIPHON_IMG`).

---

## Beru (differ + egress mocks)

Beru correlates traffic from the three shadow pods and runs **diff-of-diffs** (Control A vs B = noise; A vs Candidate = regressions).

| Surface | Purpose |
|---------|---------|
| **gRPC `ReportTraffic`** | Direct / manual traffic reports |
| **Envoy `ext_proc` (ingress)** | Observe shadow app responses by `x-shadow-trace-id` |
| **Envoy `ext_proc` (egress)** | Strict replay — hash outbound request, return mock or **HTTP 599** on miss |
| **HTTP `POST /v1/seed_mock`** | Manually seed egress mock responses (Phase 4a.1) |
| **HTTP `POST /v1/record_egress`** | Auto-seed from Siphon prod capture (Phase 4a.2) |

Deploy: `kubectl apply -f beru/deploy/` → `beru-system` (gRPC `:50051`, HTTP `:8080`). ShadowTest `spec.beruGRPCAddress` must match the Beru Service DNS name.

---

## Implementation status (summary)

Aligned with [ARCHITECTURE.md](ARCHITECTURE.md):

| Phase | Feature | Status |
|-------|---------|--------|
| 2b | Ingress diff-of-diffs via Envoy `ext_proc` + Beru gRPC | Done |
| 3a | Igris HTTP multicast to three shadow pods | Done |
| 3b | Siphon ingress capture → Igris | Done |
| 4a.1 | Shadow egress via `HTTP_PROXY` → Envoy → Beru strict replay | Done |
| 4a.2 | Prod egress auto-record (Siphon → Beru) + shadow replay without `seed_mock` | Done |

**Not yet:** raw eBPF programs, TLS decrypt, persistent Beru store, full prod env/volume parity, `async_message` Igris driver.

---

## Quick start

### Build and test (repo root)

```bash
make test-all          # Monarch + Beru + Igris unit tests
make -C siphon test    # Siphon (separate module)
```

### Individual components

```bash
# Monarch operator
make -C monarch test
make -C monarch deploy IMG=<registry>/monarch:<tag>

# Beru
make -C beru test
make beru-docker-build BERU_IMG=<registry>/beru:<tag>

# Igris
make -C igris test
make igris-docker-build IGRIS_IMG=<registry>/igris:<tag>

# Siphon
make siphon-docker-build SIPHON_IMG=<registry>/siphon:<tag>
```

Most targets are forwarded from the root [`Makefile`](Makefile).

### Kind E2E (full stack)

```bash
# Reset cluster, build/load images, deploy Monarch + Beru + ShadowTest, verify Siphon config
./scripts/e2e-reset-kind.sh

# With tests
./scripts/e2e-reset-kind.sh --run-test           # ingress: prod → Siphon → Igris → Beru
./scripts/e2e-reset-kind.sh --run-egress-test    # egress: seed_mock / 599 / 200
./scripts/e2e-reset-kind.sh --run-record-replay  # auto-record prod egress → shadow replay

# Pin image tags (recommended after code changes — avoids Kind layer cache)
SIPHON_IMG=siphon:dev MONARCH_IMG=monarch:dev BERU_IMG=beru:dev \
  ./scripts/e2e-reset-kind.sh --run-record-replay
```

Example ShadowTest: [`examples/e2e-shadowtest.yaml`](examples/e2e-shadowtest.yaml).

---

## Related docs

- [ARCHITECTURE.md](ARCHITECTURE.md) — data flows, Monarch / Igris / Siphon / Beru integration
- [VERIFICATION.md](VERIFICATION.md) — manual and automated verification per phase
- [monarch/README.md](monarch/README.md) — operator development (Kubebuilder scaffold)
- [monarch/DEPLOYMENT.md](monarch/DEPLOYMENT.md) — operator install
