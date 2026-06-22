#!/usr/bin/env bash
# E2E: prod outbound (Service DNS Host) -> Pixie eBPF egress -> Recorder OTLP :4317 -> Beru /v1/record_egress
#
# Prerequisites:
#   MINIKUBE_DRIVER=kvm2 ./testing/scripts/setup-local-pixie.sh
#   ./testing/scripts/e2e-reset-minikube.sh --no-reset
#   ./testing/scripts/start-pixie-stream-bridge.sh
#
# Usage:
#   ./testing/scripts/e2e-pixie-egress-record-test.sh
#   PIXIE_WAIT_SEC=120 ./testing/scripts/e2e-pixie-egress-record-test.sh
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"
# shellcheck source=testing/scripts/lib/siphon-otlp.sh
source "$REPO/testing/scripts/lib/siphon-otlp.sh"
# shellcheck source=testing/scripts/lib/pixie-bridge.sh
source "$REPO/testing/scripts/lib/pixie-bridge.sh"

SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
PROD_NS="${PROD_NS:-default}"
EGRESS_HOST="${EGRESS_HOST:-egress-httpbin.default.svc.cluster.local}"
EGRESS_PATH="${EGRESS_PATH:-/post}"
RECORD_BODY="${RECORD_BODY:-{\"pixie_egress_e2e\":1}}"
PIXIE_WAIT_SEC="${PIXIE_WAIT_SEC:-120}"

require_kubectl_cluster

echo "==> Pixie egress record E2E: prod -> Pixie (trace_role=1) -> Recorder OTLP -> Beru"
echo "    ShadowTest=${SHADOWTEST_NS}/${SHADOWTEST} egress=${EGRESS_HOST}${EGRESS_PATH}"

phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
if [[ "$phase" != "Ready" || -z "$SHADOW_NS" ]]; then
  log_fail "ShadowTest not Ready — run ./testing/scripts/e2e-reset-minikube.sh first"
  exit 1
fi

rr_host=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
  -o jsonpath='{.spec.recordAndReplay[0].host}' 2>/dev/null || true)
if [[ -z "$rr_host" ]]; then
  log_fail "ShadowTest spec.recordAndReplay[0].host is required"
  exit 1
fi
EGRESS_HOST="$rr_host"

kubectl wait -n default --for=condition=Available deployment/egress-httpbin --timeout=120s 2>/dev/null \
  || kubectl apply -f "$REPO/testing/scripts/manifests/e2e-egress-httpbin.yaml"
kubectl wait -n default --for=condition=Available deployment/egress-httpbin --timeout=120s
kubectl wait -n "$PROD_NS" --for=condition=Ready pod -l app=my-prod-app --timeout=120s
kubectl wait -n beru-system --for=condition=Ready pod \
  -l app.kubernetes.io/name=beru --timeout=120s

echo "==> Pixie Vizier (pl namespace)"
wait_pixie_vizier_pem 120
wait_pixie_vizier_healthy 120
wait_pixie_http_events_ready 180

echo "==> PixieStreamRule recorder OTLP endpoint"
wait_pixie_stream_rule "$SHADOWTEST" "$SHADOWTEST_NS" 120
recorder_ep=$(kubectl get pixiestreamrule "pixie-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
  -o jsonpath='{.spec.recorderOtelEndpoint}' 2>/dev/null || true)
echo "    recorderOtelEndpoint=${recorder_ep}"
if [[ "$recorder_ep" != *"recorder.${SHADOW_NS}.svc.cluster.local:4317"* ]]; then
  log_fail "PixieStreamRule must export egress to shadow Recorder Service :4317"
  exit 1
fi

if ! pgrep -f pixie-stream-bridge.sh >/dev/null 2>&1; then
  log_fail "pixie-stream-bridge not running — start: $(pixie_bridge_start_hint)"
  exit 1
fi

PROD_POD=$(kubectl get pod -n "$PROD_NS" -l app=my-prod-app -o jsonpath='{.items[0].metadata.name}')
RECORDER_DEPLOY="${SHADOWTEST}-recorder"
egress_pxl="${PIXIE_BRIDGE_STATE_DIR}/${SHADOWTEST_NS}-pixie-${SHADOWTEST}-egress.pxl"
record_marker="beru client: recorded POST ${EGRESS_HOST}${EGRESS_PATH}"

echo "==> Step 1: prod outbound POST via Service DNS (Host=${EGRESS_HOST})"
url="http://${EGRESS_HOST}${EGRESS_PATH}"
body_q=$(printf '%q' "$RECORD_BODY")
url_q=$(printf '%q' "$url")
out=$(kubectl exec -n "$PROD_NS" "$PROD_POD" -c nginx -- sh -c "
set -e
if command -v wget >/dev/null 2>&1; then
  wget -q -O - --post-data=${body_q} --header='Content-Type: application/json' ${url_q} || true
  echo
  echo '__CODE__200'
elif command -v curl >/dev/null 2>&1; then
  curl -sS -w '__CODE__%{http_code}' -H 'Content-Type: application/json' -d ${body_q} ${url_q}
  echo
else
  echo '__CODE__000'
fi
")
if ! echo "$out" | grep -q '__CODE__200'; then
  log_fail "prod egress to ${url} failed: ${out}"
  exit 1
fi
log_success "prod issued outbound POST to ${EGRESS_HOST}${EGRESS_PATH}"

echo "==> Step 2: Pixie px.export (egress) -> Recorder -> Beru (up to ${PIXIE_WAIT_SEC}s)"
deadline=$((SECONDS + PIXIE_WAIT_SEC))
seeded=0
while [[ "$SECONDS" -lt "$deadline" ]]; do
  if pixie_vizier_healthy; then
    [[ -f "$egress_pxl" ]] && run_pixie_export_once "$egress_pxl" || true
  fi
  if kubectl logs -n "$SHADOW_NS" "deploy/${RECORDER_DEPLOY}" --tail=200 2>/dev/null \
    | grep -Fq "$record_marker"; then
    seeded=1
    break
  fi
  sleep 3
done

if [[ "$seeded" -ne 1 ]]; then
  log_fail "Recorder did not seed Beru (${record_marker})"
  kubectl logs -n "$SHADOW_NS" "deploy/${RECORDER_DEPLOY}" --tail=40 2>&1 | sed 's/^/       /' || true
  exit 1
fi
log_success "Recorder posted egress capture to Beru mock store"

echo ""
log_success "Pixie egress record path OK: prod -> Pixie -> Recorder OTLP -> Beru"
