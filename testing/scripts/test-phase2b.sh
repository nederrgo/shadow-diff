#!/usr/bin/env bash
# Phase 2b cluster smoke test. Requires: kubectl, grpcurl, docker (for image builds).
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
export MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
export BERU_IMG="${BERU_IMG:-beru:dev}"
export KIND_CLUSTER="${KIND_CLUSTER:-monarch-test}"
SHADOW_NS="${SHADOW_NS:-}"

echo "==> Build images"
make -C "$REPO/pipeline/monarch" docker-build IMG="$MONARCH_IMG"
make -C "$REPO" beru-docker-build BERU_IMG="$BERU_IMG"

if command -v kind >/dev/null 2>&1; then
  echo "==> Load images into kind cluster $KIND_CLUSTER"
  kind load docker-image "$MONARCH_IMG" "$BERU_IMG" --name "$KIND_CLUSTER"
fi

echo "==> Install/update CRD and apply ShadowTest (nginx: Envoy 8080 -> app 80)"
kubectl apply -f "$REPO/pipeline/monarch/config/crd/bases/engine.shadow-diff.io_shadowtests.yaml" 2>/dev/null || make -C "$REPO/pipeline/monarch" install
kubectl apply -f - <<EOF
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
  servicePort: 8080
  applicationPort: 80
  beruGRPCAddress: beru.beru-system.svc.cluster.local:50051
EOF

kubectl rollout restart deployment/monarch-controller-manager -n monarch-system 2>/dev/null || true
kubectl rollout restart deployment/beru -n beru-system 2>/dev/null || kubectl apply -f "$REPO/pipeline/beru/deploy/"
kubectl rollout status deployment/beru -n beru-system --timeout=120s

kubectl annotate shadowtest my-app-shadow -n default reconcile="$(date +%s)" --overwrite 2>/dev/null || true
SHADOW_NS=$(kubectl get shadowtest my-app-shadow -n default -o jsonpath='{.status.shadowNamespace}')
echo "Shadow namespace: $SHADOW_NS"
kubectl rollout restart deployment -n "$SHADOW_NS"
kubectl rollout status deployment/my-app-shadow-control-a -n "$SHADOW_NS" --timeout=180s
kubectl get pods -n "$SHADOW_NS"

echo "==> Test 1: grpcurl ReportTraffic (JSON regression)"
kubectl port-forward -n beru-system svc/beru 50051:50051 &
PF=$!
sleep 2
trap 'kill $PF 2>/dev/null || true' EXIT
for spec in 'control-a:eyJwcmljZSI6MTAsInRpbWVzdGFtcCI6MX0=' \
            'control-b:eyJwcmljZSI6MTAsInRpbWVzdGFtcCI6Mn0=' \
            'candidate:eyJwcmljZSI6MTIsInRpbWVzdGFtcCI6Mn0='; do
  ROLE="${spec%%:*}"
  BODY="${spec##*:}"
  grpcurl -plaintext \
    -import-path "$REPO/pipeline/beru/api/proto" \
    -proto beru/v1/traffic.proto \
    -d "{\"report\":{\"trace_id\":\"trace-grpc-test\",\"role\":\"$ROLE\",\"direction\":\"INGRESS\",\"payload\":{\"content_type\":\"application/json\",\"body\":\"$BODY\"}}}" \
    localhost:50051 beru.v1.TrafficReporter/ReportTraffic
done
sleep 1
kubectl logs -n beru-system deployment/beru --tail=5

echo "==> Test 2: HTTP via Envoy on all three pods (same trace id)"
TRACE_ID="trace-http-$(date +%s)"
for ROLE in control-a control-b candidate; do
  IP=$(kubectl get pod -n "$SHADOW_NS" -l "shadow-diff.io/role=$ROLE" -o jsonpath='{.items[0].status.podIP}')
  echo "  $ROLE -> $IP:8080"
  kubectl run "curl-${ROLE}-$$" -n "$SHADOW_NS" --image=curlimages/curl:8.5.0 --restart=Never --rm -i --quiet -- \
    curl -s -o /dev/null -w "%{http_code}\n" -H "x-shadow-trace-id: $TRACE_ID" "http://${IP}:8080/"
done
sleep 2
echo "Beru logs (expect 'Could not diff' for nginx HTML, or regression for JSON app):"
kubectl logs -n beru-system deployment/beru --tail=10

echo "Done."
