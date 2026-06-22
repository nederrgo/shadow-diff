#!/usr/bin/env bash
# E2E: prod echo-http -> Pixie eBPF -> Siphon OTLP -> igris-http -> 3 shadow clones.
#
# Flow:
#   1. Traffic hits production my-prod-app Service (cluster DNS)
#   2. Pixie PEM captures http_events; pixie-stream-bridge px.export -> Siphon :4317
#   3. Siphon POSTs to igris-http in the shadow namespace
#   4. Igris multicasts to control-a, control-b, and candidate
#
# Prerequisites:
#   MINIKUBE_DRIVER=kvm2 ./testing/scripts/setup-local-pixie.sh
#   ./testing/scripts/e2e-reset-minikube.sh --no-reset
#
# Usage:
#   ./testing/scripts/e2e-siphon-otlp-ingress-test.sh
#   ./testing/scripts/e2e-reset-minikube.sh --setup-pixie --run-otlp-ingress-test
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
PROD_LABEL="${PROD_LABEL:-app=my-prod-app}"
TRACE_ID="${TRACE_ID:-otlp-ingress-$(date +%s)}"
IGRIS_PORT="${IGRIS_PORT:-80}"
REQUEST_PATH="/?otlp_probe=${TRACE_ID}"
PIXIE_WAIT_SEC="${PIXIE_WAIT_SEC:-}"

require_kubectl_cluster

echo "==> OTLP ingress E2E: prod Service -> Pixie eBPF -> Siphon -> Igris -> 3 shadows"
echo "    ShadowTest=${SHADOWTEST_NS}/${SHADOWTEST} trace=${TRACE_ID}"

phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
siphon_phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.siphonPhase}' 2>/dev/null || true)
SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
capture=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.captureTargets}' 2>/dev/null || true)

if [[ "$phase" != "Ready" || -z "$SHADOW_NS" ]]; then
  log_fail "ShadowTest not Ready — run ./testing/scripts/e2e-reset-minikube.sh first"
  exit 1
fi

echo "==> Pixie Vizier (pl namespace)"
wait_pixie_vizier_pem 120
wait_pixie_vizier_healthy 120
wait_pixie_http_events_ready 180

echo "==> Production tap target (my-prod-app Service — not Siphon)"
PROD_HOST=$(wait_prod_echo_ready "$PROD_NS" "$PROD_LABEL")
echo "    prod Service host=${PROD_HOST}"
echo "    Monarch captureTargets (prod template labels): ${capture:-<none>}"
if [[ "$capture" != *"app=my-prod-app"* ]]; then
  log_fail "captureTargets should list prod template labels (app=my-prod-app), not Siphon IPs"
  exit 1
fi
log_success "Monarch targets prod pod labels for Pixie capture"

if [[ "$siphon_phase" == "Ready" ]]; then
  echo "==> PixieStreamRule (export destination is shadow Siphon, tap target is prod)"
  wait_pixie_stream_rule "$SHADOWTEST" "$SHADOWTEST_NS" 120
  otel_ep=$(kubectl get pixiestreamrule "pixie-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.otelEndpoint}' 2>/dev/null || true)
  recorder_ep=$(kubectl get pixiestreamrule "pixie-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.recorderOtelEndpoint}' 2>/dev/null || true)
  echo "    otelEndpoint=${otel_ep} (Pixie sends here — separate from prod ${PROD_HOST})"
  if [[ -n "$recorder_ep" ]]; then
    echo "    recorderOtelEndpoint=${recorder_ep}"
  fi
  if [[ "$otel_ep" != *"siphon.${SHADOW_NS}.svc.cluster.local:4317"* ]]; then
    log_fail "PixieStreamRule must export to shadow Siphon Service, not prod"
    exit 1
  fi
fi

if [[ -z "$PIXIE_WAIT_SEC" ]]; then
  recorder_ep=$(kubectl get pixiestreamrule "pixie-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.recorderOtelEndpoint}' 2>/dev/null || true)
  if [[ -n "${recorder_ep:-}" ]]; then
    PIXIE_WAIT_SEC=120
  else
    PIXIE_WAIT_SEC=90
  fi
fi

echo "==> Shadow Siphon OTLP receiver (forwards to igris-http only)"
ensure_shadow_siphon_deployment "$SHADOW_NS" "$SHADOWTEST" "$IGRIS_PORT"

if ! pgrep -f pixie-stream-bridge.sh >/dev/null 2>&1; then
  log_fail "pixie-stream-bridge not running — start: $(pixie_bridge_start_hint)"
  exit 1
fi

echo "==> Step 1: curl production Service (${PROD_HOST}:80)"
curl_prod_service "$PROD_NS" "$TRACE_ID" "$REQUEST_PATH"
log_success "prod echo received request trace=${TRACE_ID}"

ingress_pxl="${PIXIE_BRIDGE_STATE_DIR}/${SHADOWTEST_NS}-pixie-${SHADOWTEST}-ingress.pxl"
egress_pxl="${PIXIE_BRIDGE_STATE_DIR}/${SHADOWTEST_NS}-pixie-${SHADOWTEST}-egress.pxl"
echo "==> Step 2-3: Pixie px.export -> Siphon -> Igris (up to ${PIXIE_WAIT_SEC}s)"
deadline=$((SECONDS + PIXIE_WAIT_SEC))
multicast_ok=0
while [[ "$SECONDS" -lt "$deadline" ]]; do
  [[ -f "$ingress_pxl" ]] && run_pixie_export_once "$ingress_pxl" || true
  [[ -f "$egress_pxl" ]] && run_pixie_export_once "$egress_pxl" || true
  if wait_igris_multicast_trace "$SHADOW_NS" "$SHADOWTEST" "$TRACE_ID" 2; then
    multicast_ok=1
    break
  fi
  sleep 3
done
if [[ "$multicast_ok" -ne 1 ]]; then
  log_fail "Igris did not multicast trace ${TRACE_ID}"
  kubectl logs -n "$SHADOW_NS" deployment/siphon --tail=30 2>&1 | sed 's/^/       /' || true
  exit 1
fi
log_success "Igris multicast complete for ${TRACE_ID}"

echo "==> Step 4: verify all three shadow clones (control-a, control-b, candidate)"
if assert_shadow_roles_saw_trace "$SHADOW_NS" "$SHADOWTEST" "$TRACE_ID"; then
  log_success "all three shadow clones received the multicasted request"
else
  log_fail "one or more shadow clones did not see trace ${TRACE_ID}"
  exit 1
fi

echo ""
log_success "OTLP ingress path OK: prod(${PROD_HOST}) -> Pixie -> Siphon -> Igris -> 3 shadows"
