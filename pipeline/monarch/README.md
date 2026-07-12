# Monarch

**Monarch** is the **control plane** for Shadow-Diff — it orchestrates **L0 through L5** from a single **`ShadowTest`** custom resource. The Kubebuilder operator reads your production target Deployment, provisions an isolated shadow namespace with three roles (control-a, control-b, candidate), and wires ingress capture, Igris, Siphon, Recorder, AMQP relays, Envoy sidecars, and dependencies.

Monarch does **not** run diffing or store traces (**Beru**, L5) and does **not** install the OpenTelemetry Operator (optional for `spec.otelInjection`). You deploy those separately; Monarch only annotates shadow pods and reconciles `PixieStreamRule` plus cluster DNS targets.

See [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) for layer definitions and data flow.

---

## Role in the pipeline

```
                    ┌─────────────────────────────┐
                    │  ShadowTest CR (any ns)      │
                    └──────────────┬──────────────┘
                                   │
                    ┌──────────────▼──────────────┐
                    │  Monarch reconciler          │
                    │  monarch-system              │
                    └──┬───┬───┬───┬───┬───┬───┬──┘
         L1 PixieStreamRule ◄──┘   │   │   │   │   │   └──► L2 igris-rabbitmq
         L4b Recorder ◄────┘   │   │   │   └──────► L4a egress-relay-rabbitmq
                               │   │   └──────────► L2 igris-http
                               │   └──────────────► L3 shadow Deployments + Envoy
                               └──────────────────► prod AMQP queue bind (L1)
```

| Layer | What Monarch provisions or configures |
| ----- | ------------------------------------- |
| **L1 Capture** | `PixieStreamRule` (ingress `otelEndpoint` when Siphon on; always `recorderOtelEndpoint`; mongo when deps) + shadow `Service/siphon` + `Deployment/siphon` for HTTP ingress; prod RabbitMQ shadow queue + bind (AMQP) |
| **L2 Ingress** | Igris Deployment (HTTP/TCP) **or** igris-rabbitmq (AMQP) |
| **L3 Shadow stack** | Three app Deployments + Envoy sidecars + Services; ephemeral **dependencies** per role |
| **L4a Analysis ingest** | Envoy ConfigMaps → Beru gRPC; egress-relay-rabbitmq for AMQP tests |
| **L4b Egress record/replay** | **Always-on** Shop + Recorder (`:4317` Pixie OTLP → Shop); Envoy `shop_ext_proc` + iptables egress redirect (no `spec.recordAndReplay`) |
| **L5 Beru** | Not deployed — `spec.beruGRPCAddress` only |

Shadow namespace name is deterministic: **`shadow-<crNamespace>-<crName>`** (see `shadowtest_helpers.go`).

---

## ShadowTest CR (overview)

One namespaced **`ShadowTest`** (`engine.shadow-diff.io/v1alpha1`) drives the full stack:

| Field | Purpose |
| ----- | ------- |
| `targetDeployment` / `targetNamespace` | Prod Deployment to mirror (env copied from first container) |
| `oldImage` / `newImage` | control-a & control-b vs candidate container images |
| `servicePort` / `applicationPort` | Igris → Envoy ingress (:8888 typical) → app (:80/:8080) |
| `beruGRPCAddress` | Beru gRPC for Envoy `ext_proc` |
| `inputs[]` | Ingress drivers: `http_request`, `tcp_stream`, `rabbitmq_message` |
| `dependencies[]` | Ephemeral Redis, RabbitMQ, MongoDB, etc. per role + env injection |
| `siphon` | Optional override for HTTP ingress capture; otherwise inferred from HTTP/TCP inputs |
| `shop` / `recorder` / `igris` / `igrisRabbitmq` / `egressRelayRabbitmq` | Optional component image overrides (defaults via `MONARCH_MODE`) |
| `beru` | Optional beru-local image override |

**Removed:** `spec.recordAndReplay` / `recordAndReplayHosts` — Shop+Recorder are always-on.

**`PixieStreamRule`** (created by Monarch, namespaced with the ShadowTest):

| Field | When set |
| ----- | -------- |
| `otelEndpoint` | HTTP ingress Siphon enabled → `siphon.<shadow-ns>:4317` |
| `recorderOtelEndpoint` | Always → `<shadowtest>-recorder.<shadow-ns>:4317` |
| `mongoOtelEndpoint` | Mongo dependency → `beru-local.<shadow-ns>:4317` |
| `targetLabels` / `targetPorts` | Prod target labels; ingress Pixie ports use `applicationPort` |

**Status:** `phase` (Ready / Progressing / Failed), `shadowNamespace`, `captureTargets`, `amqpQueueName`, `siphonPhase`, `igrisRabbitMQPhase`, `message`.

Field-level reference and examples: **[DEPLOYMENT.md](DEPLOYMENT.md)**.

---

## Reconcile flow (summary)

