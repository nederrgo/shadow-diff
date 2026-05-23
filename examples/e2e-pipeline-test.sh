#!/usr/bin/env bash
# Full E2E: prod -> Siphon -> Igris -> shadow Envoy -> Beru
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
TRACE_ID="${TRACE_ID:-e2e-$(date +%s)}"
PROD_PORT="${PROD_PORT:-80}"
IGRIS_LISTEN_PORT="${IGRIS_LISTEN_PORT:-80}"   # must match prod port for Siphon forward
# Kind: use "any" so Siphon picks cni0/br-*/eth0/veth* as available on the node.
SIPHON_IFACE="${SIPHON_IFACE:-any}"

echo "==> Prerequisites"
kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o custom-columns=PHASE:.status.phase,SIPHON:.status.siphonPhase,NS:.status.shadowNamespace,CAPTURE:.status.captureTargets

SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}')

# Optional one-time setup: set Siphon interface without rolling during the test.
if [[ "${SIPHON_SETUP:-}" == "1" ]]; then
  echo "==> One-time Siphon setup (SIPHON_SETUP=1)"
  kubectl set env daemonset/siphon-agent -n siphon-system \
    SIPHON_INTERFACE="${SIPHON_IFACE}" --containers=agent
  kubectl rollout status daemonset/siphon-agent -n siphon-system --timeout=120s
fi

echo "==> Wait for Siphon agent (hostNetwork — use hostIP for API)"
kubectl wait -n siphon-system --for=condition=Ready pod \
  -l app.kubernetes.io/name=siphon-agent --timeout=120s

# hostIP is the node address where hostNetwork :8080 is reachable from cluster pods.
SIPHON_IP=$(kubectl get pods -n siphon-system -l app.kubernetes.io/name=siphon-agent \
  -o jsonpath='{.items[0].status.hostIP}')
if [[ -z "${SIPHON_IP}" ]]; then
  echo "ERROR: could not resolve Siphon hostIP" >&2
  exit 1
fi
echo "Siphon API: http://${SIPHON_IP}:8080 (hostIP)"

# Fresh prod IP every run (pod restarts change IP; must match BPF target_ips).
kubectl wait -n default --for=condition=Ready pod -l app=my-prod-app --timeout=120s
PROD_IP=$(kubectl get pods -n default -l app=my-prod-app -o jsonpath='{.items[0].status.podIP}')
if [[ -z "${PROD_IP}" ]]; then
  echo "ERROR: prod pod has no IP (is my-prod-app Running?)" >&2
  exit 1
fi
echo "Prod target: ${PROD_IP}:${PROD_PORT}"

CONFIG_JSON=$(cat <<EOF
{"sample_rate":100,"targets":[{"shadowtest":"${SHADOWTEST_NS}/${SHADOWTEST}","target_ips":["${PROD_IP}"],"target_ports":[${PROD_PORT}],"igris_host":"my-app-shadow-igris.${SHADOW_NS}.svc.cluster.local","listeners":[{"port":${IGRIS_LISTEN_PORT},"driver":"http_request"}]}]}
EOF
)

echo "==> POST /v1/config to Siphon"
kubectl run curl-siphon-config --rm -i --restart=Never --image=curlimages/curl:latest -- \
  curl -sf -X POST "http://${SIPHON_IP}:8080/v1/config" \
  -H 'Content-Type: application/json' \
  -d "${CONFIG_JSON}"

echo "==> Siphon /v1/status (before traffic — want targets_count>=1, interfaces non-null)"
kubectl run curl-siphon-status --rm -i --restart=Never --image=curlimages/curl:latest -- \
  curl -sf "http://${SIPHON_IP}:8080/v1/status" || true

