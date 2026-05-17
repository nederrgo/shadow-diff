# Monarch — Deployment manual

This guide explains how to run Monarch on a cluster and use it to provision **shadow Deployments** (and their Pods) from a `ShadowTest` custom resource.

Monarch does **not** replace your production Deployment. You keep an existing **target** Deployment; Monarch reads it and creates three **shadow** Deployments in a dedicated namespace.

## What Monarch creates

For each `ShadowTest`:

| Resource | Description |
|----------|-------------|
| **Shadow namespace** | `shadow-<cr-namespace>-<cr-name>` (DNS-sanitized) |
| **`<name>-control-a`** Deployment | `spec.oldImage`, 1 replica |
| **`<name>-control-b`** Deployment | `spec.oldImage`, 1 replica |
| **`<name>-candidate`** Deployment | `spec.newImage`, 1 replica |

Pods are created by the Kubernetes **Deployment** controller from those specs. Monarch only creates/updates the Deployments and namespace.

---

## Prerequisites

1. **Kubernetes cluster** (v1.24+ recommended) and `kubectl` configured.
2. **Target Deployment** already running in the cluster (the app you want to shadow-test).
3. **Container images** for shadow pods:
   - `oldImage` — used for control-a and control-b
   - `newImage` — used for candidate  
   Images must be pullable from the cluster (registry credentials if private).
4. **Monarch operator** installed (see below) with permission to create namespaces and Deployments.

---

## Step 1 — Install the Monarch operator

### Option A — Build and deploy from source

```bash
cd /path/to/repo/monarch

# Build and push the manager image (use your registry)
export IMG=<registry>/monarch:<tag>
make docker-build docker-push IMG=$IMG

# Install CRD + RBAC + controller Deployment
make install
make deploy IMG=$IMG
```

The controller runs in namespace **`monarch-system`** as `deployment/monarch-controller-manager` (see `config/default/kustomization.yaml` — `namespace: monarch-system`, `namePrefix: monarch-`).

### Option B — Local development (no in-cluster operator)

```bash
make install          # CRDs only
make run              # controller on your machine using ~/.kube/config
```

Use this only for dev; production should use Option A.

### Verify the operator

```bash
kubectl get pods -n monarch-system
kubectl get crd shadowtests.engine.shadow-diff.io
```

---

## Step 2 — Prepare the target Deployment

Monarch **requires** a real Deployment to exist before reconciliation succeeds.

1. Deploy your application as usual, e.g.:

```bash
kubectl create deployment my-prod-app --image=ghcr.io/org/app:v1 -n default
# or apply your own manifest
```

2. Note:
   - **Namespace** → `spec.targetNamespace`
   - **Deployment name** → `spec.targetDeployment`
   - **First container** in the pod template → source for **literal `env` vars** only (MVP)
   - **Container port** → set `spec.servicePort` to the port your app listens on

If the target is missing, `ShadowTest` status becomes **`Failed`** and the controller retries.

---

## Step 3 — Create a ShadowTest

Apply a `ShadowTest` in the **same or any** namespace (the CR is namespaced; shadow workloads go in a separate shadow namespace).

### Minimal example

Save as `my-shadowtest.yaml` and adjust values:

```yaml
apiVersion: engine.shadow-diff.io/v1alpha1
kind: ShadowTest
metadata:
  name: my-app-shadow
  namespace: default          # namespace where the ShadowTest CR lives
spec:
  targetDeployment: my-prod-app
  targetNamespace: default    # where the production Deployment lives
  oldImage: ghcr.io/org/app:v1
  newImage: ghcr.io/org/app:v2
  servicePort: 8080         # Envoy ingress listener port
  applicationPort: 8081     # App container port (must differ from servicePort)
  beruGRPCAddress: beru.beru-system.svc.cluster.local:50051
```

```bash
kubectl apply -f my-shadowtest.yaml
```

### Field reference

