# Monarch — Deployment manual

This guide explains how to install Monarch and use a **`ShadowTest`** custom resource to provision the shadow stack: three shadow Deployments (with Envoy sidecars), optional **Igris** / **igris-rabbitmq**, **Kaisel** capture, **Shop** + egress proxy, and ephemeral **dependencies**.

Monarch does **not** replace your production Deployment. You keep an existing **target** Deployment; Monarch reads it and creates isolated shadow workloads in a dedicated namespace.

**Beru** (diff engine) is deployed separately — Monarch only wires shadow Envoy `ext_proc` to the Beru gRPC address you configure.

---

## What Monarch creates

For each `ShadowTest`, Monarch reconciles resources in two places:

| Location | Resources |
|----------|-----------|
| **Shadow namespace** `shadow-<cr-namespace>-<cr-name>` | `<name>-control-a`, `-control-b`, `-candidate` Deployments + Services (Envoy sidecar + app) |
| Same shadow namespace | **Igris** Deployment + Service (HTTP/TCP ingress) *or* **igris-rabbitmq** (AMQP ingress) |
| Same shadow namespace | **egress-relay-rabbitmq** Deployment (ShadowTests with a RabbitMQ `dependencies[]` entry, or AMQP ingress) |
| Same shadow namespace | Per-role **dependency** Deployments + Services (Redis, RabbitMQ, etc.) |
| **`kaisel-system`** (cluster-wide) | **Kaisel** DaemonSet (eBPF ingress capture) |
| **Production broker** (AMQP only) | Prod shadow queue `shadow-diff-<uid>` bound to your exchange |

Each shadow app pod (when egress is enabled) gets `HTTP_PROXY` / `HTTPS_PROXY` → Envoy egress listener **:15001** → Beru strict replay.

---

## Prerequisites

1. **Kubernetes cluster** (v1.24+ recommended) and `kubectl` configured.
2. **Target Deployment** in the cluster (`spec.targetDeployment` / `spec.targetNamespace`).
3. **Container images** pullable by the cluster:
   - Shadow apps: `oldImage`, `newImage`
   - Helper images (Igris, Shop, AMQP relays): resolved by Monarch — see **Helper image resolution** below. Optional CR overrides (`spec.igris`, etc.) or operator env vars still work.
4. **Beru** deployed (e.g. `kubectl apply -f pipeline/beru/deploy/`) in `beru-system`.
5. **Kaisel DaemonSet** (once per cluster):
   ```bash
   kubectl apply -k pipeline/kaisel/deploy/
   ```

   ```bash
   ```

   Deploy Kaisel separately: `kubectl apply -k pipeline/kaisel/deploy/`.

6. **OpenTelemetry Operator** (optional): required only if you rely on default OTel SDK injection on shadow app pods. Set `spec.otelInjection.enabled: false` to skip.

---

## Helper image resolution

Monarch resolves helper container images at reconcile time:

| Precedence | Source |
|------------|--------|
| 1 | ShadowTest CR override (`spec.igris.image`, `spec.shop.image`, …) |
| 2 | Operator env var (`IGRIS_HTTP_IMAGE`, `SHOP_IMAGE`, …) |
| 3 | Default base + tag from **`MONARCH_MODE`** on the operator Deployment |

| `MONARCH_MODE` | Tag suffix | Example defaults |
|----------------|------------|------------------|
| `dev` or `development` | `:dev` | `igris-http:dev`, `shop:dev` |
| unset / `prod` / `production` | `:latest` | `igris-http:latest`, `shop:latest`, … |

**Minikube E2E:** `./testing/tools/e2e-reset-minikube.sh` sets `MONARCH_MODE=dev` and rollout-restarts the operator after loading images. Use `MONARCH_NO_CACHE=1` when rebuilding Monarch to avoid stale Docker cache under the same tag.

---

## Step 1 — Install the Monarch operator

### Option A — Build and deploy from source

```bash
cd /path/to/repo/pipeline/monarch

export IMG=<registry>/monarch:<tag>
make docker-build docker-push IMG=$IMG

make install    # CRDs
make deploy IMG=$IMG
# Optional for local Kind E2E:
kubectl set env deployment/monarch-controller-manager -n monarch-system MONARCH_MODE=dev
```

The controller runs in **`monarch-system`** as `deployment/monarch-controller-manager`.

From the **repo root** you can also run:

```bash
make -C pipeline/monarch install
make -C pipeline/monarch deploy IMG=$IMG
```

### Option B — Local development

