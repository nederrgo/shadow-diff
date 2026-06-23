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
# (e.g. ./testing/scripts/e2e-reset-kind.sh) where ~/.bashrc may return early.
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
# Uses `docker ps` with a timeout — on WSL, `docker info` often hangs while `docker ps` still works.
require_docker() {
  require_cmd docker
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
    echo "         ./testing/scripts/e2e-reset-minikube.sh" >&2
    echo "         ./testing/scripts/e2e-reset-kind.sh" >&2
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

# scale_down_recorder_replicasets_not_matching scales ReplicaSets whose recorder
# container image differs from want_image to zero replicas. Use after kubectl set
# image or a partial rollout left an old RS at desired=1 with ErrImagePull on Kind.
scale_down_recorder_replicasets_not_matching() {
  local shadow_ns="$1" deploy_name="$2" want_image="$3"
  local rs name img
  while IFS= read -r rs; do
    [[ -z "$rs" ]] && continue
    name="${rs#replicaset.apps/}"
    img=$(kubectl get "$rs" -n "$shadow_ns" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true)
    if [[ -n "$img" && "$img" != "$want_image" ]]; then
      echo "    scale down stale recorder RS $name (image=$img, want=$want_image)"
      kubectl scale "$rs" --replicas=0 -n "$shadow_ns" >/dev/null
    fi
  done < <(kubectl get rs -n "$shadow_ns" -o name 2>/dev/null | grep "^replicaset.apps/${deploy_name}-" || true)
}

# wait_recorder_rollout waits for the Recorder Deployment after ensuring the
# ShadowTest spec and Deployment template use want_image (avoids rollout hang on
# recorder:latest pods that cannot pull on Kind).
wait_recorder_rollout() {
  local shadowtest="$1" shadowtest_ns="$2" shadow_ns="$3" want_image="$4" timeout="${5:-120s}"
  local deploy="${shadowtest}-recorder"
  if ! kubectl get deploy "$deploy" -n "$shadow_ns" >/dev/null 2>&1; then
    return 0
  fi
  scale_down_recorder_replicasets_not_matching "$shadow_ns" "$deploy" "$want_image"
  kubectl rollout status "deployment/${deploy}" -n "$shadow_ns" --timeout="$timeout" 2>/dev/null || true
}

# shadow_app_pod_for_role returns a shadow worker pod (app container), not a
# spec.dependencies pod (rabbitmq-control-a also carries shadow-diff.io/role).
shadow_app_pod_for_role() {
  local shadow_ns="$1" shadowtest="$2" role="$3"
  kubectl get pods -n "$shadow_ns" \
    -l "shadow-diff.io/shadowtest-name=${shadowtest},shadow-diff.io/role=${role},shadow-diff.io/resource-kind!=dependency" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
}

# Kind or Minikube cluster detection and image load (shared by ingress/E2E scripts).
e2e_detect_cluster() {
  if minikube -p "${MINIKUBE_PROFILE:-minikube}" status --format='{{.Host}}' 2>/dev/null | grep -qi running; then
    echo minikube
    return
  fi
  if kind get clusters 2>/dev/null | grep -q .; then
    echo kind
    return
  fi
  echo unknown
}

e2e_init_cluster() {
  local repo="$1"
  E2E_CLUSTER="${E2E_CLUSTER:-$(e2e_detect_cluster)}"
  if [[ "$E2E_CLUSTER" == minikube ]]; then
    # shellcheck source=testing/scripts/lib/cluster-minikube.sh
    source "$repo/testing/scripts/lib/cluster-minikube.sh"
  fi
  echo "==> E2E cluster: ${E2E_CLUSTER}"
}

e2e_prepare_docker_build() {
  if [[ "${E2E_CLUSTER:-}" == minikube && "${MINIKUBE_DRIVER:-kvm2}" != none ]]; then
    use_minikube_docker_env
  fi
}

e2e_load_image() {
  local img="$1"
  [[ "${SKIP_LOAD:-0}" == "1" ]] && return 0
  case "${E2E_CLUSTER:-unknown}" in
    minikube)
      if [[ "${MINIKUBE_DRIVER:-kvm2}" == none ]]; then
        load_minikube_image "$img"
      else
        use_minikube_docker_env
        docker image inspect "$img" >/dev/null 2>&1 || {
          log_fail "missing image ${img} in minikube docker — build or unset SKIP_LOAD"
          exit 1
        }
      fi
      ;;
    kind)
      require_cmd kind
      local cluster="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1)}"
      [[ -n "$cluster" ]] || { log_fail "no Kind cluster; set KIND_CLUSTER"; exit 1; }
      kind load docker-image "$img" --name "$cluster"
      ;;
    *)
      log_fail "need kind or minikube cluster (detected: ${E2E_CLUSTER})"
      exit 1
      ;;
  esac
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
