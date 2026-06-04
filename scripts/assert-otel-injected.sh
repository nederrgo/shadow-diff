#!/usr/bin/env bash
# Verify OpenTelemetry Operator injected init/agent containers into shadow app pods.
# Pod Phase Running is not sufficient — the mutating webhook can fail-open.
set -euo pipefail

SHADOW_NS="${1:?usage: assert-otel-injected.sh <shadow-namespace> [role-label]}"
ROLE="${2:-}"

label_args=()
if [[ -n "$ROLE" ]]; then
  label_args=(-l "shadow-diff.io/role=${ROLE}")
fi

pods=$(kubectl get pods -n "$SHADOW_NS" "${label_args[@]}" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
if [[ -z "$pods" ]]; then
  echo "ERROR: no pods in namespace ${SHADOW_NS}" >&2
  exit 1
fi

missing=0
while IFS= read -r pod; do
  [[ -z "$pod" ]] && continue
  names=$(kubectl get pod -n "$SHADOW_NS" "$pod" -o jsonpath='{.spec.initContainers[*].name}{" "}{.spec.containers[*].name}')
  if echo "$names" | grep -qiE 'otel|opentelemetry|auto-instrumentation|instrumentation'; then
    echo "OK: ${pod} has OTel injection (${names})"
  else
    echo "WARN: ${pod} missing OTel init/agent containers (webhook may have failed-open)" >&2
    echo "      containers: ${names}" >&2
    missing=$((missing + 1))
  fi
done <<< "$pods"

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
