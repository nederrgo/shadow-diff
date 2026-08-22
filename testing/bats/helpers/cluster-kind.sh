# Kind cluster bootstrap for Monarch local E2E.
# Source from bats helpers; do not execute directly.
#
# Reuses an existing cluster named KIND_CLUSTER. Does not delete/recreate.
# If the cluster lacks extraPortMappings from kind/config.yaml (18080→30080,
# 15432→30432), recreate manually:
#   kind delete cluster --name "${KIND_CLUSTER:-shadow-diff}"

KIND_CLUSTER="${KIND_CLUSTER:-shadow-diff}"
KIND_CONTEXT="${KIND_CONTEXT:-kind-${KIND_CLUSTER}}"
KIND_CONFIG="${KIND_CONFIG:-${REPO}/testing/bats/kind/config.yaml}"

# WSL + Docker Desktop: /var/run/docker.sock is often a dead bind while the
# real engine is under /mnt/wsl/docker-desktop-bind-mounts/<distro>/docker.sock.
# Export DOCKER_HOST so kind and later docker builds/loads share one daemon.
_ensure_docker_host() {
  if docker info >/dev/null 2>&1; then
    return 0
  fi
  local sock
  for sock in /mnt/wsl/docker-desktop-bind-mounts/*/docker.sock; do
    [[ -S "$sock" ]] || continue
    if DOCKER_HOST="unix://${sock}" docker info >/dev/null 2>&1; then
      export DOCKER_HOST="unix://${sock}"
      echo "==> DOCKER_HOST=${DOCKER_HOST} (WSL Docker Desktop bind-mount)"
      return 0
    fi
  done
  return 1
}

require_kind() {
  command -v kind >/dev/null 2>&1 || {
    echo "ERROR: kind not found (install: https://kind.sigs.k8s.io/docs/user/quick-start/)" >&2
    return 1
  }
  command -v docker >/dev/null 2>&1 || {
    echo "ERROR: docker not found (kind needs a working docker daemon)" >&2
    return 1
  }
  if ! _ensure_docker_host; then
    echo "ERROR: docker daemon not reachable (start Docker Desktop; enable WSL integration for this distro)" >&2
    return 1
  fi
}

kind_cluster_running() {
  kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER"
}

load_kind_image() {
  local img="$1"
  [[ -n "$img" ]] || {
    echo "ERROR: load_kind_image requires an image name" >&2
    return 1
  }
  echo "==> kind load docker-image ${img} --name ${KIND_CLUSTER}"
  kind load docker-image "$img" --name "$KIND_CLUSTER"
}

ensure_kind_ready() {
  require_kind || return 1

  if [[ -z "${REPO:-}" ]]; then
    echo "ERROR: REPO unset (call bats_init_env before ensure_kind_ready)" >&2
    return 1
  fi

  if ! kind_cluster_running; then
    [[ -f "$KIND_CONFIG" ]] || {
      echo "ERROR: Kind config not found: ${KIND_CONFIG}" >&2
      return 1
    }
    echo "==> Create Kind cluster ${KIND_CLUSTER} (config=${KIND_CONFIG})"
    kind create cluster --name "$KIND_CLUSTER" --config "$KIND_CONFIG" || {
      echo "ERROR: kind create cluster failed" >&2
      return 1
    }
  else
    echo "==> Kind cluster ${KIND_CLUSTER} already exists — reuse"
    echo "    (recreate with kind delete cluster --name ${KIND_CLUSTER} if host:18080 or host:15432 mappings are missing)"
  fi

  kubectl config use-context "$KIND_CONTEXT" >/dev/null 2>&1 || {
    echo "ERROR: kubectl context '${KIND_CONTEXT}' not found after Kind ready" >&2
    return 1
  }

  echo "==> Wait for Kind API server (context=${KIND_CONTEXT})"
  local i
  for i in $(seq 1 60); do
    if kubectl cluster-info --context "$KIND_CONTEXT" >/dev/null 2>&1; then
      echo "==> Kind ready (cluster=${KIND_CLUSTER}, context=${KIND_CONTEXT})"
      return 0
    fi
    sleep 2
  done
  echo "ERROR: kubectl cannot reach Kind API (context=${KIND_CONTEXT})" >&2
  return 1
}