```bash
make -C pipeline/monarch install
make -C pipeline/monarch run    # uses ~/.kube/config
```

### Verify

```bash
kubectl get pods -n monarch-system
kubectl get crd shadowtests.engine.shadow-diff.io
kubectl api-resources | grep shadowtest   # short name: st
```

---

## Step 2 — Deploy Beru

Monarch does not install Beru. Apply the Beru manifest and ensure `spec.beruGRPCAddress` on the ShadowTest matches the Service DNS name:

```bash
kubectl apply -f pipeline/beru/deploy/
kubectl rollout status deployment/beru -n beru-system
```

Default gRPC address: `beru.beru-system.svc.cluster.local:50051`.

---

## Step 3 — Prepare the target Deployment

1. Deploy your application (production) as usual.
2. Note for the ShadowTest spec:
   - **Namespace** → `targetNamespace`
   - **Deployment name** → `targetDeployment`
   - **First container** → source for **inline `env` vars** only (MVP)
   - **Listen port** → `applicationPort` (and/or prod port for Kaisel capture)

If the target is missing, `status.phase` becomes **`Failed`**.

---

## Step 4 — Create a ShadowTest

Apply a namespaced `ShadowTest` CR. Shadow workloads land in **`status.shadowNamespace`**, not the CR’s namespace.

### Minimal HTTP ingress example

```yaml
apiVersion: engine.shadow-diff.io/v1alpha1
kind: ShadowTest
metadata:
  name: my-app-shadow
  namespace: default
spec:
  targetDeployment: my-prod-app
  targetNamespace: default
  oldImage: ghcr.io/org/app:v1
  newImage: ghcr.io/org/app:v2
  servicePort: 8888          # Envoy ingress listener in shadow pods
  applicationPort: 8080      # App container port (Envoy forwards here)
  beruGRPCAddress: beru.beru-system.svc.cluster.local:50051
```

### Full HTTP + Kaisel + egress example

See `testing/bats/manifests/e2e-shadowtest.yaml` — `inputs`, optional ports only (no `igris` / `shop` image blocks when `MONARCH_MODE=dev` is set on the operator).

```bash
kubectl apply -f testing/bats/manifests/e2e-shadowtest.yaml
```

### RabbitMQ (AMQP-only) example

When `inputs[].driver` is `rabbitmq_message`, Monarch skips HTTP Igris and deploys **igris-rabbitmq** + **egress-relay-rabbitmq** . See `testing/bats/manifests/rabbitmq-e2e/shadowtest-rmq.yaml`.

---

## ShadowTest spec reference

### Core (required for all modes)

| Field | Required | Description |
|-------|----------|-------------|
| `targetDeployment` | yes | Production Deployment to mirror (env copy source) |
| `targetNamespace` | yes | Namespace of that Deployment |
| `oldImage` | yes | Image for **control-a** and **control-b** |
| `newImage` | yes | Image for **candidate** |
| `servicePort` | no | TCP port for Envoy **ingress** listener (default **8888**) |
| `applicationPort` | no | App listen port (default **8080** for HTTP/TCP; AMQP-only uses `servicePort+1`) |
| `beruGRPCAddress` | no | Beru ext_proc gRPC `host:port`; default `beru.beru-system.svc.cluster.local:50051` |
| `beruGRPCTimeout` | no | ext_proc timeout (e.g. `2s`) |

### Ingress — `inputs`, `igris`

| Field | Description |
|-------|-------------|
| `inputs[]` | Igris listener ports and drivers. Empty → single HTTP listener on `servicePort`. |
| `inputs[].port` | TCP port Igris binds (omit for `rabbitmq_message`) |
| `inputs[].driver` | `http_request`, `tcp_stream`, or `rabbitmq_message` |
| `inputs[].amqp` | Required for `rabbitmq_message`: `prodUrl`, `exchange`, `routingKey`, `targetDependency` |
| `inputs[].amqp.exchangeType` | `topic` (default), `direct`, `fanout`, `headers` |
| `inputs[].addon` | Deprecated; use `driver` (`http` → `http_request`) |
| `igris` | Optional override for **igris-http** image, replicas, resources (HTTP/TCP path) |
| `igris.image` | Container image (default `igris-http:latest`, or `igris-http:dev` when `MONARCH_MODE=dev`) |
| `igris.replicas` | Default `1` |
| `igris.resources` | CPU/memory requests and limits |

### AMQP ingress — `igrisRabbitmq`, `egressRelayRabbitmq`

Used when any input has `driver: rabbitmq_message`.

