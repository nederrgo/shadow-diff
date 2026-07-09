# Phase 1 platform bootstrap — idempotent, flock-guarded.
# shellcheck shell=bash

bats_source_pixie_helpers() {
  # shellcheck source=testing/scripts/helpers/pixie-bridge.sh
  source "${REPO}/testing/scripts/helpers/pixie-bridge.sh"
}

bats_platform_state_read() {
  local key="$1" file="${BATS_STATE_DIR}/platform.state"
  [[ -f "$file" ]] || return 1
  grep -E "^${key}=" "$file" 2>/dev/null | head -1 | cut -d= -f2-
}

bats_platform_state_write() {
  mkdir -p "${BATS_STATE_DIR}"
  local file="${BATS_STATE_DIR}/platform.state"
  : >"$file"
  {
    echo "BOOTSTRAPPED=1"
    echo "MONARCH_IMG=${MONARCH_IMG}"
    echo "BERU_IMG=${BERU_IMG}"
    echo "TIMESTAMP=$(date +%s)"
    local pid
    pid=$(_pixie_bridge_running_pid 2>/dev/null || true)
    [[ -n "$pid" ]] && echo "BRIDGE_PID=${pid}"
  } >>"$file"
}

bats_platform_state_valid() {
  [[ "${BATS_FORCE_PLATFORM_BOOTSTRAP:-0}" == "1" ]] && return 1
  [[ "$(bats_platform_state_read BOOTSTRAPPED)" == "1" ]] || return 1
  [[ "$(bats_platform_state_read MONARCH_IMG)" == "$MONARCH_IMG" ]] || return 1
  return 0
}

bats_platform_with_flock() {
  local fn="$1"
  mkdir -p "${BATS_STATE_DIR}"
  local lock="${BATS_STATE_DIR}/platform.lock"
  exec 9>"$lock"
  flock 9
  "$fn"
  exec 9>&-
}

platform_health_matrix() {
  local ok=1
  bats_source_e2e_helpers
  bats_source_cluster_helpers
  bats_source_pixie_helpers

  bats_minikube_running || { echo "health: minikube not running" >&2; ok=0; }
  kubectl cluster-info >/dev/null 2>&1 || { echo "health: kubectl cluster unreachable" >&2; ok=0; }
  kubectl get crd shadowtests.engine.shadow-diff.io >/dev/null 2>&1 || { echo "health: ShadowTest CRD missing" >&2; ok=0; }
  kubectl get deploy monarch-controller-manager -n monarch-system >/dev/null 2>&1 || { echo "health: Monarch deploy missing" >&2; ok=0; }
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=60s >/dev/null 2>&1 || { echo "health: Monarch not ready" >&2; ok=0; }
  kubectl get deploy beru -n beru-system >/dev/null 2>&1 || { echo "health: Beru deploy missing" >&2; ok=0; }

  if pixie_vizier_installed; then
    pixie_vizier_healthy || { echo "health: Pixie not CS_HEALTHY" >&2; ok=0; }
  fi
  _pixie_bridge_running_pid "${PIXIE_BRIDGE_STATE_DIR}/bridge.pid" >/dev/null 2>&1 || { echo "health: pixie-stream-bridge not running" >&2; ok=0; }

  [[ "$ok" == "1" ]]
}

platform_bootstrap_install() {
  echo "==> [bats] platform bootstrap"
  bats_source_e2e_helpers
  bats_source_cluster_helpers
  bats_source_pixie_helpers

  bats_ensure_minikube

  if [[ "${SKIP_PLATFORM_BOOTSTRAP:-0}" == "1" ]]; then
    platform_health_matrix || return 1
    return 0
  fi

  make -C "${REPO}/pipeline/monarch" install
  make -C "${REPO}/pipeline/monarch" deploy IMG="${MONARCH_IMG}"
  kubectl set env deployment/monarch-controller-manager -n monarch-system \
    MONARCH_MODE=dev BERU_IMAGE="${BERU_IMG}" SHOP_IMAGE="${SHOP_IMG}" >/dev/null 2>&1 || true
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s

  kubectl apply -f "${REPO}/pipeline/beru/deploy/"

  if ! pixie_vizier_installed; then
    MINIKUBE_DRIVER="${MINIKUBE_DRIVER}" \
      "${REPO}/testing/scripts/setup/setup-local-pixie.sh" --skip-minikube-start --no-bridge
  else
    wait_pixie_vizier_healthy 120 || true
  fi

  wait_pixie_http_events_ready 180 2>/dev/null || true

  "${REPO}/testing/scripts/setup/start-pixie-stream-bridge.sh"

  kubectl apply -f "${REPO}/pipeline/siphon/deploy/rbac.yaml"
}

