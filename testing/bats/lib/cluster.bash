# Thin wrapper over cluster-minikube.sh for Bats suites.
# shellcheck shell=bash

bats_source_cluster_helpers() {
  # shellcheck source=testing/scripts/helpers/cluster-minikube.sh
  source "${REPO}/testing/scripts/helpers/cluster-minikube.sh"
}

bats_source_e2e_helpers() {
  # shellcheck source=testing/scripts/helpers/e2e-helpers.sh
  source "${REPO}/testing/scripts/helpers/e2e-helpers.sh"
}

bats_ensure_minikube() {
  bats_source_cluster_helpers
  bats_source_e2e_helpers
  ensure_minikube_ready
}

bats_minikube_running() {
  bats_source_cluster_helpers
  minikube -p "${MINIKUBE_PROFILE:-minikube}" status --format='{{.Host}}' 2>/dev/null | grep -qi running
}
