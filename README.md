# Shadow-Diff

**Shadow-Diff** is an open-source **differential testing framework for Kubernetes**. It replays production or synthetic traffic against **three isolated shadow workloads** — two identical controls plus a candidate — and compares responses to find regressions while filtering non-deterministic noise.

The core idea is **diff-of-diffs**: diff control-a vs control-b to learn what varies on the same build (noise), then diff control-a vs candidate to surface changes that are not explained by that noise. You declare a single **`ShadowTest`** custom resource; **Monarch** provisions the shadow stack and wires capture, ingress, sidecars, and analysis paths.

This repo is a **Go monorepo** (`go.work` at the root). Each pipeline service is its own module; the root [`Makefile`](Makefile) delegates build, test, and image targets.

---

## Why this project exists

Shipping a new container image to production is risky when behavior is hard to predict: flaky timestamps, ordering, external APIs, and AMQP side effects all obscure real regressions. Traditional staging often runs one environment with synthetic checks that miss prod-shaped traffic.

Shadow-Diff addresses that by:

- **Mirroring prod traffic** (HTTP/TCP via BPF capture, AMQP via broker-native shadow queues) or accepting synthetic drivers
- **Running three roles in parallel** — control-a, control-b (`oldImage`), candidate (`newImage`) — under Envoy sidecars
- **Correlating by trace id** so ingress responses, egress HTTP, and AMQP publishes are compared apples-to-apples
- **Separating noise from signal** with diff-of-diffs and configurable ignore paths
- **Optional egress record/replay** so shadow apps can call downstreams without hitting real prod APIs

For how the layers connect and how data flows end-to-end, start with **[docs/architecture/ARCHITECTURE.md](docs/architecture/ARCHITECTURE.md)**.

---

## Repository layout

```
monarch/                          # repo root (Shadow-Diff monorepo)
├── pipeline/                     # Runtime services — one Go module per component
│   ├── monarch/                  # Control plane — ShadowTest operator (all layers)
│   ├── igrises/                  # L2 ingress hub (igris-http, igris-rabbitmq)
│   ├── siphon/                   # L1 capture agent (DaemonSet, BPF)
│   ├── recorder/                 # L4b prod egress HTTP → Beru mock store
│   ├── egress-relay-rabbitmq/    # L4a shadow AMQP publish → Beru egress diff
│   └── beru/                     # L5 analysis sink — diff, mocks, dashboard
├── docs/
│   ├── architecture/             # System design (start here after this README)
│   └── verification/             # Manual and E2E verification procedures
├── testing/
│   ├── scripts/                  # Kind E2E, manifests, shared shell helpers
│   └── example-apps/             # Sample prod targets, workers, k6 load tests
├── Makefile                      # Delegates to pipeline/*/Makefile targets
└── go.work                       # Go workspace linking pipeline modules
```

| Path | Purpose |
| ---- | ------- |
| [`pipeline/`](pipeline/) | All deployable services and the Monarch operator |
| [`docs/`](docs/) | Architecture, verification, deployment guides |
| [`testing/`](testing/) | E2E scripts, ShadowTest sample manifests, example apps |
| [`.github/workflows/`](.github/workflows/) | CI — unit tests, lint, E2E |

---

## Pipeline services

Each service has its own README with layer role, build commands, and Monarch wiring. **Monarch** orchestrates L0–L5 from a `ShadowTest` CR; **Beru** is installed separately (cluster-wide) and referenced via `spec.beruGRPCAddress`.

| Layer | Service | README |
| ----- | ------- | ------ |
| Control plane | **Monarch** | [pipeline/monarch/README.md](pipeline/monarch/README.md) |
| L1 Capture | **Siphon** | [pipeline/siphon/README.md](pipeline/siphon/README.md) |
| L2 Ingress | **Igris** (HTTP/TCP + AMQP) | [pipeline/igrises/README.md](pipeline/igrises/README.md) |
| L4b Egress record | **Recorder** | [pipeline/recorder/README.md](pipeline/recorder/README.md) |
| L4a AMQP egress diff | **egress-relay-rabbitmq** | [pipeline/egress-relay-rabbitmq/README.md](pipeline/egress-relay-rabbitmq/README.md) |
| L5 Analysis | **Beru** | [pipeline/beru/README.md](pipeline/beru/README.md) |

**Architecture (recommended next read):** [docs/architecture/ARCHITECTURE.md](docs/architecture/ARCHITECTURE.md) — layer stack, HTTP/AMQP paths, egress record/replay, Monarch wiring diagram.

**Deploy Monarch + ShadowTest:** [pipeline/monarch/DEPLOYMENT.md](pipeline/monarch/DEPLOYMENT.md)

**Verify on a cluster or Kind:** [docs/verification/VERIFICATION.md](docs/verification/VERIFICATION.md)

---

## Quick start (local E2E)

With Docker and Kind available:

```sh
./testing/scripts/e2e-reset-kind.sh
```

This bootstraps a test cluster, builds and loads images, deploys Monarch and Beru, applies a sample `ShadowTest`, and waits for `Ready`. See [docs/verification/VERIFICATION.md](docs/verification/VERIFICATION.md) for step-by-step manual verification and individual E2E scripts (egress, RabbitMQ, OTel, dependencies).

Build or test a single service from the repo root:

```sh
make -C pipeline/monarch test
make beru-test
make siphon-docker-build
```

Run `make help` or `make -C pipeline/monarch help` for the full target list.
