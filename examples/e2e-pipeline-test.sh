#!/usr/bin/env bash
# Full E2E: prod -> Siphon -> Igris -> shadow Envoy -> Beru
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
TRACE_ID="${TRACE_ID:-e2e-$(date +%s)}"
PROD_PORT="${PROD_PORT:-80}"
IGRIS_LISTEN_PORT="${IGRIS_LISTEN_PORT:-80}"   # must match prod port for Siphon forward

echo "==> Prerequisites"
kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o custom-columns=PHASE:.status.phase,SIPHON:.status.siphonPhase,NS:.status.shadowNamespace,CAPTURE:.status.captureTargets

SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}')
PROD_IP=$(kubectl get pods -n default -l app=my-prod-app -o jsonpath='{.items[0].status.podIP}')
SIPHON_IP=$(kubectl get pods -n siphon-system -l app.kubernetes.io/name=siphon-agent -o jsonpath='{.items[0].status.podIP}')

echo "==> Siphon: multi-iface on Kind + config"
kubectl set env daemonset/siphon-agent -n siphon-system SIPHON_INTERFACE=any --containers=agent 2>/dev/null || true
kubectl rollout status daemonset/siphon-agent -n siphon-system --timeout=60s 2>/dev/null || true

kubectl run curl-siphon-config --rm -i --restart=Never --image=curlimages/curl:latest -- \
  curl -s -X POST "http://${SIPHON_IP}:8080/v1/config" \
  -H 'Content-Type: application/json' \
  -d "{\"sample_rate\":100,\"targets\":[{\"shadowtest\":\"${SHADOWTEST_NS}/${SHADOWTEST}\",\"target_ips\":[\"${PROD_IP}\"],\"target_ports\":[${PROD_PORT}],\"igris_host\":\"my-app-shadow-igris.${SHADOW_NS}.svc.cluster.local\",\"listeners\":[{\"port\":${IGRIS_LISTEN_PORT},\"driver\":\"http_request\"}]}]}"

echo "==> Siphon /v1/status (before traffic)"
kubectl run curl-siphon-status --rm -i --restart=Never --image=curlimages/curl:latest -- \
  curl -s "http://${SIPHON_IP}:8080/v1/status" || true

echo "==> Hit production IN-CLUSTER (pod IP ${PROD_IP}:${PROD_PORT}, trace ${TRACE_ID})"
echo "    Do NOT use kubectl port-forward to prod."
kubectl run "e2e-prod-${TRACE_ID}" --rm -i --restart=Never -n default --image=curlimages/curl:latest -- \
  curl -s -o /dev/null -w "prod_http=%{http_code}\n" \
  -H "x-shadow-trace-id: ${TRACE_ID}" \
  "http://${PROD_IP}:${PROD_PORT}/"

echo "==> Wait for Siphon -> Igris -> Beru"
sleep 8

echo "==> Siphon /v1/status (after traffic — want frames_read, packets, requests_forwarded > 0)"
kubectl run curl-siphon-status2 --rm -i --restart=Never --image=curlimages/curl:latest -- \
  curl -s "http://${SIPHON_IP}:8080/v1/status" || true

echo "==> Siphon logs"
kubectl logs -n siphon-system daemonset/siphon-agent --tail=20 | grep -E 'Reassembled|Capture started|error' || kubectl logs -n siphon-system daemonset/siphon-agent --tail=8

echo "==> Igris logs (trace ${TRACE_ID})"
kubectl logs -n "$SHADOW_NS" deploy/my-app-shadow-igris --tail=40 | grep -E "${TRACE_ID}|multicast complete" || true

echo "==> Beru logs (trace ${TRACE_ID})"
kubectl logs -n beru-system deploy/beru --tail=40 | grep -E "${TRACE_ID}|Regression|No regression|Timed out" || true

echo ""
echo "Success checklist for trace ${TRACE_ID}:"
echo "  1. Siphon status: packets > 0, requests_forwarded > 0"
echo "  2. Siphon log:    Reassembled HTTP request"
echo "  3. Igris log:     multicast complete trace_id=${TRACE_ID}"
echo "  4. Beru log:      No regression OR Regression found for Trace ${TRACE_ID}"
