# Verification guide — Monarch, Beru & Igris

Step-by-step commands to confirm Monarch (shadow Deployments + Envoy sidecars), Beru (gRPC `ReportTraffic` / ext_proc), and Igris (HTTP multicaster) work on Ubuntu with a reachable cluster or locally.

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
export IGRIS_IMG=igris:dev
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
make test              # Monarch unit tests (forwarded to monarch/)
make igris-test        # Igris unit tests
make test-all          # Monarch + Beru + Igris
```

---

## 8. Phase 3a — Igris modular traffic engine

Igris uses a **core engine** plus **HTTP add-on** (MVP). It returns **202 Accepted** immediately, clones requests to three shadow targets, injects **`x-shadow-trace-id`** on all outbound calls, and logs `multicast complete` for Beru correlation.

**Monarch always deploys Igris** in the shadow namespace. Optional `spec.inputs` defines listener ports (default: `servicePort` → `http`). `ShadowTest` stays **`Progressing`** until Igris `AvailableReplicas > 0`.

### Build and unit tests

```bash
cd "$REPO"
make -C igris test
make -C igris build    # binary: igris/bin/igris
make igris-docker-build IGRIS_IMG=$IGRIS_IMG
```

### Local smoke test (three mock backends)

Terminal 1–3 — simple HTTP servers:

```bash
python3 -m http.server 9001 &
python3 -m http.server 9002 &
python3 -m http.server 9003 &
```

Terminal 4 — Igris:

```bash
export CONTROL_A_URL=http://127.0.0.1:9001
export CONTROL_B_URL=http://127.0.0.1:9002
export CANDIDATE_URL=http://127.0.0.1:9003
make -C igris run
```

Terminal 5 — send traffic:

```bash
curl -i -X POST http://localhost:8080/orders?q=1 \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer secret' \
  -H 'x-shadow-trace-id: trace-igris-1' \
  -d '{"price":10}'
```

Expected:

- HTTP **202** with `x-shadow-trace-id: trace-igris-1` in the response.
- Igris logs include `multicast complete` with `trace_id`, `method`, `path`, and per-target `status_code` (or `error`).
- Mock servers receive POST `/orders?q=1` **without** `Authorization` (redacted).

Invalid config exits immediately (before listening):

```bash
CONTROL_A_URL=ftp://bad make -C igris run   # expect exit 1
```

### Cluster E2E (Monarch-deployed Igris)

Build and load the Igris image (same pattern as Beru):

```bash
make igris-docker-build IGRIS_IMG=$IGRIS_IMG
# kind load docker-image $IGRIS_IMG
```

Ensure `ShadowTest` spec includes Beru address and optional inputs:

```yaml
spec:
  servicePort: 80
  applicationPort: 8081
  inputs:
    - port: 80
      addon: http
  igris:
    image: igris:dev
```

After `status.phase: Ready` (Igris + shadows available):

```bash
export SHADOW_NS=$(kubectl get shadowtest my-app-shadow -n default -o jsonpath='{.status.shadowNamespace}')

kubectl get deploy,svc -n "$SHADOW_NS" | grep igris
kubectl get cm -n "$SHADOW_NS" my-app-shadow-igris-config -o jsonpath='{.data.listeners\.json}'