1. Validate inputs and dependencies; ensure target Deployment exists.
2. Create shadow namespace + finalizer.
3. Reconcile **dependencies** (wait until Ready — RabbitMQ brokers enable Firehose via startup probe).
4. **AMQP path:** declare prod shadow queue → igris-rabbitmq → egress-relay-rabbitmq.
5. **HTTP/TCP path:** Igris ConfigMap + Deployment + Service.
6. For each role: Envoy ConfigMap + shadow Deployment (app + sidecar) + Service.
7. **Shop** then **Recorder** (always-on; Recorder `:4317` OTLP).
8. **Pixie capture:** `PixieStreamRule` + shadow-namespace `Service/siphon` + `Deployment/siphon` when HTTP ingress Siphon is on.
9. Patch status **Ready** when all gates pass.

Deletion removes shadow namespace resources, prod AMQP queue (if applicable), and `PixieStreamRule`.

**HTTP capture runtime (outside Monarch):** install Pixie Vizier (`testing/bats/setup/setup-local-pixie.sh`) and run **pixie-stream-bridge** on a host with `px` CLI + Pixie auth. The bridge runs **ingress and egress** `px.export` scripts when the rule exposes the corresponding endpoints. Monarch deploys the Siphon OTLP receiver in the shadow namespace for HTTP ingress.

### RabbitMQ shadow dependencies

When `dependencies[]` includes RabbitMQ (`AMQP_URL` injection), Monarch deploys `rabbitmq-control-a/b/candidate` with the **tracing** and **management** plugins. A startup probe runs `rabbitmqctl trace_on` (Firehose) before the broker is marked Ready. Default resources: **512Mi** memory limit, **500m** CPU limit, **60s** startup probe timeout — tuned for Firehose on resource-constrained clusters (e.g. Minikube).

---

## Layout

```
monarch/
  api/v1alpha1/              ShadowTest + PixieStreamRule CRD types
  cmd/main.go                Operator entrypoint
  config/
    crd/                     ShadowTest + PixieStreamRule CRD manifests
    manager/                 Deployment kustomize
    rbac/                    ClusterRole for reconciler
    samples/                 Example ShadowTest YAML
  internal/controller/       Reconciler (Envoy, Igris, Siphon, RabbitMQ, OTel, …)
  DEPLOYMENT.md              Install guide + CRD field reference
```

---

## Build and deploy

From the repo root:

```sh
make -C pipeline/monarch install          # CRDs (ShadowTest + PixieStreamRule)
make -C pipeline/monarch docker-build IMG=monarch:dev
make -C pipeline/monarch deploy IMG=monarch:dev
kubectl set env deployment/monarch-controller-manager -n monarch-system MONARCH_MODE=dev  # Kind/Minikube E2E: resolve :dev helper images
make -C pipeline/monarch test
```

Local development:

```sh
make -C pipeline/monarch install
make -C pipeline/monarch run              # controller on ~/.kube/config
```

Verify:

```sh
kubectl get pods -n monarch-system
kubectl get crd shadowtests.engine.shadow-diff.io pixiestreamrules.engine.shadow-diff.io
kubectl api-resources | grep shadowtest   # short name: st
kubectl api-resources | grep pixiestreamrule   # short name: psr
```

**Minikube E2E** (Pixie eBPF + OTLP):

```sh
MINIKUBE_DRIVER=kvm2 ./testing/bats/setup/setup-local-pixie.sh
./testing/tools/e2e-reset-minikube.sh
./testing/bats/setup/start-pixie-stream-bridge.sh
./make test-bats-integration      # HTTP ingress
./make test-bats-e2e        # HTTP egress → Recorder
USE_PIXIE=1 ./make test-bats-e2e  # RMQ + Mongo + HTTP + Firehose
```

Recommend **8GB+ Minikube memory** for the hybrid test (six dependency pods + three workers + igris + recorder + egress-relay).

**Kind E2E** (stack without Pixie):

```sh
./testing/tools/e2e-reset-minikube.sh
```

---

## Prerequisites Monarch expects

| Component | Who installs | Monarch's role |
| --------- | ------------ | -------------- |
| **Beru** | You (`pipeline/beru/deploy/`) | Wire `beruGRPCAddress`; Recorder/relay use Beru HTTP |
| **Pixie Vizier** | You (`testing/bats/setup/setup-local-pixie.sh`) | Reconciles `PixieStreamRule` targeting prod labels |
| **pixie-stream-bridge** | You (host process) | Not deployed by Monarch — runs ingress + egress `px.export` |
| **Siphon OTLP receiver** | Monarch (when HTTP ingress Siphon on) | Provisions `Service/siphon` + `Deployment/siphon` (`SIPHON_IGRIS_BASE_URL` → shadow igris-http) |
| **OpenTelemetry Operator** | You (optional) | Set pod annotations; E2E may pre-apply `Instrumentation` CR |
| **Production target** | You | Read-only mirror source |

---

## Related reading

- [DEPLOYMENT.md](DEPLOYMENT.md) — step-by-step install, ShadowTest examples, troubleshooting
- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — layer stack and diagrams
- [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) — E2E verification
- Per-service READMEs: [Beru](../beru/README.md), [Igris](../igrises/README.md), [Siphon](../siphon/README.md), [Recorder](../recorder/README.md), [egress-relay-rabbitmq](../egress-relay-rabbitmq/README.md)
