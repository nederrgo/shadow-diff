# Verification guide — Monarch, Beru & Igris

Step-by-step commands to confirm Monarch (shadow Deployments + Envoy sidecars), Beru (gRPC `ReportTraffic` / ext_proc), and Igris (HTTP multicaster) work on Ubuntu with a reachable cluster or locally.

For operator install details, see [monarch/DEPLOYMENT.md](pipeline/monarch/DEPLOYMENT.md).

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
export IGRIS_IMG=igris-http:dev
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
cd "$REPO/pipeline/monarch"

make docker-build IMG=$MONARCH_IMG

# Kind: load image into the cluster
# kind load docker-image $MONARCH_IMG --name <your-kind-cluster>

# Minikube: build inside Minikube Docker
# eval $(minikube docker-env)
# make docker-build IMG=$MONARCH_IMG

make install
make deploy IMG=$MONARCH_IMG
kubectl set env deployment/monarch-controller-manager -n monarch-system MONARCH_MODE=dev
```

Verify the operator:

```bash
kubectl get pods -n monarch-system
kubectl wait -n monarch-system deployment/monarch-controller-manager --for=condition=Available --timeout=120s
kubectl get crd shadowtests.engine.shadow-diff.io
```

### Option B — Controller on your machine (no operator pod)

```bash
cd "$REPO/pipeline/monarch"
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

Igris uses a **core engine** plus **HTTP add-on** (MVP). It returns **202 Accepted** immediately, clones requests to three shadow targets, injects W3C **`traceparent`** and **`x-shadow-trace-id`** on all outbound calls, and logs `multicast complete` for Beru correlation.

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

- HTTP **202** with `x-shadow-trace-id: trace-igris-1` and `traceparent: 00-...` in the response.
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

Ensure `ShadowTest` spec includes Beru address and optional inputs (helper images resolve via `MONARCH_MODE=dev` — no `spec.igris` required):

```yaml
spec:
  servicePort: 8888
  applicationPort: 8080
  inputs:
    - port: 8888
      driver: http_request
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

## Phase 3b — Pixie eBPF → Siphon OTLP (ingress capture)

Siphon receives **OTLP gRPC** on `:4317` (logs and traces) from Pixie `px.export`, parses HTTP fields, and POSTs to **igris-http** in the shadow namespace. Monarch writes a `PixieStreamRule` per ShadowTest; **pixie-stream-bridge** renders PxL and runs `px.export` to `spec.otelEndpoint`.

### Pixie local sandbox (Minikube kvm2)

Pixie requires a **VM Minikube driver** (`kvm2` or `virtualbox`). Kind, `driver=none`, and `driver=docker` are not supported by Pixie PEM.

```bash
# 1. Pixie Cloud account (free): px auth login or export PIXIE_API_KEY
MINIKUBE_DRIVER=kvm2 ./testing/scripts/setup-local-pixie.sh

# 2. Monarch E2E stack (deploy ShadowTest + prod echo)
./testing/scripts/e2e-reset-minikube.sh --no-reset

# 3. Confirm Vizier + PixieStreamRule
kubectl get pods -n pl -l name=vizier-pem
kubectl get pixiestreamrule -A
```

### Curl production Service (out-of-band tap)

Traffic hits **prod** `my-prod-app` Service; Pixie eBPF exports to shadow **Siphon** separately:

```bash
TRACE_ID="pixie-$(date +%s)"
SHADOW_NS=$(kubectl get shadowtest my-app-shadow -n default -o jsonpath='{.status.shadowNamespace}')

kubectl run curl-prod --rm -i --restart=Never --image=curlimages/curl -- \
  curl -sf -H "x-shadow-trace-id: ${TRACE_ID}" \
  "http://my-prod-app.default.svc.cluster.local:80/?probe=${TRACE_ID}"

kubectl logs -n "$SHADOW_NS" deploy/my-app-shadow-igris --tail=50 | grep "$TRACE_ID"
```

Full automated path:

```bash
MINIKUBE_DRIVER=kvm2 ./testing/scripts/e2e-reset-minikube.sh --setup-pixie --run-otlp-ingress-test
```

### One-shot Kind reset (non-Pixie HTTP tests)

```bash
./testing/scripts/e2e-reset-kind.sh --run-test
```

### One-shot Minikube reset

```bash
# Pixie OTLP ingress (kvm2 VM — required for Pixie PEM)
MINIKUBE_DRIVER=kvm2 ./testing/scripts/e2e-reset-minikube.sh --setup-pixie --run-otlp-ingress-test
```

### Build Siphon locally

```bash
cd "$REPO/pipeline/siphon"
make build
make docker-build SIPHON_IMG=siphon:dev
minikube image load siphon:dev   # if using Minikube
```

---

## 9. Cleanup (optional)

```bash
kubectl delete -k "$REPO/pipeline/siphon/deploy/"
```

```bash
kubectl delete shadowtest my-app-shadow -n default
kubectl delete deployment my-prod-app -n default
kubectl delete -f "$REPO/pipeline/beru/deploy/"

