# Verification guide — Monarch & Beru on Kubernetes

Step-by-step commands to confirm Monarch (shadow Deployments + Envoy sidecars) and Beru (gRPC `ReportTraffic`) work on Ubuntu with a reachable cluster.

For operator install details, see [monarch/DEPLOYMENT.md](monarch/DEPLOYMENT.md).

---

## 0. Prerequisites

```bash
# Cluster + kubectl
kubectl cluster-info
kubectl get nodes

# Tools (install if missing)
docker --version
go version
make --version

# Optional: test Beru gRPC from your laptop
sudo apt-get update && sudo apt-get install -y protobuf-compiler grpcurl
```

Set paths for the rest of the session:

```bash
export REPO=/home/projects/monarch   # adjust to your clone path
export MONARCH_IMG=monarch:dev
export BERU_IMG=beru:dev
cd "$REPO"
```

---

## 1. Create a target Deployment

Monarch requires an existing Deployment; it copies **literal `env`** from the target’s **first container** (MVP).

```bash
kubectl create deployment my-prod-app \
  --image=nginx:1.25 \
  -n default \
  --dry-run=client -o yaml | \
  kubectl apply -f -

kubectl set env deployment/my-prod-app DEMO_ENV=hello -n default

kubectl rollout status deployment/my-prod-app -n default
kubectl get deploy my-prod-app -n default
```

---

## 2. Install and run Monarch

### Option A — Operator in the cluster (typical)

```bash
cd "$REPO/monarch"

make docker-build IMG=$MONARCH_IMG

# Kind: load image into the cluster
# kind load docker-image $MONARCH_IMG --name <your-kind-cluster>

# Minikube: build inside Minikube Docker
# eval $(minikube docker-env)
# make docker-build IMG=$MONARCH_IMG

make install
make deploy IMG=$MONARCH_IMG
```

Verify the operator:

```bash
kubectl get pods -n monarch-system
kubectl wait -n monarch-system deployment/monarch-controller-manager --for=condition=Available --timeout=120s
kubectl get crd shadowtests.engine.shadow-diff.io
```

### Option B — Controller on your machine (no operator pod)

```bash
cd "$REPO/monarch"
make install

# In one terminal — keep running
make run
```

---

## 3. Apply a ShadowTest

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: engine.shadow-diff.io/v1alpha1
kind: ShadowTest
metadata:
  name: my-app-shadow
  namespace: default
spec:
  targetDeployment: my-prod-app
  targetNamespace: default
  oldImage: nginx:1.25
  newImage: nginx:1.25-alpine
  servicePort: 80
  applicationPort: 8080
  beruGRPCAddress: beru.beru-system.svc.cluster.local:50051
EOF
```

Wait for reconciliation:

```bash
kubectl get shadowtest my-app-shadow -n default -w
```

Check status:

```bash
kubectl get shadowtest my-app-shadow -n default -o yaml | grep -A5 '^status:'
kubectl describe shadowtest my-app-shadow -n default
```

Expected:

- `status.phase`: `Ready`
- `status.shadowNamespace`: e.g. `shadow-default-my-app-shadow`

If `Failed`, check operator logs:

```bash
kubectl logs -n monarch-system deployment/monarch-controller-manager -c manager --tail=100
```

---

## 4. Verify Monarch (app + Envoy sidecar)

```bash
export SHADOW_NS=$(kubectl get shadowtest my-app-shadow -n default \
  -o jsonpath='{.status.shadowNamespace}')
echo "Shadow namespace: $SHADOW_NS"

kubectl get deploy -n "$SHADOW_NS"
kubectl get pods -n "$SHADOW_NS"

kubectl get pods -n "$SHADOW_NS" \
  -o custom-columns=NAME:.metadata.name,CONTAINERS:.spec.containers[*].name,READY:.status.containerStatuses[*].ready

kubectl get configmap -n "$SHADOW_NS"

kubectl get deploy -n "$SHADOW_NS" -o json | \
  jq -r '.items[] | "\(.metadata.labels["shadow-diff.io/role"]) \(.spec.template.spec.containers[] | select(.name=="envoy-sidecar") | .env[] | select(.name=="SHADOW_ROLE") | .value)"'

kubectl get cm my-app-shadow-control-a-envoy -n "$SHADOW_NS" -o jsonpath='{.data.envoy\.yaml}' | head -5
```

| Check | Expected |
|--------|----------|
| Deployments | `my-app-shadow-control-a`, `-control-b`, `-candidate` |
| Pods | `2/2` Ready |
| Containers | `app` and `envoy-sidecar` |
| ConfigMaps | 3 × `*-envoy` with `envoy.yaml` |
| Sidecar image | `envoyproxy/envoy:v1.26-latest` |

If Envoy fails:

```bash
kubectl describe pod -n "$SHADOW_NS" -l shadow-diff.io/role=control-a
kubectl logs -n "$SHADOW_NS" -l shadow-diff.io/role=control-a -c envoy-sidecar
```

---

## 5. Deploy Beru

```bash
cd "$REPO"

make beru-docker-build BERU_IMG=$BERU_IMG

# Kind: kind load docker-image $BERU_IMG
# Minikube: minikube image load $BERU_IMG

