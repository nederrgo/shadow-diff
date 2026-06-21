#!/usr/bin/env bash
# Verify OpenTelemetry Operator injected init/agent containers into shadow app pods.
# Pod Phase Running is not sufficient — the mutating webhook can fail-open.
set -euo pipefail

SHADOW_NS="${1:?usage: assert-otel-injected.sh <shadow-namespace> [role-label] [shadowtest-name]}"
ROLE="${2:-}"
SHADOWTEST="${3:-}"

label_selector="shadow-diff.io/resource-kind!=dependency"
if [[ -n "$ROLE" ]]; then
  label_selector="shadow-diff.io/role=${ROLE},${label_selector}"
fi
if [[ -n "$SHADOWTEST" ]]; then
  label_selector="shadow-diff.io/shadowtest-name=${SHADOWTEST},${label_selector}"
fi
label_args=(-l "$label_selector")

pod_is_terminating() {
  local pod="$1"
  [[ -n "$(kubectl get pod -n "$SHADOW_NS" "$pod" -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null || true)" ]]
}

pod_has_otel() {
  local pod="$1"
  local names
  names=$(kubectl get pod -n "$SHADOW_NS" "$pod" -o jsonpath='{.spec.initContainers[*].name}{" "}{.spec.containers[*].name}')
  echo "$names" | grep -qiE 'otel|opentelemetry|auto-instrumentation|instrumentation'
}

list_active_pods() {
  kubectl get pods -n "$SHADOW_NS" "${label_args[@]}" \
    --field-selector=status.phase=Running \
    --sort-by=.metadata.creationTimestamp \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | while IFS= read -r pod; do
    [[ -z "$pod" ]] && continue
    pod_is_terminating "$pod" && continue
    echo "$pod"
  done
}

check_pod() {
  local pod="$1"
  local names
  names=$(kubectl get pod -n "$SHADOW_NS" "$pod" -o jsonpath='{.spec.initContainers[*].name}{" "}{.spec.containers[*].name}')
  if pod_has_otel "$pod"; then
    echo "OK: ${pod} has OTel injection (${names})"
    return 0
  fi
  echo "WARN: ${pod} missing OTel init/agent containers (webhook may have failed-open)" >&2
  echo "      containers: ${names}" >&2
  return 1
}

# After rollout restart, old replicas may still be Running briefly without injection.
if [[ -n "$ROLE" ]]; then
  for _ in $(seq 1 30); do
    mapfile -t active < <(list_active_pods)
    [[ "${#active[@]}" -le 1 ]] && break
    sleep 2
  done
  mapfile -t active < <(list_active_pods)
  if [[ "${#active[@]}" -eq 0 ]]; then
    echo "ERROR: no active pods in namespace ${SHADOW_NS} for role ${ROLE}" >&2
    exit 1
  fi
  pods_to_check=("${active[-1]}")
else
  mapfile -t pods_to_check < <(list_active_pods)
  if [[ "${#pods_to_check[@]}" -eq 0 ]]; then
    echo "ERROR: no pods in namespace ${SHADOW_NS}" >&2
    exit 1
  fi
fi

missing=0
for pod in "${pods_to_check[@]}"; do
  check_pod "$pod" || missing=$((missing + 1))
done

if [[ "$missing" -gt 0 ]]; then
  echo "" >&2
  echo "ERROR: ${missing} shadow pod(s) lack OTel injection. Ensure:" >&2
  echo "  - OpenTelemetry Operator is installed and its webhook is healthy" >&2
  echo "  - An Instrumentation CR exists in ${SHADOW_NS} (or operator default namespace)" >&2
  echo "  - spec.otelInjection.enabled is not false on the ShadowTest" >&2
  if [[ "${OTEL_INJECTION_OPTIONAL:-}" == "1" ]]; then
    echo "OTEL_INJECTION_OPTIONAL=1 — continuing despite missing injection" >&2
    exit 0
  fi
  exit 1
fi

echo "All checked pods in ${SHADOW_NS} have OTel injection markers."