cd "$REPO/pipeline/monarch"
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

When `spec.recordAndReplay` is set, Monarch injects `HTTP_PROXY=http://127.0.0.1:15001` into shadow app containers and configures an egress Envoy listener with **ext_proc** to Beru. Beru hashes outbound requests (JSON compacted, optional path stripping) and returns a seeded mock or **HTTP 599** on miss.

### Prerequisites

- Beru deployed with HTTP port **8080** (`beru/deploy/` includes `BERU_HTTP_ADDR=:8080`)
- `ShadowTest` includes `recordAndReplay` (see [`testing/scripts/manifests/e2e-shadowtest.yaml`](testing/scripts/manifests/e2e-shadowtest.yaml))

### Automated Kind E2E

After [`testing/scripts/e2e-reset-kind.sh`](testing/scripts/e2e-reset-kind.sh) deploys the stack:

```bash
./testing/scripts/e2e-egress-test.sh
# or: ./testing/scripts/e2e-reset-kind.sh --run-egress-test
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

## Phase 4a.2 — Egress recorder (auto-seed from prod)

Siphon captures prod outbound HTTP (cleartext), pairs request/response on each TCP connection, and POSTs to Beru `POST /v1/record_egress`. Beru hashes the request and stores the response in `MockStore` — no manual `seed_mock` required.

### Automated Kind E2E

```bash
./testing/scripts/e2e-record-replay.sh
# or: ./testing/scripts/e2e-reset-kind.sh --run-record-replay
```

The script:

1. POSTs from the **prod** pod directly to `http://httpbin.org/post` (no HTTP_PROXY)
2. Polls shadow egress via Envoy proxy with the same body
3. Expects HTTP **200** (auto-recorded response, not 599)

### Manual record flow

```bash
# Trigger prod outbound (direct, not proxied)
kubectl exec -n default deploy/my-prod-app -- \
  curl -sS -X POST http://httpbin.org/post \
    -H 'Content-Type: application/json' \
    -d '{"e2e_record":1}'

# Replay from shadow (via HTTP_PROXY)
export SHADOW_NS=$(kubectl get shadowtest my-app-shadow -n default -o jsonpath='{.status.shadowNamespace}')
kubectl exec -n "$SHADOW_NS" deploy/my-app-shadow-control-a -c app -- \
  curl -s -x http://127.0.0.1:15001 http://httpbin.org/post \
    -H 'Content-Type: application/json' -d '{"e2e_record":1}'
```

Watch Siphon for `egress forwarder: recorded` and Beru for incoming `/v1/record_egress`.

### Unit tests (Sprint 4a.2)

```bash
make -C siphon test
# or:
cd siphon && go test ./internal/capture/... ./internal/egress/... ./internal/config/... -v -count=1
```

Covers: BPF ingress+egress clauses, `FlushOlderThan` goroutine lifecycle, keep-alive HTTP pairing (two transactions per connection), `ignore_paths` on records.

### Verification checklist (4a.2)

1. Rebuild/load `siphon`, `beru`, `monarch` into Kind (use fresh image tags after code changes).
2. Apply [`testing/scripts/manifests/e2e-shadowtest.yaml`](testing/scripts/manifests/e2e-shadowtest.yaml) with `spec.recordAndReplay`.
3. Prod curl to `httpbin.org/post` → Siphon logs `egress forwarder: recorded …`.
4. `./testing/scripts/e2e-record-replay.sh` → shadow egress **200** without `seed_mock`.
5. Siphon status: `record_and_replay_count>0`, `recorder_host_configured=true` (Monarch POST via hostIP).
6. `go test ./internal/egress/...` — keep-alive parser test passes.

---

## Phase 2a scope (superseded by 2b config)

- Phase 2b Envoy config uses **ext_proc** + **generate_request_id** (no longer admin-only placeholder).
- Monarch does **not** deploy Beru; apply `beru/deploy/` separately.