kubectl apply -f beru/deploy/
kubectl set image deployment/beru beru=$BERU_IMG -n beru-system

kubectl rollout status deployment/beru -n beru-system --timeout=120s
kubectl get pods,svc -n beru-system
```

---

## 6. Verify Beru gRPC (`ReportTraffic`)

Port-forward (terminal 1):

```bash
kubectl port-forward svc/beru 50051:50051 -n beru-system
```

Call RPC (terminal 2):

```bash
grpcurl -plaintext \
  -d '{"pod_role":"candidate","trace_id":"test-123"}' \
  localhost:50051 \
  beru.v1.TrafficReporter/ReportTraffic
```

Check logs:

```bash
kubectl logs -n beru-system deployment/beru --tail=20
```

Expected log:

```text
Received report from candidate for ID test-123
```

Other roles:

```bash
grpcurl -plaintext -d '{"pod_role":"control-a","trace_id":"trace-a"}' \
  localhost:50051 beru.v1.TrafficReporter/ReportTraffic

grpcurl -plaintext -d '{"pod_role":"control-b","trace_id":"trace-b"}' \
  localhost:50051 beru.v1.TrafficReporter/ReportTraffic
```

---

## 7. Local sanity (no cluster)

```bash
cd "$REPO"
make test
make test-all
```

---

## 8. Cleanup (optional)

```bash
kubectl delete shadowtest my-app-shadow -n default
kubectl delete deployment my-prod-app -n default
kubectl delete -f "$REPO/beru/deploy/"

cd "$REPO/monarch"
make undeploy
make uninstall
```

---

## Troubleshooting

| Symptom | Likely cause | What to do |
|---------|----------------|------------|
| `phase: Failed`, target not found | Wrong `targetDeployment` / `targetNamespace` | Fix spec; ensure Deployment exists |
| Pods `ImagePullBackOff` | Local image not in cluster | `kind load` / `minikube image load` or use a registry |
| Pods `1/2` not Ready | Envoy sidecar failing | `kubectl logs ... -c envoy-sidecar` |
| `grpcurl` connection refused | Beru not ready or no port-forward | Check `beru-system` pods; re-run port-forward |
| Wrong cluster | Multiple kube contexts | `kubectl config current-context` |

---

## Phase 2b — Diff-of-diffs via ext_proc

After Beru is deployed with the Phase 2b image, Envoy sidecars call Beru’s **ext_proc** API (same gRPC port). Beru correlates INGRESS response bodies by `x-shadow-trace-id` (from Envoy `x-request-id`).

### Send JSON traffic through each shadow pod

Use the same trace ID when hitting all three roles (for manual testing):

```bash
export SHADOW_NS=$(kubectl get shadowtest my-app-shadow -n default -o jsonpath='{.status.shadowNamespace}')
export INGRESS_PORT=80

for ROLE in my-app-shadow-control-a my-app-shadow-control-b my-app-shadow-candidate; do
  kubectl exec -n "$SHADOW_NS" deploy/$ROLE -c envoy-sidecar -- \
    curl -s -H 'x-shadow-trace-id: trace-123' "http://127.0.0.1:${INGRESS_PORT}/" || true
done
```

For JSON diff tests, use an app image that returns JSON bodies (not default nginx HTML).

### Watch Beru logs

```bash
kubectl logs -n beru-system deployment/beru -f
```

Expected messages include:

- `Regression found in Trace trace-123: Field 'price' expected ...`
- `No regression for Trace ...`
- `Timed out waiting for Trace ... missing [candidate]` (if a pod hangs)

### Manual ReportTraffic (optional)

```bash
kubectl port-forward svc/beru 50051:50051 -n beru-system
grpcurl -plaintext -d '{
  "report": {
    "trace_id": "trace-123",
    "role": "control-a",
    "direction": "INGRESS",
    "payload": {
      "content_type": "application/json",
      "body": "eyJwcmljZSI6MTB9"
    }
  }
}' localhost:50051 beru.v1.TrafficReporter/ReportTraffic
```

(`body` is base64 bytes in proto JSON encoding when using grpcurl.)

### Verify Envoy config

```bash
kubectl get cm -n "$SHADOW_NS" my-app-shadow-control-a-envoy -o yaml | grep -E 'generate_request_id|x-shadow-trace-id|ext_proc'
```

---

## Phase 2a scope (superseded by 2b config)

- Phase 2b Envoy config uses **ext_proc** + **generate_request_id** (no longer admin-only placeholder).
- Monarch does **not** deploy Beru; apply `beru/deploy/` separately.

---

## End-to-end checklist

- [ ] `kubectl cluster-info` succeeds
- [ ] Target Deployment `my-prod-app` exists
- [ ] Monarch installed (`make deploy` or `make run`)
- [ ] `ShadowTest` status `Ready` with `shadowNamespace`
- [ ] Three shadow Deployments; pods `2/2` with `app` + `envoy-sidecar`
- [ ] Three Envoy ConfigMaps in shadow namespace
- [ ] Beru pod running in `beru-system`
- [ ] `grpcurl ReportTraffic` returns `{}` and log shows received report
