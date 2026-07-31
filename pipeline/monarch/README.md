# Monarch

**Monarch** is the **control plane** for Shadow-Diff — it orchestrates **L0 through L5** from a single **`ShadowTest`** custom resource. The Kubebuilder operator reads your production target Deployment, provisions an isolated shadow namespace with three roles (control-a, control-b, candidate), and wires Kaisel capture, Igris, Shop, AMQP relays, Envoy sidecars, and dependencies.

Monarch does **not** run diffing or store traces (**Beru** does that). Shadow pods are **Envoy-only** (app + sidecar); apps must already propagate W3C `traceparent`. You install the Kaisel DaemonSet and (optionally) shared Beru separately; Monarch reconciles `KaiselRule` and cluster DNS wiring.

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
         L1 KaiselRule ◄───────┘   │   │   │   │   │   └──► L2 igris-rabbitmq
         L4b Shop ◄─────────────┘   │   │   │   └──────► L4a egress-relay-rabbitmq
                               │   │   └──────────► L2 igris-http
                               │   └──────────────► L3 shadow Deployments + Envoy
                               └──────────────────► prod AMQP queue bind (L1)
```

| Layer | What Monarch provisions or configures |
| ----- | ------------------------------------- |
| **L1 Capture** | `KaiselRule` (HTTP ingress → igris-http, HTTP egress → Shop); prod RabbitMQ shadow queue + bind (AMQP) |
| **L2 Ingress** | Igris Deployment (HTTP/TCP) **or** igris-rabbitmq (AMQP) |
| **L3 Shadow stack** | Three app Deployments + Envoy sidecars + Services; iptables init → Envoy `:10001`; ephemeral **dependencies** per role |
| **L4a Analysis ingest** | Envoy ConfigMaps → Beru gRPC / wire ingest; egress-relay-rabbitmq for AMQP tests |
| **L4b Egress record/replay** | Always-on Shop (seeded by Kaisel); Envoy `shop_ext_proc` |
| **L5 Beru** | Per-shadow **beru-local**, one per ShadowTest |

Shadow namespace name is deterministic: **`shadow-<crNamespace>-<crName>`** (see `shadowtest_helpers.go`).

---

## ShadowTest CR (overview)

One namespaced **`ShadowTest`** (`engine.shadow-diff.io/v1alpha1`) drives the full stack:

| Field | Purpose |
| ----- | ------- |
| `targetDeployment` / `targetNamespace` | Prod Deployment to mirror (env copied from first container) |
| `oldImage` / `newImage` | control-a & control-b vs candidate; `oldImage` defaults from target when unset |
| `servicePort` / `applicationPort` | Envoy ingress → app; both optional (Monarch derives conflict-free values) |
| `beruGRPCTimeout` | Envoy `ext_proc` timeout; the target is always beru-local |
| `inputs[]` | Ingress drivers: `http_request`, `tcp_stream`, `rabbitmq_message` |
| `dependencies[]` | Ephemeral Redis, RabbitMQ, MongoDB, etc. per role + env injection |
| `samplePercentage` | Shared prod sampling gate (1–100, default 100): HTTP → Kaisel; rabbitmq → igris-rabbitmq |
| `shop` / `igris` / `igrisRabbitmq` / `egressRelayRabbitmq` | Optional component image/resource overrides (defaults via `MONARCH_MODE`) |
| `beru` | Optional beru-local image override |

**`KaiselRule`** (HTTP ingress; namespaced with the ShadowTest): target pod IPs, ports, `igrisBaseURL`, `samplePercentage`.

**Status:** `phase` (Ready / Progressing / Failed), `shadowNamespace`, `captureTargets`, `amqpQueueName`, `kaiselPhase`, `igrisEndpoint`, `igrisRabbitMQPhase`, `message`.

Field-level reference and examples: **[DEPLOYMENT.md](DEPLOYMENT.md)**.

---

## Reconcile flow (summary)

1. Validate inputs and dependencies; ensure target Deployment exists.
2. Create shadow namespace + finalizer.
3. Reconcile **dependencies** (wait until Ready — RabbitMQ brokers enable Firehose via startup probe).
4. **AMQP path:** declare prod shadow queue → igris-rabbitmq → egress-relay-rabbitmq.
5. **HTTP/TCP path:** Igris ConfigMap + Deployment + Service.
6. For each role: Envoy ConfigMap + shadow Deployment (app + sidecar + iptables init) + Service.
7. **Shop** (always-on).
8. **HTTP capture:** `KaiselRule` — ingress when HTTP/TCP inputs match target ports, egress always (`egressBaseURL` → Shop).
9. Patch status **Ready** when all gates pass.

Deletion removes shadow namespace resources, prod AMQP queue (if applicable), and `KaiselRule`.

**Capture runtime (outside Monarch):** install the **Kaisel** DaemonSet (`pipeline/kaisel/deploy/`); it handles both HTTP ingress and HTTP egress.

### RabbitMQ shadow dependencies

When `dependencies[]` includes RabbitMQ (`AMQP_URL` injection), Monarch deploys `rabbitmq-control-a/b/candidate` with the **tracing** and **management** plugins. A startup probe runs `rabbitmqctl trace_on` (Firehose) before the broker is marked Ready. Default resources: **512Mi** memory limit, **500m** CPU limit, **60s** startup probe timeout — tuned for Firehose on resource-constrained clusters (e.g. Minikube).

---

## Layout

```
monarch/
  api/v1alpha1/              ShadowTest + KaiselRule CRD types
  cmd/main.go                Operator entrypoint
  config/
    crd/                     ShadowTest + KaiselRule CRD manifests
    manager/                 Deployment kustomize
    rbac/                    ClusterRole for reconciler
    samples/                 Example ShadowTest YAML
  internal/controller/       Reconciler (Envoy, Igris, Kaisel, Shop, RabbitMQ, …)
  DEPLOYMENT.md              Install guide + CRD field reference
