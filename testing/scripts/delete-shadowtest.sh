#!/usr/bin/env bash
# Delete ShadowTest CR(s) and wait for Monarch to tear down shadow namespaces (beru-local GC).
#
# Usage:
#   ./testing/scripts/delete-shadowtest.sh <name> [namespace]
#   ./testing/scripts/delete-shadowtest.sh --all
#   SHADOWTEST=my-shadow SHADOWTEST_NS=default ./testing/scripts/delete-shadowtest.sh
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"

WAIT_SECS="${WAIT_SECS:-180}"

usage() {
  cat <<EOF
Usage:
  $(basename "$0") <shadowtest-name> [namespace]
  $(basename "$0") --all

Deletes the ShadowTest CR; Monarch removes the shadow namespace (beru-local, apps, deps).
Env: SHADOWTEST, SHADOWTEST_NS (default namespace: default), WAIT_SECS
EOF
}

wait_shadow_namespace_gone() {
  local ns="$1" max_wait="${2:-$WAIT_SECS}"
  [[ -n "$ns" ]] || return 0
  if ! kubectl get namespace "$ns" >/dev/null 2>&1; then
    return 0
  fi
  local i=0
  while kubectl get namespace "$ns" >/dev/null 2>&1; do
    i=$((i + 2))
    if [[ "$i" -gt "$max_wait" ]]; then
      log_fail "timed out waiting for namespace ${ns} to be deleted"
      kubectl get namespace "$ns" -o yaml 2>/dev/null | tail -20 >&2 || true
      return 1
    fi
    echo "    waiting for namespace ${ns} to finish deleting (${i}s/${max_wait}s)..."
    sleep 2
  done
  return 0
}

delete_one_shadowtest() {
  local name="$1" ns="$2"
  local shadow_ns=""

  if kubectl get shadowtest "$name" -n "$ns" >/dev/null 2>&1; then
    shadow_ns=$(kubectl get shadowtest "$name" -n "$ns" \
      -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
    echo "==> Delete ShadowTest ${ns}/${name} (shadow namespace: ${shadow_ns:-<pending>})"
    kubectl delete shadowtest "$name" -n "$ns" --wait=false
  else
    echo "==> ShadowTest ${ns}/${name} not found (skip CR delete)"
    shadow_ns="${SHADOW_NS:-shadow-${ns}-${name}}"
  fi

  wait_shadowtest_gone "$name" "$ns" "$WAIT_SECS" || return 1

  if [[ -n "$shadow_ns" ]]; then
    echo "==> Wait for Monarch to delete shadow namespace ${shadow_ns}"
    wait_shadow_namespace_gone "$shadow_ns" "$WAIT_SECS" || return 1
  fi

  log_success "removed ${ns}/${name}"
}

require_kubectl_cluster

case "${1:-}" in
  -h|--help)
    usage
    exit 0
    ;;
  --all)
    mapfile -t rows < <(kubectl get shadowtest -A --no-headers 2>/dev/null | awk '{print $1 "\t" $2}' || true)
    if [[ "${#rows[@]}" -eq 0 ]]; then
      echo "==> No ShadowTest resources found"
      exit 0
    fi
    for row in "${rows[@]}"; do
      ns="${row%%$'\t'*}"
      name="${row#*$'\t'}"
      delete_one_shadowtest "$name" "$ns" || exit 1
    done
    ;;
  "")
    if [[ -z "${SHADOWTEST:-}" ]]; then
      usage >&2
      log_fail "missing shadowtest name (arg or SHADOWTEST env)"
      exit 1
    fi
    delete_one_shadowtest "$SHADOWTEST" "${SHADOWTEST_NS:-default}"
    ;;
  *)
    delete_one_shadowtest "$1" "${2:-${SHADOWTEST_NS:-default}}"
    ;;
esac
