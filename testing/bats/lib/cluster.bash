# Thin wrapper over cluster-minikube.sh / cluster-kind.sh for Bats suites.
# shellcheck shell=bash

bats_source_cluster_helpers() {
  local cluster="${E2E_CLUSTER:-minikube}"
  if [[ "$cluster" == kind ]]; then
    # shellcheck source=testing/bats/helpers/cluster-kind.sh
    source "${REPO}/testing/bats/helpers/cluster-kind.sh"
  else
    # shellcheck source=testing/bats/helpers/cluster-minikube.sh
    source "${REPO}/testing/bats/helpers/cluster-minikube.sh"
  fi
}

bats_source_e2e_helpers() {
  # shellcheck source=testing/bats/helpers/e2e-helpers.sh
  source "${REPO}/testing/bats/helpers/e2e-helpers.sh"
}

bats_ensure_cluster() {
  bats_source_cluster_helpers
  bats_source_e2e_helpers
  if [[ "${E2E_CLUSTER:-minikube}" == kind ]]; then
    ensure_kind_ready
  else
    ensure_minikube_ready
  fi
}

# True when the selected E2E_CLUSTER is up (Kind list or Minikube Host running).
bats_cluster_running() {
  bats_source_cluster_helpers
  if [[ "${E2E_CLUSTER:-minikube}" == kind ]]; then
    kind_cluster_running
  else
    bats_minikube_running
  fi
}

# Always Minikube — kept for leftover callers until Phase 2e; platform uses bats_ensure_cluster.
bats_ensure_minikube() {
  # shellcheck source=testing/bats/helpers/cluster-minikube.sh
  source "${REPO}/testing/bats/helpers/cluster-minikube.sh"
  bats_source_e2e_helpers
  ensure_minikube_ready
}

bats_minikube_running() {
  # shellcheck source=testing/bats/helpers/cluster-minikube.sh
  source "${REPO}/testing/bats/helpers/cluster-minikube.sh"
  minikube -p "${MINIKUBE_PROFILE:-minikube}" status --format='{{.Host}}' 2>/dev/null | grep -qi running
}
