# Phase 1 platform bootstrap — idempotent, flock-guarded.
# shellcheck shell=bash

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

  bats_minikube_running || { echo "health: minikube not running" >&2; ok=0; }
  kubectl cluster-info >/dev/null 2>&1 || { echo "health: kubectl cluster unreachable" >&2; ok=0; }
  kubectl get crd shadowtests.engine.shadow-diff.io >/dev/null 2>&1 || { echo "health: ShadowTest CRD missing" >&2; ok=0; }
  kubectl get deploy monarch-controller-manager -n monarch-system >/dev/null 2>&1 || { echo "health: Monarch deploy missing" >&2; ok=0; }
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=60s >/dev/null 2>&1 || { echo "health: Monarch not ready" >&2; ok=0; }
  kubectl get daemonset kaisel -n kaisel-system >/dev/null 2>&1 || { echo "health: Kaisel DaemonSet missing" >&2; ok=0; }
  kubectl rollout status daemonset/kaisel -n kaisel-system --timeout=60s >/dev/null 2>&1 || { echo "health: Kaisel not ready" >&2; ok=0; }

  [[ "$ok" == "1" ]]
}

# Monarch CRD + controller only (used to decide Kaisel-only heal vs full rebuild).
_platform_monarch_healthy() {
  kubectl get crd shadowtests.engine.shadow-diff.io >/dev/null 2>&1 || return 1
  kubectl get deploy monarch-controller-manager -n monarch-system >/dev/null 2>&1 || return 1
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=30s >/dev/null 2>&1
}

_platform_kaisel_healthy() {
  kubectl get daemonset kaisel -n kaisel-system >/dev/null 2>&1 || return 1
  kubectl rollout status daemonset/kaisel -n kaisel-system --timeout=30s >/dev/null 2>&1
}

platform_bootstrap_install() {
  echo "==> [bats] platform bootstrap"
  bats_source_e2e_helpers
  bats_source_cluster_helpers

  bats_ensure_minikube

  if [[ "${SKIP_PLATFORM_BOOTSTRAP:-0}" == "1" ]]; then
    platform_health_matrix || return 1
    return 0
  fi

  make -C "${REPO}/pipeline/monarch" install
  make -C "${REPO}/pipeline/monarch" deploy IMG="${MONARCH_IMG}"
  # Env overrides keep bare local tags; unset helpers would default to ghcr.io/shadow-diff/*.
  kubectl set env deployment/monarch-controller-manager -n monarch-system \
    MONARCH_MODE=dev \
    BERU_IMAGE="${BERU_IMG}" \
    SHOP_IMAGE="${SHOP_IMG}" \
    IGRIS_HTTP_IMAGE="${IGRIS_IMG}" \
    IGRIS_RABBITMQ_IMAGE="${IGRIS_RABBITMQ_IMG}" \
    EGRESS_RELAY_RABBITMQ_IMAGE="${EGRESS_RELAY_RABBITMQ_IMG}" \
    SHADOW_SOLDIER_IMAGE="${SHADOW_SOLDIER_IMG}" >/dev/null 2>&1 || true
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s

  # shellcheck source=testing/bats/lib/kaisel.bash
  source "${REPO}/testing/bats/lib/kaisel.bash"
  echo "==> [bats] Kaisel DaemonSet (${KAISEL_IMG:-kaisel:dev})"
  kaisel_daemonset_deploy
  kaisel_daemonset_wait_ready 120
}

# Redeploy Kaisel only (e.g. after record/kaisel-capture teardown_file).
_platform_heal_kaisel() {
  # shellcheck source=testing/bats/lib/kaisel.bash
  source "${REPO}/testing/bats/lib/kaisel.bash"
  echo "==> [bats] heal Kaisel DaemonSet only (${KAISEL_IMG:-kaisel:dev})"
  kaisel_daemonset_deploy
  kaisel_daemonset_wait_ready 120
}