| Field | Description |
|-------|-------------|
| `igrisRabbitmq` | Override **igris-rabbitmq** Deployment (prod queue → three shadow brokers) |
| `igrisRabbitmq.image` | Default `igris-rabbitmq:latest` |
| `egressRelayRabbitmq` | Override **egress-relay-rabbitmq** (Firehose → Beru egress API) |
| `egressRelayRabbitmq.image` | Default `egress-relay-rabbitmq:latest` |
| `egressRelayRabbitmq.replicas` | Default `1` |

Monarch declares the prod broker queue **`shadow-diff-<shadowtest-uid>`** and sets `status.amqpQueueName`.

### Capture — Kaisel / sampling

| Field | Description |
|-------|-------------|
| _(implicit)_ | HTTP/TCP inputs that match target ports enable KaiselRule capture |
| `samplePercentage` | Shared prod gate for all input types (`(V*100)<(N*256)`, 1–100; default `100`). HTTP → Kaisel (ingress and egress); `rabbitmq_message` → igris-rabbitmq (`IGRIS_RMQ_SAMPLE_PERCENTAGE`). Empty `traceparent` dropped. RabbitMQ does not use Kaisel. |

Monarch reconciles `KaiselRule` for both HTTP ingress and HTTP egress. **`status.kaiselPhase`**: `Ready` or `Degraded`.

### Egress — Shop (always-on)

| Field | Description |
|-------|-------------|
| `shop` | Optional image override for the always-on Shop |

Envoy `shop_ext_proc` always performs the egress mock lookup.

Monarch always deploys Shop in the shadow namespace. Kaisel seeds it with captured prod egress pairs.

### Ephemeral dependencies — `dependencies`

| Field | Description |
|-------|-------------|
| `dependencies[].name` | Logical id (DNS-safe); used in resource names |
| `dependencies[].image` | Container image (e.g. `redis:7-alpine`) |
| `dependencies[].port` | Service port |
| `dependencies[].envVarInjection` | Env var name injected on each shadow app pod with role-specific `host:port` |

Monarch creates **three** isolated instances per dependency: `<name>-control-a`, `-control-b`, `-candidate`.

For RabbitMQ ingress, one dependency entry (e.g. `rabbitmq`) backs shadow brokers; `inputs[].amqp.targetDependency` must match `dependencies[].name`.

### OpenTelemetry — `otelInjection`

| Field | Description |
|-------|-------------|
| `otelInjection.enabled` | Default **true** when omitted; set `false` to skip OTel annotations/env |
| `otelInjection.language` | Override auto-detect: `java`, `python`, `nodejs`, `dotnet`, `go` |

---

## ShadowTest status reference

| Field | Meaning |
|-------|---------|
| `phase` | `Ready`, `Progressing`, or `Failed` |
| `message` | Human-readable detail (skipped env, ingress summary, Kaisel phase) |
| `shadowNamespace` | e.g. `shadow-default-my-app-shadow` |
| `captureTargets` | Production pod IPs written to KaiselRule |
| `kaiselPhase` | `Ready` or `Degraded` |
| `igrisEndpoint` | Igris DNS:port or AMQP queue summary |
| `amqpQueueName` | Prod broker queue (RabbitMQ ShadowTests) |
| `igrisRabbitMQPhase` | `igris-rabbitmq` readiness (AMQP path) |

```bash
kubectl get shadowtest my-app-shadow -n default -o yaml
kubectl get st -n default -o wide    # short name
```

While progressing, common `status.message` values include:

- `waiting for shadow dependencies`
- `waiting for igris-rabbitmq` / `waiting for egress-relay-rabbitmq`
- `waiting for shadow Deployments` / `waiting for Igris` / `waiting for Shop`

---

## Step 5 — Verify shadow workloads

```bash
SHADOW_NS=$(kubectl get shadowtest my-app-shadow -n default -o jsonpath='{.status.shadowNamespace}')

kubectl get deploy,pods,svc -n "$SHADOW_NS"
kubectl get ds -n kaisel-system    # Kaisel DaemonSet
kubectl logs -n monarch-system deployment/monarch-controller-manager -c manager -f
```

Expected Deployments in the shadow namespace (varies by spec):

| Deployment | When |
|------------|------|
| `<name>-control-a`, `-control-b`, `-candidate` | Always |
| `<name>-igris` | HTTP/TCP `inputs` (not AMQP-only) |
| `<name>-igris-rabbitmq` | `rabbitmq_message` input |
| `shop` | Always |
| `<name>-egress-relay-rabbitmq` | RabbitMQ `dependencies[]` (HTTP or AMQP ingress) |
| `<dep>-control-a`, etc. | Each `spec.dependencies` entry |

