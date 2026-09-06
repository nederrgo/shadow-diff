# Thin wrapper over cluster-kind.sh for Bats suites.
# shellcheck shell=bash

bats_source_cluster_helpers() {
  # shellcheck source=testing/bats/helpers/cluster-kind.sh
  source "${REPO}/testing/bats/helpers/cluster-kind.sh"
}

bats_source_e2e_helpers() {
  # shellcheck source=testing/bats/helpers/e2e-helpers.sh
  source "${REPO}/testing/bats/helpers/e2e-helpers.sh"
}

bats_ensure_cluster() {
  bats_source_cluster_helpers
  bats_source_e2e_helpers
  ensure_kind_ready
}

# True when the Kind cluster is up.
bats_cluster_running() {
  bats_source_cluster_helpers
  kind_cluster_running
}
