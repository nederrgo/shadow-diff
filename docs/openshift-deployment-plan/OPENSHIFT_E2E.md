# OpenShift / Red Hat OpenShift — E2E deployment guide

This guide deploys Monarch, Beru, and a `ShadowTest` on **Red Hat OpenShift 4.x** and runs the same egress E2E flow as [`testing/scripts/e2e-egress-test.sh`](../testing/scripts/e2e-egress-test.sh).

## Will this work on OpenShift?

| Component | OpenShift compatibility | Notes |
|-----------|-------------------------|-------|
| **Monarch operator** | Yes | Standard Deployment; works with `restricted-v2` SCC |
| **Beru** | Yes | Standard Deployment + ClusterIP Service |
| **Igris + shadow pods (Envoy sidecar)** | Yes | Sidecars run under default restricted SCC |
| **Egress E2E** (`e2e-egress-test.sh`) | **Yes** | Uses `HTTP_PROXY` → Envoy `:15001` → Beru; **does not require Siphon** |
| **Siphon (ingress capture)** | **Requires SCC** | `hostNetwork`, `runAsUser: 0`, `NET_RAW`, `NET_ADMIN` — see [Siphon on OpenShift](#optional-siphon-for-ingress-capture) |

The egress test validates mock seeding and regression detection for outbound HTTP. It mirrors the Kind workflow except you push images to an OpenShift-accessible registry instead of `kind load docker-image`.

---

## Prerequisites

1. **OpenShift 4.12+** cluster and `oc` CLI logged in (`oc login`).
2. Permissions to:
   - Install CRDs and cluster-scoped RBAC (Monarch operator), **or** a cluster-admin has pre-installed them.
   - Create namespaces: `monarch-system`, `beru-system`, and optionally `siphon-system`.
   - Grant SCC to Siphon (cluster-admin only) if you enable ingress capture.
3. **Tools on your workstation:** `oc`, `kubectl`, `docker` or `podman`, `make`, `bash`, `curl`.
4. **Outbound network:** shadow pods must reach your egress downstream host (default `httpbin.org`). If the cluster blocks external egress, pick an allowed host and set `EGRESS_HOST` when running the test.
5. **Registry access:** images must be pullable from worker nodes (OpenShift internal registry or corporate mirror).

---

## Step 1 — Choose namespaces and registry

```bash
export OC_PROJECT="${OC_PROJECT:-shadow-diff-e2e}"   # prod app + ShadowTest CR
export REGISTRY="${REGISTRY:-$(oc registry info --internal | sed 's|https://||')}"

# Image tags (built locally, pushed to OpenShift internal registry)
export MONARCH_IMG="${REGISTRY}/${OC_PROJECT}/monarch:e2e"
export BERU_IMG="${REGISTRY}/${OC_PROJECT}/beru:e2e"
export IGRIS_IMG="${REGISTRY}/${OC_PROJECT}/igris:e2e"
export SIPHON_IMG="${REGISTRY}/${OC_PROJECT}/siphon:e2e"   # only if enabling Siphon

oc new-project "$OC_PROJECT" 2>/dev/null || oc project "$OC_PROJECT"
```

Log in to the internal registry (requires `cluster-admin` or `registry-editor` on the project):

```bash
oc registry login
# or: podman login -u $(oc whoami) -p $(oc whoami -t) "$REGISTRY"
```

---

## Step 2 — Build and push container images

From the repo root:

```bash
cd /path/to/monarch

# Build (uses testing/scripts/lib/docker.sh to avoid WSL credential-helper issues)
make -C pipeline/monarch docker-build IMG="$MONARCH_IMG"
make beru-docker-build BERU_IMG="$BERU_IMG"
make igris-docker-build IGRIS_IMG="$IGRIS_IMG"

# Push to OpenShift internal registry
docker push "$MONARCH_IMG"
docker push "$BERU_IMG"
docker push "$IGRIS_IMG"
```

If your cluster cannot pull from the internal registry across namespaces, create ImageStreams in each target namespace or use a shared corporate registry and update image references accordingly.

---

## Step 3 — Install Monarch operator

```bash
make -C pipeline/monarch install
make -C pipeline/monarch deploy IMG="$MONARCH_IMG"

oc rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s
oc get crd shadowtests.engine.shadow-diff.io
```

Verify:

```bash
oc get pods -n monarch-system
```

---

## Step 4 — Install Beru

```bash
oc apply -f pipeline/beru/deploy/
oc set image deployment/beru beru="$BERU_IMG" -n beru-system
oc rollout status deployment/beru -n beru-system --timeout=120s
```

Beru exposes:

- gRPC: `beru.beru-system.svc.cluster.local:50051`
- HTTP (seed_mock / record_egress): `beru.beru-system.svc.cluster.local:8080`

---

## Step 5 — Deploy production app and ShadowTest

Create the target Deployment Beru/Monarch will shadow. The Kind example uses `http-https-echo` on port 80:

```bash
# Patch namespace in the manifest if not using default
sed "s/namespace: default/namespace: ${OC_PROJECT}/" testing/scripts/manifests/e2e-prod-app.yaml | oc apply -f -
oc rollout status deployment/my-prod-app -n "$OC_PROJECT" --timeout=120s
```

Apply a ShadowTest with **egress recordAndReplay** and **Igris/Envoy** settings. For an **egress-only** first run (no Siphon SCC setup), disable Siphon:

```bash
cat <<EOF | oc apply -f -
apiVersion: engine.shadow-diff.io/v1alpha1
kind: ShadowTest
metadata:
  name: my-app-shadow
  namespace: ${OC_PROJECT}
spec:
  targetDeployment: my-prod-app
  targetNamespace: ${OC_PROJECT}
  oldImage: mendhak/http-https-echo:latest
  newImage: mendhak/http-https-echo:latest
  servicePort: 8888
  applicationPort: 80
  beruGRPCAddress: beru.beru-system.svc.cluster.local:50051
  inputs:
    - port: 80
      driver: http_request
    - port: 8888
      driver: http_request
  igris:
    image: ${IGRIS_IMG}
    replicas: 1
  siphon:
    enabled: false
  recordAndReplay:
    - host: httpbin.org
      ignoreRequestPaths: []
EOF
```

Wait until Monarch reports `Ready`:

```bash
for i in $(seq 1 36); do
  phase=$(oc get shadowtest my-app-shadow -n "$OC_PROJECT" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  ns=$(oc get shadowtest my-app-shadow -n "$OC_PROJECT" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  echo "  phase=$phase shadowNamespace=$ns ($i/36)"
  [[ "$phase" == "Ready" && -n "$ns" ]] && break
  sleep 5
done

export SHADOW_NS=$(oc get shadowtest my-app-shadow -n "$OC_PROJECT" -o jsonpath='{.status.shadowNamespace}')
oc get shadowtest my-app-shadow -n "$OC_PROJECT" -o wide
oc get pods -n "$SHADOW_NS"
```

All three shadow Deployments should show `2/2` (`app` + `envoy-sidecar`). If Envoy CrashLoops, check:

```bash
oc logs -n "$SHADOW_NS" deploy/my-app-shadow-control-a -c envoy-sidecar --tail=30
```

---

## Step 6 — Run the egress E2E test

The script is the same as Kind; point it at your OpenShift namespaces:

```bash
export SHADOWTEST=my-app-shadow
export SHADOWTEST_NS="$OC_PROJECT"
export EGRESS_HOST=httpbin.org          # must match spec.recordAndReplay[0].host
export BERU_HTTP=http://beru.beru-system.svc.cluster.local:8080

chmod +x testing/scripts/e2e-egress-test.sh
./testing/scripts/e2e-egress-test.sh
```

### Expected pass output

1. `HTTP_PROXY=http://127.0.0.1:15001` on the shadow `app` container.
2. Envoy ConfigMap contains `egress_proxy`, `x-shadow-mode: egress`, and your downstream host.
3. Unseeded POST to `httpbin.org/post` via proxy → **HTTP 599** with body containing `Egress Regression`.
4. Beru logs contain `Egress Regression`.
5. After `POST /v1/seed_mock`, matching request → **HTTP 200** with `{"mock":true}`.

### OpenShift-specific notes for the test script

- The script uses `kubectl`; `oc` is a drop-in alias.
- If the app image has no `curl`, the script uses `kubectl debug` with `curlimages/curl`. On OpenShift this requires permission to create ephemeral debug containers (`oc debug` may need `allowPrivilegeEscalation` on the node — if blocked, install curl in the shadow app image or run curl from a Job in the same namespace).
- If external egress is blocked, replace `httpbin.org` with an in-cluster HTTP service and update `spec.recordAndReplay[0].host` + `EGRESS_HOST`.

---

## Optional: Siphon for ingress capture

To replay **production ingress** traffic (prod → Siphon → Igris → shadows), enable Siphon and grant SCC.

### 1. Grant SCC to the Siphon ServiceAccount

Siphon needs `hostNetwork`, root user, and `NET_RAW` / `NET_ADMIN`. On OpenShift this typically requires the **privileged** SCC (cluster-admin):

```bash
oc apply -f siphon/deploy/rbac.yaml
oc adm policy add-scc-to-user privileged \
  system:serviceaccount:siphon-system:siphon-agent
```

Some clusters prefer a custom SCC instead of `privileged`; coordinate with your platform team.

### 2. Enable Siphon in ShadowTest and push image

```bash
make siphon-docker-build SIPHON_IMG="$SIPHON_IMG"
docker push "$SIPHON_IMG"

oc patch shadowtest my-app-shadow -n "$OC_PROJECT" --type=merge -p "{
  \"spec\": {
    \"siphon\": {
      \"enabled\": true,
      \"image\": \"${SIPHON_IMG}\",
      \"sampleRate\": 100
    }
  }
}"
```

### 3. Verify Siphon

```bash
oc rollout status daemonset/siphon-agent -n siphon-system --timeout=180s
SIPHON_IP=$(oc get pods -n siphon-system -l app.kubernetes.io/name=siphon-agent \
  -o jsonpath='{.items[0].status.hostIP}')
curl -s "http://${SIPHON_IP}:8080/v1/status" | head
oc get shadowtest my-app-shadow -n "$OC_PROJECT" -o jsonpath='{.status.siphonPhase}{"\n"}'
```

Then run the ingress pipeline test:

```bash
./testing/scripts/e2e-pipeline-test.sh
```

On OpenShift, set `SHADOWTEST_NS="$OC_PROJECT"` and ensure prod pod IP is listed in Siphon `/v1/status` targets.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Siphon pod `CreateContainerConfigError` / SCC denied | Missing SCC | `oc adm policy add-scc-to-user privileged system:serviceaccount:siphon-system:siphon-agent` |
| `ImagePullBackOff` on custom images | Registry not reachable from namespace | Push to internal registry; `oc set image` with full `registry/.../image:tag` |
| ShadowTest stuck not `Ready` | Target Deployment missing or Igris failing | `oc describe shadowtest ...`; check monarch-controller logs |
| Egress test: HTTP 599 never appears | Envoy sidecar not routing egress | Verify ConfigMap `${SHADOW_DEPLOY}-envoy` and `HTTP_PROXY` env |
| Egress test: cannot reach `httpbin.org` | Cluster egress firewall | Use allowed host or in-cluster mock HTTP server |
| `seed_mock` fails | Beru HTTP not reachable | `oc get svc -n beru-system`; run seed curl from a pod in-cluster |
| Stress / 413 test fails on Igris | Old Igris image | Rebuild, push, restart `my-app-shadow-igris` Deployment |

Monarch controller logs:

```bash
oc logs -n monarch-system deployment/monarch-controller-manager --tail=50
```

Beru logs:

```bash
oc logs -n beru-system deployment/beru --tail=50
```

---

## Quick reference — full egress E2E on OpenShift

```bash
# 1. Project + registry
export OC_PROJECT=shadow-diff-e2e
export REGISTRY=$(oc registry info --internal | sed 's|https://||')
oc new-project "$OC_PROJECT" || oc project "$OC_PROJECT"
oc registry login

# 2. Build + push
export MONARCH_IMG=$REGISTRY/$OC_PROJECT/monarch:e2e
export BERU_IMG=$REGISTRY/$OC_PROJECT/beru:e2e
export IGRIS_IMG=$REGISTRY/$OC_PROJECT/igris:e2e
make -C pipeline/monarch docker-build IMG=$MONARCH_IMG
make beru-docker-build BERU_IMG=$BERU_IMG
make igris-docker-build IGRIS_IMG=$IGRIS_IMG
docker push $MONARCH_IMG $BERU_IMG $IGRIS_IMG

# 3. Install stack
make -C pipeline/monarch install && make -C pipeline/monarch deploy IMG=$MONARCH_IMG
oc apply -f pipeline/beru/deploy/ && oc set image deployment/beru beru=$BERU_IMG -n beru-system

# 4. Prod + ShadowTest (siphon disabled for egress-only)
sed "s/namespace: default/namespace: $OC_PROJECT/" testing/scripts/manifests/e2e-prod-app.yaml | oc apply -f -
# Apply ShadowTest manifest from Step 5 above (with your IGRIS_IMG)

# 5. Test
export SHADOWTEST_NS=$OC_PROJECT
./testing/scripts/e2e-egress-test.sh
```

---

## Related docs

- Kind E2E reset: [`testing/scripts/e2e-reset-kind.sh`](../testing/scripts/e2e-reset-kind.sh)
- Egress test script: [`testing/scripts/e2e-egress-test.sh`](../testing/scripts/e2e-egress-test.sh)
- Architecture overview: [`ARCHITECTURE.md`](../ARCHITECTURE.md)
- Verification checklist: [`VERIFICATION.md`](../VERIFICATION.md)