---

## Phase 5b: RabbitMQ shadow ingress

AMQP-only ShadowTests use **`igris-rabbitmq`** (not HTTP Igris or Siphon ingress). Monarch declares the prod queue once; see [`testing/scripts/manifests/rabbitmq-e2e/README.md`](testing/scripts/manifests/rabbitmq-e2e/README.md).

**W3C parity (with Phase 5_OTel):** `igris-rabbitmq` injects **`traceparent`** and **`x-shadow-trace-id`** on multicast when missing. Resolution: shadow header → parse `traceparent` → generate 32-char hex trace id. E2E publishes both legacy (`x-shadow-trace-id`) and **traceparent-only** messages; `rmq-test-worker` forwards both headers on HTTP ingress/egress. OTel operator on shadow pods may additionally propagate context when AMQP/HTTP are instrumented; set `RMQ_WORKER_MANUAL_TRACE=0` to rely on the agent only.

```bash
make igris-rabbitmq-docker-build IGRIS_RABBITMQ_IMG=igris-rabbitmq:dev
make -C testing/example-apps/rmq-test-worker docker-build RMQ_TEST_WORKER_IMG=rmq-test-worker:dev
./testing/scripts/e2e-reset-kind.sh --no-reset
./testing/scripts/e2e-rabbitmq-test.sh
```

Verify:

- `kubectl get shadowtest rmq-test-shadow -o jsonpath='{.status.amqpQueueName}'` is non-empty
- Prod broker lists queue `shadow-diff-<uid>`
- Beru log contains `No regression for Trace <trace-id>`

---

## Phase 5_OTel: W3C traceparent and OpenTelemetry auto-instrumentation

### Prerequisites