| Field | Required | Meaning |
|-------|----------|---------|
| `targetDeployment` | yes | Name of the existing Deployment to mirror (env copy) |
| `targetNamespace` | yes | Namespace of that Deployment |
| `oldImage` | yes | Image for **control-a** and **control-b** |
| `newImage` | yes | Image for **candidate** |
| `servicePort` | yes | Envoy ingress listener port (1–65535) |
| `applicationPort` | no | App listen port; defaults to `servicePort+1` if unset |
| `beruGRPCAddress` | no | Beru ext_proc gRPC `host:port`; default `beru.beru-system.svc.cluster.local:50051` |
| `beruGRPCTimeout` | no | ext_proc timeout (e.g. `2s`) |

### Sample in the repo

```bash
# Edit config/samples/engine_v1alpha1_shadowtest.yaml first (images, target name)
kubectl apply -k config/samples/
```

---

## Step 4 — Verify shadow Deployments and Pods

1. **Status** on the CR:

```bash
kubectl get shadowtest my-app-shadow -n default -o wide
kubectl describe shadowtest my-app-shadow -n default
```

When healthy, expect:

- `status.phase`: `Ready`
- `status.shadowNamespace`: e.g. `shadow-default-my-app-shadow`

2. **Shadow namespace and workloads**:

```bash
SHADOW_NS=$(kubectl get shadowtest my-app-shadow -n default -o jsonpath='{.status.shadowNamespace}')

kubectl get ns "$SHADOW_NS"
kubectl get deploy,pods -n "$SHADOW_NS"
```

You should see three Deployments:

- `my-app-shadow-control-a`
- `my-app-shadow-control-b`
- `my-app-shadow-candidate`

3. **Operator logs** (if something fails):

```bash
kubectl logs -n monarch-system deployment/monarch-controller-manager -c manager -f
```

---

## Step 5 — Change images or settings

Edit the `ShadowTest` and re-apply:

```bash
kubectl apply -f my-shadowtest.yaml
```

Monarch **patches** the shadow Deployments via `CreateOrPatch`; you do not create shadow Deployments manually.

To point at a different production Deployment, update `targetDeployment` / `targetNamespace` and ensure the new target exists.

---

## Step 6 — Remove a shadow test

Delete the CR; the finalizer removes the shadow namespace (and owned resources):

```bash
kubectl delete shadowtest my-app-shadow -n default
```

To uninstall Monarch entirely:

```bash
kubectl delete -k config/samples/   # if you used samples
make undeploy
make uninstall
```

---

## End-to-end checklist

- [ ] Cluster reachable with `kubectl`
- [ ] Target Deployment exists in `targetNamespace`
- [ ] `make install` + `make deploy IMG=...` (or `make run` for dev)
- [ ] `ShadowTest` applied with correct images and `servicePort`
- [ ] `kubectl get shadowtest` shows `Ready` and `shadowNamespace`
- [ ] Three Deployments and Pods in the shadow namespace

---

## MVP limitations (important)

- **Env vars**: Only **inline `env`** from the target’s **first container** are copied. `envFrom`, `valueFrom`, and secrets/configMaps referenced that way are **not** fully mirrored; check `status.message` on the CR.
- **Traffic**: Monarch provisions workloads; it does **not** automatically mirror production traffic to shadow Pods. Wire routing (Service, mesh, gateway) separately if needed.
- **Replicas**: Shadow Deployments are fixed at **1** replica each in the current implementation.

---

## Troubleshooting

| Symptom | Likely cause | What to do |
|---------|----------------|------------|
| `phase: Failed`, target not found | Wrong `targetDeployment` / `targetNamespace` | Fix spec; ensure Deployment exists |
| Pods `ImagePullBackOff` | Bad image name or missing pull secret | Fix `oldImage`/`newImage`; add `imagePullSecrets` to shadow Deployments (not automated today) |
| No shadow namespace | Reconcile not run or operator down | Check `monarch-system` namespace pods; operator logs |
| CR stuck deleting | Finalizer cleaning namespace | Wait; check namespace `shadow-...` is terminating |

---

## Related docs

- [../VERIFICATION.md](../VERIFICATION.md) — step-by-step cluster verification (Monarch + Beru)
- [ARCHITECTURE.md](./ARCHITECTURE.md) — how reconciliation works
- [REPO_OVERVIEW.md](./REPO_OVERVIEW.md) — repository layout
- [config/samples/engine_v1alpha1_shadowtest.yaml](./config/samples/engine_v1alpha1_shadowtest.yaml) — example CR
