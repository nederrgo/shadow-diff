# Kaisel eBPF DaemonSet helpers for bats tests.
# shellcheck shell=bash

KAISEL_NS="kaisel-system"
KAISEL_DEPLOY_DIR="${REPO}/pipeline/kaisel/deploy"

# Self-contained platform setup for kaisel E2E: builds + loads Monarch and
# kaisel images, installs CRDs, deploys the Monarch operator, and deploys the
# kaisel DaemonSet.  Does NOT install Beru, Pixie, or other services — the
# capture assertion only needs the operator to emit a KaiselRule and kaisel to
# see it.
#
# Environment:
#   SKIP_BUILD=1   skip docker build (images must already exist in cluster)
#   SKIP_LOAD=1    skip image load (images must already be in cluster registry)
kaisel_setup_platform() {
  bats_source_e2e_helpers
  bats_source_cluster_helpers

  if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
    echo "==> [kaisel] build monarch image (${MONARCH_IMG})"
    e2e_prepare_docker_build
    make -C "${REPO}/pipeline/monarch" docker-build IMG="${MONARCH_IMG}"

    echo "==> [kaisel] build kaisel image (${KAISEL_IMG})"
    make -C "${REPO}/pipeline/kaisel" docker-build KAISEL_IMG="${KAISEL_IMG}"
  fi

  if [[ "${SKIP_LOAD:-0}" != "1" ]]; then
    echo "==> [kaisel] load images into cluster"
    e2e_load_image "${MONARCH_IMG}"
    e2e_load_image "${KAISEL_IMG}"
  fi

  echo "==> [kaisel] install Monarch CRDs"
  make -C "${REPO}/pipeline/monarch" install

  echo "==> [kaisel] deploy Monarch operator (${MONARCH_IMG})"
  make -C "${REPO}/pipeline/monarch" deploy IMG="${MONARCH_IMG}"
  # MONARCH_MODE=dev tells the operator to use locally-built :dev image tags for
  # shadow-stack components.  Pods may enter ImagePullBackOff if those images
  # aren't loaded, but the KaiselRule is created from live pod IPs before any
  # shadow pod becomes ready, so capture tests are unaffected.
  kubectl set env deployment/monarch-controller-manager -n monarch-system \
    MONARCH_MODE=dev 2>/dev/null || true
  # Force a rollout so the pod picks up a rebuilt image with the same tag.
  kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  kubectl rollout status deployment/monarch-controller-manager \
    -n monarch-system --timeout=180s
}

# Wait for a KaiselRule to exist with at least one targetIP.
# Usage: wait_kaiselrule_ready <name> <namespace> [timeout_seconds]
wait_kaiselrule_ready() {
  local name="$1" ns="${2:-default}" timeout="${3:-120}"
  local elapsed=0 ip

  echo "==> [kaisel] wait KaiselRule ${ns}/${name} has targetIPs (timeout=${timeout}s)"
  while true; do
    ip=$(kubectl get kaiselrule "$name" -n "$ns" \
      -o jsonpath='{.spec.targetIPs[0]}' 2>/dev/null || true)

    if [[ -n "$ip" ]]; then
      echo "    KaiselRule ${name} targetIPs[0]=${ip}"
      return 0
    fi

    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: timed out waiting for KaiselRule ${ns}/${name} to have targetIPs" >&2
      kubectl get kaiselrule "$name" -n "$ns" -o yaml 2>/dev/null >&2 || true
      kubectl get shadowtest "$name" -n "$ns" -o yaml 2>/dev/null >&2 || true
      return 1
    fi

    echo "    waiting KaiselRule ${ns}/${name} IPs (${elapsed}s/${timeout}s)..."
    sleep 3
    elapsed=$((elapsed + 3))
  done
}

# Deploy the kaisel DaemonSet into the cluster.
# Patches the container image to ${KAISEL_IMG} if it differs from the manifest
# default (kaisel:latest), so locally-built dev images are used automatically.
kaisel_daemonset_deploy() {
  echo "==> [kaisel] deploying DaemonSet from ${KAISEL_DEPLOY_DIR}"
  kubectl apply -k "$KAISEL_DEPLOY_DIR"
  if [[ -n "${KAISEL_IMG:-}" ]] && [[ "${KAISEL_IMG}" != "kaisel:latest" ]]; then
    kubectl set image daemonset/kaisel kaisel="${KAISEL_IMG}" -n "$KAISEL_NS"
  fi
  # Force pod restart so a rebuilt image with the same tag is picked up.
  kubectl rollout restart daemonset/kaisel -n "$KAISEL_NS" 2>/dev/null || true
}

# Wait for at least one kaisel DaemonSet pod to reach Running.
# Usage: kaisel_daemonset_wait_ready [timeout_seconds]
kaisel_daemonset_wait_ready() {
  local timeout="${1:-120}"
  echo "==> [kaisel] wait DaemonSet pod Running (timeout=${timeout}s)"
  kubectl rollout status daemonset/kaisel -n "$KAISEL_NS" --timeout="${timeout}s"
}

# Tear down the kaisel DaemonSet and its namespace.
kaisel_daemonset_teardown() {
  kubectl delete -k "$KAISEL_DEPLOY_DIR" --ignore-not-found || true
}

# Assert at least one kaisel pod log line matches pattern.
# Collects logs from all nodes; fails if pattern absent.
# Usage: kaisel_assert_captured <grep_pattern>
kaisel_assert_captured() {
  local pattern="$1"
  local logs
  logs=$(kubectl logs -l app=kaisel -n "$KAISEL_NS" --tail=500 2>/dev/null || true)
  if echo "$logs" | grep -q "$pattern"; then
    return 0
  fi
  echo "FAIL: kaisel pod logs missing pattern: ${pattern}" >&2
  echo "--- kaisel logs ---" >&2
  echo "$logs" >&2
  return 1
}

# Assert kaisel pod logs do NOT contain pattern.
kaisel_assert_not_captured() {
  local pattern="$1"
  local logs
  logs=$(kubectl logs -l app=kaisel -n "$KAISEL_NS" --tail=500 2>/dev/null || true)
  if ! echo "$logs" | grep -q "$pattern"; then
    return 0
  fi
  echo "FAIL: kaisel pod logs unexpectedly match: ${pattern}" >&2
  echo "$logs" | grep "$pattern" >&2
  return 1
}