_ensure_platform_ready_body() {
  if bats_platform_state_valid && platform_health_matrix; then
    echo "==> [bats] platform already healthy (skip install)"
    return 0
  fi
  build_test_images_if_needed || return 1
  load_test_images_if_needed || return 1
  platform_bootstrap_install || return 1
  platform_health_matrix || return 1
  bats_platform_state_write
  date +%s >"${BATS_STATE_DIR}/platform.health"
}

ensure_platform_ready() {
  bats_init_env
  bats_platform_with_flock _ensure_platform_ready_body
}

build_test_images_if_needed() {
  bats_init_env
  [[ "${SKIP_BUILD:-0}" == "1" ]] && return 0

  bats_source_e2e_helpers
  bats_source_cluster_helpers
  if [[ "${MINIKUBE_DRIVER:-kvm2}" != none ]]; then
    use_minikube_docker_env
  fi
  require_docker || {
    echo "HINT: if images are already in minikube docker, rerun with SKIP_BUILD=1 SKIP_LOAD=1" >&2
    return 1
  }

  echo "==> [bats] build container images"
  make -C "${REPO}/pipeline/monarch" docker-build IMG="${MONARCH_IMG}"
  make -C "${REPO}/pipeline/beru" docker-build BERU_IMG="${BERU_IMG}"
  make -C "${REPO}/pipeline/shop" docker-build SHOP_IMG="${SHOP_IMG}"
  make -C "${REPO}/pipeline/igrises/igris-http" docker-build IGRIS_IMG="${IGRIS_IMG}"
  make -C "${REPO}/pipeline/siphon" docker-build SIPHON_IMG="${SIPHON_IMG}"
  make -C "${REPO}/pipeline/recorder" docker-build RECORDER_IMG="${RECORDER_IMG}" 2>/dev/null || true
  make -C "${REPO}/pipeline/igrises/igris-rabbitmq" docker-build IGRIS_RABBITMQ_IMG="${IGRIS_RABBITMQ_IMG}"
  make -C "${REPO}/pipeline/egress-relay-rabbitmq" docker-build EGRESS_RELAY_RABBITMQ_IMG="${EGRESS_RELAY_RABBITMQ_IMG}"
  make -C "${REPO}/testing/example-apps/python-test-worker" docker-build PYTHON_TEST_WORKER_IMG="${PYTHON_TEST_WORKER_IMG}"
  make -C "${REPO}/testing/example-apps/nodejs-hybrid-worker" docker-build NODEJS_HYBRID_WORKER_IMG="${NODEJS_HYBRID_WORKER_IMG}" 2>/dev/null || true
  make -C "${REPO}/testing/example-apps/http-rmq-python-worker" docker-build HTTP_RMQ_PYTHON_IMG="${HTTP_RMQ_PYTHON_WORKER_IMG}" 2>/dev/null || true
  make -C "${REPO}/testing/example-apps/http-rmq-test-app" docker-build HTTP_RMQ_TEST_IMG="${HTTP_RMQ_NODEJS_WORKER_IMG}" 2>/dev/null || true
}

load_test_images_if_needed() {
  bats_init_env
  [[ "${SKIP_LOAD:-0}" == "1" ]] && return 0
  bats_source_e2e_helpers
  bats_source_cluster_helpers
  if [[ "${MINIKUBE_DRIVER:-kvm2}" != none ]]; then
    use_minikube_docker_env
  fi
  for img in "$MONARCH_IMG" "$BERU_IMG" "$SHOP_IMG" "$IGRIS_IMG" "$SIPHON_IMG" "$RECORDER_IMG" \
    "$IGRIS_RABBITMQ_IMG" "$EGRESS_RELAY_RABBITMQ_IMG" "$PYTHON_TEST_WORKER_IMG" \
    "$NODEJS_HYBRID_WORKER_IMG" "$HTTP_RMQ_PYTHON_WORKER_IMG" "$HTTP_RMQ_NODEJS_WORKER_IMG" \
    "$MONGO_IMAGE"; do
    e2e_load_image "$img" 2>/dev/null || docker pull "$img" 2>/dev/null || true
  done
  docker pull rabbitmq:3-management-alpine 2>/dev/null || true
}
