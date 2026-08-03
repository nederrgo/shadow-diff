# Shared helpers for Monarch E2E bash scripts.
# shellcheck shell=bash

log_success() {
  echo "[SUCCESS] $*"
}

log_fail() {
  echo "[FAIL] $*" >&2
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    log_fail "missing command: $1"
    exit 1
  }
}

# ensure_go_path puts the Go toolchain and GOPATH/bin on PATH for non-login shells
# (e.g. scripts run from CI or reset scripts) where ~/.bashrc may return early.
ensure_go_path() {
  if [[ -d /usr/local/go/bin ]] && ! command -v go >/dev/null 2>&1; then
    export PATH="/usr/local/go/bin:$PATH"
  fi
  if command -v go >/dev/null 2>&1; then
    local gopath
    gopath="$(go env GOPATH 2>/dev/null || echo "${HOME}/go")"
    export PATH="${gopath}/bin:${PATH}"
  fi
  if ! command -v go >/dev/null 2>&1; then
    log_fail "Go is not installed or not on PATH (expected /usr/local/go/bin/go)"
    echo "       Install Go or: export PATH=\"/usr/local/go/bin:\$PATH\"" >&2
    exit 1
  fi
}

# require_docker ensures the Docker daemon is reachable (Kind image builds/loads need it).
# When E2E_CLUSTER=minikube we check Minikube's daemon (not the host socket); the host
# Docker Desktop being down is irrelevant in that case because all builds target Minikube.
require_docker() {
  require_cmd docker
  if [[ "${E2E_CLUSTER:-}" == minikube && "${MINIKUBE_DRIVER:-kvm2}" != none ]]; then
    local minikube_env
    minikube_env=$(minikube docker-env 2>/dev/null) || {
      log_fail "minikube docker-env failed — is minikube running? (minikube start)"
      exit 1
    }
    eval "$minikube_env"
    if ! timeout 15 docker ps >/dev/null 2>&1; then
      log_fail "Minikube Docker daemon is not reachable after 'minikube docker-env'"
      exit 1
    fi
    return 0
  fi
  if ! timeout 15 docker ps >/dev/null 2>&1; then
    log_fail "Docker daemon is not reachable (docker ps failed or timed out after 15s)"
    echo "       On WSL: start Docker Desktop on Windows and wait until 'docker ps' succeeds." >&2
    echo "       If Docker Desktop is running, try: wsl --shutdown (from PowerShell), then reopen WSL." >&2
    echo "       Low memory can stall Docker — close other apps or raise WSL memory in ~/.wslconfig." >&2
    exit 1
  fi
}

# require_kubectl_cluster ensures kubeconfig points at a live API server.
require_kubectl_cluster() {
  require_cmd kubectl
  if ! kubectl cluster-info >/dev/null 2>&1; then
    log_fail "kubectl cannot reach the Kubernetes API (connection refused or stale kubeconfig)"
    echo "       Recreate the cluster stack:" >&2
    echo "         ./testing/tools/e2e-reset-minikube.sh" >&2
    echo "       Or point kubeconfig at a running cluster: export KUBECONFIG=..." >&2
    exit 1
  fi
}

# wait_shadowtest_gone blocks until the ShadowTest CR is fully removed.
# No-op when the CR does not exist, or exists without a deletionTimestamp (live object).
wait_shadowtest_gone() {
  local name="$1" ns="$2" max_wait="${3:-180}"
  if ! kubectl get shadowtest "$name" -n "$ns" >/dev/null 2>&1; then
    return 0
  fi
  local deleting
  deleting=$(kubectl get shadowtest "$name" -n "$ns" -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null || true)
  if [[ -z "$deleting" ]]; then
    return 0
  fi
  local i=0
  while kubectl get shadowtest "$name" -n "$ns" >/dev/null 2>&1; do
    i=$((i + 2))
    if [[ "$i" -gt "$max_wait" ]]; then
      log_fail "timed out waiting for ShadowTest $ns/$name to finish deleting"
      kubectl get shadowtest "$name" -n "$ns" -o yaml 2>/dev/null | tail -25 >&2 || true
      echo "       To force-remove finalizer (last resort):" >&2
      echo "         kubectl patch shadowtest $name -n $ns --type=merge -p '{\"metadata\":{\"finalizers\":[]}}'" >&2
      return 1
    fi
    echo "    waiting for ShadowTest $ns/$name to finish deleting (${i}s/${max_wait}s)..."
    sleep 2
  done
  return 0
}

# shadow_app_pod_for_role returns a shadow worker pod (app container), not a
# spec.dependencies pod (rabbitmq-control-a also carries shadow-diff.io/role).
shadow_app_pod_for_role() {
  local shadow_ns="$1" shadowtest="$2" role="$3"
  kubectl get pods -n "$shadow_ns" \
    -l "shadow-diff.io/shadowtest-name=${shadowtest},shadow-diff.io/role=${role},shadow-diff.io/resource-kind!=dependency" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
}

e2e_init_cluster() {
  local repo="$1"
  # shellcheck source=testing/bats/helpers/cluster-minikube.sh
  source "$repo/testing/bats/helpers/cluster-minikube.sh"
  echo "==> E2E cluster: minikube"
}

e2e_prepare_docker_build() {
  if [[ "${MINIKUBE_DRIVER:-kvm2}" != none ]]; then
    use_minikube_docker_env
  fi
}

e2e_load_image() {
  local img="$1"
  [[ "${SKIP_LOAD:-0}" == "1" ]] && return 0
  if [[ "${MINIKUBE_DRIVER:-kvm2}" == none ]]; then
    load_minikube_image "$img"
  else
    use_minikube_docker_env
    # return (not exit): load_test_images_if_needed falls back to docker pull
    docker image inspect "$img" >/dev/null 2>&1 || {
      echo "missing image ${img} in minikube docker" >&2
      return 1
    }
  fi
}

e2e_strip_kubectl_run_output() {
  local out="$1"
  echo "$out" | grep -v '^pod "' | grep -v '^If you don' | grep -v '^All commands' | grep -v '^Defaulted container' | grep -v 'credentials and sensitive'
}

e2e_in_cluster_curl() {
  local name="$1"
  shift
  local out
  out=$(kubectl run "$name" --rm -i --restart=Never -n default \
    --image=curlimages/curl:latest -- "$@" 2>&1) || true
  e2e_strip_kubectl_run_output "$out"
}

# Monarch provisions deployment/beru-local per ShadowTest.
wait_local_beru_rollout() {
  local shadow_ns="$1" timeout="${2:-120s}"
  echo "==> Wait for beru-local in ${shadow_ns}"
  kubectl wait --namespace="$shadow_ns" --for=condition=available --timeout="$timeout" \
    deployment/beru-local
  kubectl rollout status deployment/beru-local -n "$shadow_ns" --timeout="$timeout"
  log_success "beru-local ready in ${shadow_ns}"
}