echo "==> Hit production IN-CLUSTER (pod IP ${PROD_IP}:${PROD_PORT}, trace ${TRACE_ID})"
echo "    Do NOT use kubectl port-forward to prod."
kubectl run "e2e-prod-${TRACE_ID}" --rm -i --restart=Never -n default --image=curlimages/curl:latest -- \
  curl -sf -o /dev/null -w "prod_http=%{http_code}\n" \
  -H "x-shadow-trace-id: ${TRACE_ID}" \
  "http://${PROD_IP}:${PROD_PORT}/"

echo "==> Wait for Siphon -> Igris -> Beru"
sleep 8

# Siphon may have restarted (e.g. Monarch reconcile during reset); re-apply config if needed.
kubectl wait -n siphon-system --for=condition=Ready pod \
  -l app.kubernetes.io/name=siphon-agent --timeout=120s
SIPHON_IP=$(kubectl get pods -n siphon-system -l app.kubernetes.io/name=siphon-agent \
  -o jsonpath='{.items[0].status.hostIP}')
after_cfg=$(kubectl run curl-siphon-recheck --rm -i --restart=Never --image=curlimages/curl:latest -- \
  curl -sf "http://${SIPHON_IP}:8080/v1/status" 2>/dev/null || echo '{}')
if echo "$after_cfg" | grep -q '"targets_count":0'; then
  echo "==> Siphon lost config (pod restart?) — re-POST /v1/config"
  PROD_IP=$(kubectl get pods -n default -l app=my-prod-app -o jsonpath='{.items[0].status.podIP}')
  kubectl run curl-siphon-reconfig --rm -i --restart=Never --image=curlimages/curl:latest -- \
    curl -sf -X POST "http://${SIPHON_IP}:8080/v1/config" \
    -H 'Content-Type: application/json' \
    -d "{\"sample_rate\":100,\"targets\":[{\"shadowtest\":\"${SHADOWTEST_NS}/${SHADOWTEST}\",\"target_ips\":[\"${PROD_IP}\"],\"target_ports\":[${PROD_PORT}],\"igris_host\":\"my-app-shadow-igris.${SHADOW_NS}.svc.cluster.local\",\"listeners\":[{\"port\":${IGRIS_LISTEN_PORT},\"driver\":\"http_request\"}]}]}"
  kubectl run "e2e-prod-retry-${TRACE_ID}" --rm -i --restart=Never -n default --image=curlimages/curl:latest -- \
    curl -sf -o /dev/null -w "prod_http_retry=%{http_code}\n" \
    -H "x-shadow-trace-id: ${TRACE_ID}-retry" \
    "http://${PROD_IP}:${PROD_PORT}/"
  sleep 8
fi

echo "==> Siphon /v1/status (after traffic — want frames_read, packets, requests_forwarded > 0)"
kubectl run curl-siphon-status2 --rm -i --restart=Never --image=curlimages/curl:latest -- \
  curl -sf "http://${SIPHON_IP}:8080/v1/status" || true

echo "==> Siphon logs"
kubectl logs -n siphon-system daemonset/siphon-agent --tail=50 | grep -E 'BPF filter|Received configuration|Capture started|Reassembled|Error opening' \
  || kubectl logs -n siphon-system daemonset/siphon-agent --tail=20

echo "==> Igris logs (trace ${TRACE_ID})"
kubectl logs -n "$SHADOW_NS" deploy/my-app-shadow-igris --tail=40 | grep -E "${TRACE_ID}|multicast complete" || true

echo "==> Beru logs (trace ${TRACE_ID})"
kubectl logs -n beru-system deploy/beru --tail=40 | grep -E "${TRACE_ID}|Regression|No regression|Timed out" || true

echo ""
echo "Success checklist for trace ${TRACE_ID}:"
echo "  1. Siphon status: targets_count>=1; packets > 0; requests_forwarded > 0"
echo "  2. Siphon log:    BPF filter applied; Capture started; Reassembled HTTP request"
echo "  3. Igris log:     multicast complete trace_id=${TRACE_ID}"
echo "  4. Beru log:      No regression OR Regression found for Trace ${TRACE_ID}"