---

## Step 6 — Change or remove a ShadowTest

**Update:** edit the CR and re-apply; Monarch patches owned resources. Adding or removing `spec.dependencies` creates/prunes per-role dep Deployments and rolls shadow app pods so injected env (e.g. `MONGO_URL`) matches the new spec. Covered by `testing/bats/integration/monarch/deps_update.bats`.

```bash
kubectl apply -f my-shadowtest.yaml
```

**Delete:** finalizer removes the shadow namespace and owned resources; AMQP mode also deletes the prod shadow queue.

```bash
kubectl delete shadowtest my-app-shadow -n default
```

**Uninstall Monarch:**

```bash
make -C pipeline/monarch undeploy
make -C pipeline/monarch uninstall
```

---

## Port model (Kind E2E reference)

Typical layout when prod listens on **:80** and Envoy ingress is **:8888**:

| Component | Port |
|-----------|------|
| Production app | `:80` (Kaisel eBPF capture) |
| Igris listener (replay) | `:80` |
| Envoy ingress (shadow Service) | `:8888` |
| Shadow app (echo) | `:80` (`applicationPort`) |
| Envoy egress proxy | `:15001` (`HTTP_PROXY`) |

See `testing/tools/e2e-reset-minikube.sh` and `testing/bats/manifests/e2e-shadowtest.yaml`.

---

## End-to-end checklist

- [ ] Cluster reachable; target Deployment exists
- [ ] Beru running in `beru-system`
- [ ] `pipeline/kaisel/deploy/` applied (Kaisel DaemonSet)
- [ ] Monarch installed (`make -C pipeline/monarch deploy IMG=...`)
- [ ] ShadowTest applied with correct images, ports, and `beruGRPCAddress`
- [ ] `kubectl get st` shows `phase: Ready` and `shadowNamespace`
- [ ] Three shadow Deployments (+ Igris/Shop as configured) are Ready
- [ ] `status.kaiselPhase: Ready` for HTTP ingress ShadowTests

---

## Known limitations

- **Env vars:** Only **inline `env`** from the target’s **first container** are copied. `envFrom`, `valueFrom`, and volume mounts are not fully mirrored — check `status.message`.
- **Replicas:** Shadow app Deployments are fixed at **1** replica each.
- **Kaisel:** Cluster DaemonSet; per-ShadowTest `KaiselRule` carries target IPs and igris URL.
- **Traffic:** Monarch provisions capture/replay plumbing; you still need production traffic (or test scripts) hitting prod pods or AMQP exchanges.
- **imagePullSecrets:** Not automatically copied to shadow Deployments.

---

## Troubleshooting

| Symptom | Likely cause | What to do |
|---------|----------------|------------|
| `phase: Failed`, target not found | Wrong `targetDeployment` / `targetNamespace` | Fix spec; ensure Deployment exists |
| `waiting for egress-relay-rabbitmq` | Image not loaded (Kind) | Build/load `egress-relay-rabbitmq:dev`; ensure `MONARCH_MODE=dev` on operator |
| Stale `monarch:dev` (Docker cache) after controller changes | Pod still on old image digest | `docker build --no-cache` + `kubectl rollout restart` manager |
| `kaiselPhase: Degraded` | KaiselRule reconcile failed | Check Monarch logs; `kubectl get kaiselrule` |
| Pods `ImagePullBackOff` | Missing image in cluster/registry | Fix image tags; `kind load docker-image ...` for local dev |
| Shadow apps without OTel sidecar | Webhook fail-open or `otelInjection.enabled: false` | Install OTel operator or disable injection explicitly |
| CR stuck deleting | Finalizer cleaning namespace / AMQP queue | Wait; `kubectl describe shadowtest` |
| Unsupported driver errors | Old Monarch controller image | Rebuild/redeploy Monarch; restart manager Deployment |

---

## Related docs

- [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) — step-by-step verification (Monarch + Beru + E2E scripts)
- [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md) — system architecture
- [README.md](./README.md) — operator development (Kubebuilder)
- [config/samples/engine_v1alpha1_shadowtest.yaml](./config/samples/engine_v1alpha1_shadowtest.yaml) — sample CR
- [testing/bats/manifests/e2e-shadowtest.yaml](../../testing/bats/manifests/e2e-shadowtest.yaml) — Kind E2E reference manifest