kubectl port-forward -n "$SHADOW_NS" svc/my-app-shadow-igris 8080:80
```

Send traffic (new terminal):

```bash
curl -i -H 'x-shadow-trace-id: e2e-igris-1' http://localhost:8080/
kubectl logs -n beru-system deployment/beru -f
```

Confirm Beru correlates ingress for trace `e2e-igris-1` across control-a, control-b, and candidate.

### Graceful shutdown

```bash
# With Igris running, send a slow request then SIGTERM
kill -TERM $(pgrep -f 'bin/igris')
```

Expected: process stops accepting new connections, waits for in-flight multicasts to finish (`multicast complete` in logs), then exits.

### Igris environment reference

| Variable | Required | Default | Purpose |
|----------|----------|---------|---------|
| `CONTROL_A_URL` | yes | — | Base URL for control-a (http/https) |
| `CONTROL_B_URL` | yes | — | Base URL for control-b |
| `CANDIDATE_URL` | yes | — | Base URL for candidate |
| `IGRIS_LISTENERS_FILE` | no | `/etc/igris/listeners.json` | Port → add-on map (from ConfigMap) |
| `IGRIS_WORKER_POOL_SIZE` | no | `min(32, 4×CPU)` | Multicast worker pool |

---

## Phase 3b — AF_PACKET Siphon (automatic prod capture)

### Deploy Siphon

```bash
kubectl apply -k "$REPO/siphon/deploy/"
kubectl -n siphon-system rollout status daemonset/siphon-agent --timeout=120s
```

Siphon is deployed as a DaemonSet with `hostNetwork: true` using granular capabilities (`CAP_NET_RAW` and `CAP_NET_ADMIN` under root user `runAsUser: 0`) instead of full privileged access. 

Siphon implements **high-performance kernel-level BPF (libpcap) filtering** dynamically. When configuration is POSTed to Siphon's HTTP control API, the Siphon agent compiles a target packet filter of IPv4 host/port clauses (`tcp and ( (host <IP> and port <Port>) or ... )`) and attaches it directly to the socket via the kernel BPF subsystem. This ensures zero-copy filtering at the kernel level for maximum performance. Userspace processing is then only used for sticky flow sampling (TTL-based map) and relaxed TCP stream assembly before multicasting to Igris.

With `SIPHON_INTERFACE=any`, the agent captures on all active non-loopback interfaces (e.g. `eth0`, `cni0`, `veth*` on Kind).

### ShadowTest with Siphon

Apply a `ShadowTest` as in earlier sections. When `status.phase` is **Ready**, Monarch:

1. Lists **production** pod IPs for `spec.targetDeployment`
2. Pushes merged config to every `siphon-agent` pod (`POST /v1/config`)
3. Sets `status.captureTargets`, `status.siphonPhase`, `status.igrisEndpoint`

Optional spec:

```yaml
spec:
  siphon:
    enabled: true
    sampleRate: 100   # percent of new flows (sticky per 4-tuple)
    image: siphon:latest
```

### Verify capture path

```bash
# Siphon agent status (from any siphon pod IP)
kubectl get pods -n siphon-system -o wide
SIPHON_POD_IP=$(kubectl get pods -n siphon-system -l app.kubernetes.io/name=siphon-agent -o jsonpath='{.items[0].status.podIP}')
curl -s "http://${SIPHON_POD_IP}:8080/v1/status" | jq .

# Hit production (not shadow)
kubectl port-forward -n default svc/my-prod-app 8080:80 &
curl -s http://127.0.0.1:8080/