1. **[OpenTelemetry Operator](https://github.com/open-telemetry/opentelemetry-operator)** installed (mutating admission webhook).
2. Monarch reconciles **`Instrumentation/shadow-diff-telemetry`** in each shadow namespace automatically (no manual apply required for Monarch-managed ShadowTests). Without the operator webhook, Monarch’s `instrumentation.opentelemetry.io/inject-*` annotations are **ignored** and pods stay uninstrumented.

Example (adjust exporter settings for your cluster; MVP uses propagation-only env from Monarch):

```yaml
apiVersion: opentelemetry.io/v1alpha1
kind: Instrumentation
metadata:
  name: shadow-default
  namespace: <shadow-namespace-from-ShadowTest.status>
spec:
  exporter:
    endpoint: http://otel-collector.observability.svc:4317
  propagators:
    - tracecontext
  sampler:
    type: parentbased_traceidratio
    argument: "1"
```

3. **Webhook fail-open:** If the OTel webhook is slow or down, pods can be `Running` without `otel-init` / agent containers. E2E scripts call [`testing/scripts/assert-otel-injected.sh`](testing/scripts/assert-otel-injected.sh) before traffic. Set `OTEL_INJECTION_OPTIONAL=1` to skip the hard fail on clusters without the operator.

### Monarch behavior

- `spec.otelInjection.enabled` defaults to **true** (set `false` to opt out).
- `spec.language` overrides image heuristic (`java`, `python`, `nodejs`, `dotnet`, `go`).
- Monarch reconciles `Instrumentation/shadow-diff-telemetry` in the shadow namespace (Beru OTLP exporter, W3C `tracecontext`).
- Shadow app pods get `inject-<language>: shadow-diff-telemetry` (production OTel annotations stripped; no `inject-sdk`).
- App env: Monarch overwrites prod-copied `OTEL_*` vars; dependency-specific exporter settings per mongo/rabbitmq branches.

### Verify injection (before traffic)

```bash
SHADOW_NS=$(kubectl get shadowtest my-app-shadow -o jsonpath='{.status.shadowNamespace}')
kubectl wait -n "$SHADOW_NS" --for=condition=Ready pod -l shadow-diff.io/role=control-a --timeout=180s
./testing/scripts/assert-otel-injected.sh "$SHADOW_NS" control-a
```

### Verify W3C headers (Igris / Beru)

```bash
# traceparent-only ingress (Beru extracts 32-char trace id from middle segment)
TRACE_HEX="$(openssl rand -hex 16)"
curl -i -X POST "http://<igris-host>:8080/" \
  -H "traceparent: 00-${TRACE_HEX}-$(openssl rand -hex 8)-01" \
  -d '{}'
# Expect 202 with both traceparent and x-shadow-trace-id echoing TRACE_HEX
```

Beru ingress ext_proc order: `x-shadow-trace-id` → `traceparent` → `x-request-id`.

### Async context (expected limitation)

Apps that spawn **untracked** goroutines or thread pools without `context.Context` may emit outbound HTTP **without** `traceparent`. Use Beru **sequence-based diffing** when trace correlation is incomplete.

### OTel RabbitMQ egress E2E (zero-touch trace propagation)

Proves W3C `traceparent` propagates across AMQP consume/publish when shadow workers use OpenTelemetry auto-instrumentation (no app-level header copying).

```bash
./testing/scripts/e2e-reset-kind.sh --run-otel-rabbitmq-test
# or after reset:
./testing/scripts/e2e-otel-rabbitmq-test.sh
```

See [`testing/scripts/manifests/rabbitmq-otel-e2e/README.md`](../../testing/scripts/manifests/rabbitmq-otel-e2e/README.md). Expect Beru log: `No egress regression for Trace <hex> (rabbitmq)`.

### HTTP ingress → RabbitMQ egress OTel E2E (igris-http + Firehose)

Proves the same W3C `trace_id` reaches Beru on **both** ingress (Envoy `ext_proc`) and AMQP egress (egress-relay-rabbitmq) when traffic enters via **igris-http** and the app publishes to RabbitMQ with OTel auto-instrumentation only.

```bash
./testing/scripts/e2e-http-otel-rmq-nodejs-test.sh   # Express + amqplib (zero-touch)
./testing/scripts/e2e-http-otel-rmq-python-test.sh # Flask + pika (zero-touch; relay dedup)
```

See [`testing/scripts/manifests/http-otel-rmq-e2e/README.md`](../../testing/scripts/manifests/http-otel-rmq-e2e/README.md). Expect Beru logs:

- `No regression for Trace <hex>` (ingress)
- `No egress regression for Trace <hex> (rabbitmq)` (egress)

Use `--skip-otel-bootstrap` on `e2e-reset-kind.sh` when cert-manager and the OpenTelemetry Operator are already installed.

### Unit tests

```bash
make -C igris test
go test ./internal/trace/... -C beru
cd monarch && go test ./internal/controller/ -run 'TestOtel|TestRenderEnvoy'
```

### Checklist

- [ ] OTel Operator installed; `kubectl get instrumentation shadow-diff-telemetry -n <shadow-ns>` exists after ShadowTest Ready
- [ ] `assert-otel-injected.sh` passes for shadow app pods (not only Pod Ready)
- [ ] Igris 202 includes `traceparent` and `x-shadow-trace-id`
- [ ] Beru correlates ingress when only `traceparent` is sent
- [ ] `spec.otelInjection.enabled: false` removes inject annotations from shadow Deployments

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
- [ ] `x-shadow-trace-id` and `traceparent` on Igris response; Beru correlation (Phase 2b / 5_OTel)
- [ ] OTel injection verified on shadow pods before E2E traffic (Phase 5_OTel — `assert-otel-injected.sh`)
- [ ] Egress mock seeded via `POST /v1/seed_mock` and proxied curl returns mock (Phase 4a.1)
- [ ] Unseeded egress returns HTTP 599 and Beru logs `Egress Regression` (Phase 4a.1)
- [ ] Prod egress auto-recorded by Siphon and shadow replay returns 200 without `seed_mock` (Phase 4a.2 — `./testing/scripts/e2e-record-replay.sh`)
- [ ] `make -C siphon test` passes (BPF egress, FlushOlderThan, keep-alive parser)
- [ ] RabbitMQ E2E: `./testing/scripts/e2e-rabbitmq-test.sh` (Phase 5b — prod queue + igris-rabbitmq multicast)
- [ ] OTel RabbitMQ E2E: `./testing/scripts/e2e-otel-rabbitmq-test.sh` (Phase 5_OTel — W3C traceparent via OTel amqplib injection)
- [ ] HTTP→RMQ OTel E2E (Node): `./testing/scripts/e2e-http-otel-rmq-nodejs-test.sh` (igris-http ingress + Firehose egress, dual Beru correlation)
- [ ] HTTP→RMQ OTel E2E (Python): `./testing/scripts/e2e-http-otel-rmq-python-test.sh` (Flask + pika zero-touch)
- [ ] `make -C igris-rabbitmq test` passes