```

---

## Build and deploy

From the repo root:

```sh
make -C pipeline/monarch install          # CRDs (ShadowTest + KaiselRule)
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
kubectl get crd shadowtests.engine.shadow-diff.io kaiselrules.engine.shadow-diff.io
kubectl api-resources | grep shadowtest   # short name: st
kubectl api-resources | grep kaiselrule   # short name: kr
```

**Minikube E2E** (Kaisel eBPF):

```sh
./testing/tools/e2e-reset-minikube.sh
make test-bats-integration      # HTTP ingress
make test-bats-e2e              # full hybrid + HTTP egress → Shop
```

Recommend **8GB+ Minikube memory** for the hybrid test (six dependency pods + three workers + igris + egress-relay).

---

## Prerequisites Monarch expects

| Component | Who installs | Monarch's role |
| --------- | ------------ | -------------- |
| **Beru** | Monarch (always) | Provisions **beru-local** per shadow namespace and wires Envoy `ext_proc` / ingest |
| **Kaisel DaemonSet** | You (`kubectl apply -k pipeline/kaisel/deploy/`) | Reconciles `KaiselRule` targeting prod pod IPs |
| **Kaisel DaemonSet** | You (`pipeline/kaisel/deploy/`) | Monarch writes `KaiselRule` (target IPs, `igrisBaseURL`, `samplePercentage`) |
| **Shop** | Monarch (always) | Mock store seeded by Kaisel via `POST /v1/record_egress` |
| **Production target** | You | Read-only mirror source; apps must propagate `traceparent` |

---

## Related reading

- [DEPLOYMENT.md](DEPLOYMENT.md) — step-by-step install, ShadowTest examples, troubleshooting
- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — layer stack and diagrams
- [docs/control-plane/monarch-controller.md](../../docs/control-plane/monarch-controller.md) — Envoy-only shadow injection
- [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) — E2E verification
- Per-service READMEs: [Beru](../beru/README.md), [Igris](../igrises/README.md), [Kaisel](../kaisel/README.md), [Shop](../shop/README.md), [egress-relay-rabbitmq](../egress-relay-rabbitmq/README.md)