# Igris should show multicasts
export SHADOW_NS=$(kubectl get shadowtest my-app-shadow -n default -o jsonpath='{.status.shadowNamespace}')
kubectl logs -n "$SHADOW_NS" deploy/$(kubectl get deploy -n "$SHADOW_NS" -o name | grep igris | sed 's|deployment.apps/||') -f
```

On **Kind**, confirm `/v1/status` lists `interfaces` including `cni0` or a `veth` — not only `eth0`. Siphon logs should show `Reassembled HTTP request` after curling production.

### One-shot Kind reset (recommended)

From the repo root, builds/loads images, deploys Monarch/Beru/Siphon/prod/ShadowTest with **correct ports** (`prod:80`, `Envoy:8888`, `app:80`), and waits for `Ready`:

```bash
./scripts/e2e-reset-kind.sh --run-test
```

Flags: `--skip-build`, `--skip-load`, `--no-reset`, `--run-test`. Then run `./examples/e2e-pipeline-test.sh` anytime.

### Build Siphon locally

```bash
cd "$REPO/siphon"
make build    # requires Linux + gcc (CGO for afpacket)
make docker-build SIPHON_IMG=siphon:dev
kind load docker-image siphon:dev   # if using Kind
kubectl rollout restart daemonset/siphon-agent -n siphon-system   # after kind load
```

---

## 9. Cleanup (optional)

```bash
kubectl delete -k "$REPO/siphon/deploy/"
```

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
| Siphon `Degraded`, empty capture | TC not on CNI iface / no prod traffic | Check `/v1/status`; hit prod Service URL |
| No Igris logs after prod curl | `sampleRate` 0 or wrong pod IPs | `kubectl get shadowtest -o yaml` → `captureTargets` |

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

## Phase 4a.1 — Egress interception & strict replay

When `spec.downstreams` is set, Monarch injects `HTTP_PROXY=http://127.0.0.1:15001` into shadow app containers and configures an egress Envoy listener with **ext_proc** to Beru. Beru hashes outbound requests (JSON compacted, optional path stripping) and returns a seeded mock or **HTTP 599** on miss.

### Prerequisites

- Beru deployed with HTTP port **8080** (`beru/deploy/` includes `BERU_HTTP_ADDR=:8080`)
- `ShadowTest` includes `downstreams` (see [`examples/e2e-shadowtest.yaml`](examples/e2e-shadowtest.yaml))

### Automated Kind E2E

After [`scripts/e2e-reset-kind.sh`](scripts/e2e-reset-kind.sh) deploys the stack:

```bash
./examples/e2e-egress-test.sh
# or: ./scripts/e2e-reset-kind.sh --run-egress-test
```

The script verifies `HTTP_PROXY`, Envoy `egress_proxy` config, **599** on miss, `seed_mock`, and **200** mock hit.

### Seed a mock response (manual)

```bash
kubectl port-forward svc/beru 8080:8080 -n beru-system &
curl -s -X POST http://127.0.0.1:8080/v1/seed_mock \
  -H 'Content-Type: application/json' \
  -d '{
    "method": "POST",
    "host": "httpbin.org",
    "path": "/post",
    "body": {"foo": 1},
    "ignore_paths": [],
    "response": {
      "status": 200,
      "headers": {"content-type": "application/json"},
      "body": "{\"mock\":true}"
    }
  }'
```

Note the returned `hash` field.

### Issue proxied egress from a shadow app pod

```bash
export SHADOW_NS=$(kubectl get shadowtest my-app-shadow -n default -o jsonpath='{.status.shadowNamespace}')

kubectl exec -n "$SHADOW_NS" deploy/my-app-shadow-control-a -c app -- \
  curl -s -x http://127.0.0.1:15001 http://httpbin.org/post \
  -H 'Content-Type: application/json' -d '{"foo":1}'
```

Expected: `{"mock":true}` when the mock is seeded.

### Verify 599 on miss

Repeat the curl **without** seeding (or with a different body). Expected: HTTP **599** with body `Egress Regression`.

Watch Beru logs:

```bash
kubectl logs -n beru-system deployment/beru -f | grep 'Egress Regression'
```

### Verify Envoy egress config

```bash
kubectl get cm -n "$SHADOW_NS" my-app-shadow-control-a-envoy -o yaml | \
  grep -E 'egress_proxy|x-shadow-mode|request_body_mode: BUFFERED|httpbin.org'
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
- [ ] `make -C igris test` passes
- [ ] Igris returns 202 and multicasts to three targets (local smoke or cluster port-forwards)
- [ ] `x-shadow-trace-id` present on Igris response and shadow/Beru correlation (Phase 2b)
- [ ] Egress mock seeded via `POST /v1/seed_mock` and proxied curl returns mock (Phase 4a.1)
- [ ] Unseeded egress returns HTTP 599 and Beru logs `Egress Regression` (Phase 4a.1)
