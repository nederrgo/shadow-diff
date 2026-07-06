# HTTP ingress (igris-http) → OTel → AMQP/Mongo egress E2E helpers.
# shellcheck shell=bash

bats_source_http_otel_helpers() {
  # shellcheck source=testing/scripts/helpers/e2e-http-otel-rmq.sh
  source "${REPO}/testing/scripts/helpers/e2e-http-otel-rmq.sh"
}

bats_use_pixie_auto() {
  if [[ -z "${USE_PIXIE:-}" ]]; then
    kubectl get ns pl >/dev/null 2>&1 && USE_PIXIE=1 || USE_PIXIE=0
  fi
  export USE_PIXIE
}

bats_pixie_mongo_enabled() {
  bats_use_pixie_auto
  [[ "${USE_PIXIE:-0}" == "1" ]]
}

bats_http_otel_firehose_ready() {
  bats_source_http_otel_helpers
  http_otel_rmq_verify_firehose "${SHADOW_NS}"
}

# Restart shadow workers so Mongo connections start under Pixie observation.
bats_http_otel_restart_workers_for_pixie() {
  local shadowtest="${1:-${SHADOWTEST}}" shadow_ns="${2:-${SHADOW_NS}}"
  bats_pixie_mongo_enabled || return 0
  echo "==> restart shadow workers for Pixie Mongo capture"
  for role in control-a control-b candidate; do
    kubectl rollout restart "deployment/${shadowtest}-${role}" -n "$shadow_ns" 2>/dev/null || true
    kubectl rollout status "deployment/${shadowtest}-${role}" -n "$shadow_ns" --timeout=180s 2>/dev/null || true
  done
}

bats_http_otel_rollout_stack() {
  local shadowtest="${1:-${SHADOWTEST}}" shadow_ns="${2:-${SHADOW_NS}}"
  kubectl rollout status "deployment/${shadowtest}-egress-relay-rabbitmq" -n "$shadow_ns" --timeout=180s
  kubectl rollout status "deployment/${shadowtest}-igris" -n "$shadow_ns" --timeout=120s
  for role in control-a control-b candidate; do
    kubectl rollout status "deployment/mongodb-${role}" -n "$shadow_ns" --timeout=180s
    kubectl rollout status "deployment/${shadowtest}-${role}" -n "$shadow_ns" --timeout=180s
  done
}

bats_http_otel_reverify_pixie() {
  bats_pixie_mongo_enabled || return 0
  bats_source_pixie_helpers
  wait_pixie_vizier_healthy 120
}
