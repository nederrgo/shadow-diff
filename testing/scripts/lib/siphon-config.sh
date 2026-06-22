# Monarch Siphon / Pixie capture helpers for E2E scripts.
# Source from E2E scripts; do not execute directly.

nudge_siphon_config() {
  local shadowtest="${1:-${SHADOWTEST:-my-app-shadow}}"
  local ns="${2:-${SHADOWTEST_NS:-default}}"
  kubectl annotate shadowtest "$shadowtest" -n "$ns" \
    shadow-diff.io/reconcile="$(date +%s)" --overwrite >/dev/null
}

wait_pixie_capture_ready() {
  local shadowtest="${1:-${SHADOWTEST:-my-app-shadow}}"
  local shadowtest_ns="${2:-${SHADOWTEST_NS:-default}}"
  local shadow_ns="${3:-}"
  local max_wait="${4:-120}"
  local name="pixie-${shadowtest}" i=0

  if [[ -z "$shadow_ns" ]]; then
    shadow_ns=$(kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" \
      -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  fi
  [[ -n "$shadow_ns" ]] || {
    echo "ERROR: shadow namespace missing for ${shadowtest_ns}/${shadowtest}" >&2
    return 1
  }

  while [[ "$i" -lt "$max_wait" ]]; do
    if kubectl get pixiestreamrule "$name" -n "$shadowtest_ns" >/dev/null 2>&1; then
      local active otel_ep
      active=$(kubectl get pixiestreamrule "$name" -n "$shadowtest_ns" \
        -o jsonpath='{.spec.active}' 2>/dev/null || true)
      otel_ep=$(kubectl get pixiestreamrule "$name" -n "$shadowtest_ns" \
        -o jsonpath='{.spec.otelEndpoint}' 2>/dev/null || true)
      if [[ "$active" == "true" && "$otel_ep" == *"siphon.${shadow_ns}.svc.cluster.local:4317"* ]]; then
        echo "    PixieStreamRule ${shadowtest_ns}/${name} -> ${otel_ep}"
        return 0
      fi
    fi
    echo "    waiting for PixieStreamRule ${name} (${i}s/${max_wait}s)"
    nudge_siphon_config "$shadowtest" "$shadowtest_ns"
    sleep 3
    i=$((i + 3))
  done
  echo "ERROR: PixieStreamRule not ready for ${shadowtest}" >&2
  return 1
}

wait_shadow_siphon_otlp() {
  local shadow_ns="$1"
  local repo="${REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
  # shellcheck source=testing/scripts/lib/siphon-otlp.sh
  source "${repo}/testing/scripts/lib/siphon-otlp.sh"
  local shadowtest="${SHADOWTEST:-my-app-shadow}"
  ensure_shadow_siphon_deployment "$shadow_ns" "$shadowtest" 80
}
