# HTTP ingress (igris-http) → OTel → AMQP/Mongo egress E2E helpers.
# shellcheck shell=bash

bats_source_http_otel_helpers() {
  # shellcheck source=testing/bats/helpers/e2e-http-otel-rmq.sh
  source "${REPO}/testing/bats/helpers/e2e-http-otel-rmq.sh"
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
  # Monarch provisions Deployment/siphon when HTTP ingress capture is enabled.
  echo "==> wait for Monarch Siphon OTLP receiver"
  kubectl rollout status deployment/siphon -n "$shadow_ns" --timeout=120s
  _bats_http_otel_warmup_extproc "$shadow_ns"
}

# Re-publish a warmup trace every 5s until beru-local logs "No regression" for it.
# This ensures Envoy's lazy gRPC ext_proc connection to beru-local is established
# before test 1 runs — on cold image pull the pod can be Available before the gRPC
# channel is open, causing failure_mode_allow:true to silently drop the first report.
# ponytail: 60s cap; returns 0 on timeout so setup never fails from warmup alone
_bats_http_otel_warmup_extproc() {
  local shadow_ns="${1:-${SHADOW_NS}}"
  local warmup_id
  warmup_id="warmup$(openssl rand -hex 6)"
  local pattern="No regression for Trace ${warmup_id}"
  local pod last_publish=0 deadline=$((SECONDS + 60))
  pod="$(beru_local_pod "$shadow_ns")"
  echo "==> warm up beru-local ext_proc connection"
  while [[ $SECONDS -lt $deadline ]]; do
    if [[ $((SECONDS - last_publish)) -ge 5 ]]; then
      publish_igris_http "$warmup_id" >/dev/null 2>&1 || true
      last_publish=$SECONDS
    fi
    if [[ -n "$pod" ]] && \
       kubectl logs -n "$shadow_ns" "$pod" --tail=200 2>/dev/null | grep -Fq "$pattern"; then
      echo "    ext_proc connected and beru-local receiving reports"
      return 0
    fi
    sleep 1
  done
  echo "    WARNING: ext_proc warmup timed out after 60s — test 1 may be flaky" >&2
  return 0
}

bats_http_otel_reverify_pixie() {
  bats_pixie_mongo_enabled || return 0
  bats_source_pixie_helpers
  wait_pixie_vizier_healthy 120
}