_ensure_platform_ready_body() {
  if bats_platform_state_valid && platform_health_matrix; then
    echo "==> [bats] platform already healthy (skip install)"
    return 0
  fi

  # Suites that tear down Kaisel (record/kaisel-capture) leave Monarch healthy.
  # Rebuilding every image is unnecessary and often times out setup_file.
  if bats_platform_state_valid && _platform_monarch_healthy && ! _platform_kaisel_healthy; then
    _platform_heal_kaisel || return 1
    platform_health_matrix || return 1
    date +%s >"${BATS_STATE_DIR}/platform.health"
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

# Ensure a :dev image exists in the docker daemon used by the cluster (minikube
# docker-env when driver != none). Builds via make if missing. Fails hard if
# still absent — do not swallow errors (ImagePullBackOff fails ShadowTests).
# Usage: bats_ensure_dev_image <image:tag> <makefile-dir> <MAKE_VAR>
bats_ensure_dev_image() {
  local img="$1" dir="$2" make_var="$3"
  bats_init_env
  bats_source_e2e_helpers
  bats_source_cluster_helpers
  e2e_prepare_docker_build
  require_docker || return 1

  if docker image inspect "$img" >/dev/null 2>&1; then
    echo "==> [bats] image present: ${img}"
    return 0
  fi
  echo "==> [bats] build missing image ${img}"
  make -C "$dir" docker-build "${make_var}=${img}" || return 1
  docker image inspect "$img" >/dev/null 2>&1 || {
    echo "FAIL: ${img} still missing after docker-build in ${dir}" >&2
    return 1
  }
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
  make -C "${REPO}/pipeline/shadow-soldier" docker-build SHADOW_SOLDIER_IMG="${SHADOW_SOLDIER_IMG}"
  make -C "${REPO}/pipeline/tusk" docker-build TUSK_IMG="${TUSK_IMG}"
  make -C "${REPO}/pipeline/igrises/igris-http" docker-build IGRIS_IMG="${IGRIS_IMG}"
  make -C "${REPO}/pipeline/kaisel" docker-build KAISEL_IMG="${KAISEL_IMG:-kaisel:dev}" 2>/dev/null || true
  make -C "${REPO}/pipeline/igrises/igris-rabbitmq" docker-build IGRIS_RABBITMQ_IMG="${IGRIS_RABBITMQ_IMG}"
  make -C "${REPO}/pipeline/egress-relay-rabbitmq" docker-build EGRESS_RELAY_RABBITMQ_IMG="${EGRESS_RELAY_RABBITMQ_IMG}"
  make -C "${REPO}/testing/example-apps/python-test-worker" docker-build PYTHON_TEST_WORKER_IMG="${PYTHON_TEST_WORKER_IMG}"
  make -C "${REPO}/testing/example-apps/nodejs-hybrid-worker" docker-build NODEJS_HYBRID_WORKER_IMG="${NODEJS_HYBRID_WORKER_IMG}" 2>/dev/null || true
  make -C "${REPO}/testing/example-apps/http-rmq-python-worker" docker-build HTTP_RMQ_PYTHON_IMG="${HTTP_RMQ_PYTHON_WORKER_IMG}" 2>/dev/null || true
  make -C "${REPO}/testing/example-apps/http-rmq-test-app" docker-build HTTP_RMQ_TEST_IMG="${HTTP_RMQ_NODEJS_WORKER_IMG}" 2>/dev/null || true
  make -C "${REPO}/testing/example-apps/http-rmq-go-worker" docker-build HTTP_RMQ_GO_IMG="${HTTP_RMQ_GO_WORKER_IMG}" 2>/dev/null || true
}

load_test_images_if_needed() {
  bats_init_env
  [[ "${SKIP_LOAD:-0}" == "1" ]] && return 0
  bats_source_e2e_helpers
  bats_source_cluster_helpers
  if [[ "${MINIKUBE_DRIVER:-kvm2}" != none ]]; then
    use_minikube_docker_env
  fi
  echo "==> [bats] ensure images present in cluster docker"
  local img
  for img in "$MONARCH_IMG" "$BERU_IMG" "$SHOP_IMG" "$SHADOW_SOLDIER_IMG" "$TUSK_IMG" "$IGRIS_IMG" "${KAISEL_IMG:-kaisel:dev}" \
    "$IGRIS_RABBITMQ_IMG" "$EGRESS_RELAY_RABBITMQ_IMG" "$PYTHON_TEST_WORKER_IMG" \
    "$NODEJS_HYBRID_WORKER_IMG" "$HTTP_RMQ_PYTHON_WORKER_IMG" "$HTTP_RMQ_NODEJS_WORKER_IMG" "$HTTP_RMQ_GO_WORKER_IMG" \
    "$MONGO_IMAGE" rabbitmq:3-management-alpine; do
    if e2e_load_image "$img" 2>/dev/null; then
      continue
    fi
    echo "    pulling ${img}"
    docker pull "$img" || {
      echo "FAIL: could not load or pull ${img}" >&2
      return 1
    }
  done
}
