#!/usr/bin/env bash
# Bootstrap Minikube for Pixie eBPF + install Vizier (pl) + pixie-gate Deployment.
#
# Prerequisites:
#   - Linux host with kvm2 (recommended) or virtualbox Minikube driver
#   - Free Pixie Cloud account (PIXIE_API_KEY or px auth login)
#   - pixie-gate image available to the cluster (PIXIE_GATE_IMG, default pixie-gate:dev)
#
# Usage:
#   MINIKUBE_DRIVER=kvm2 PIXIE_API_KEY=... ./testing/bats/setup/setup-local-pixie.sh
#   ./testing/bats/setup/setup-local-pixie.sh --skip-minikube-start
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../../.." && pwd)}"
cd "$REPO"

# shellcheck source=testing/bats/helpers/cluster-minikube.sh
source "$REPO/testing/bats/helpers/cluster-minikube.sh"
# shellcheck source=testing/bats/helpers/pixie-bridge.sh
source "$REPO/testing/bats/helpers/pixie-bridge.sh"

SKIP_MINIKUBE_START=0
SKIP_PIXIE_INSTALL=0
NO_BRIDGE=0

usage() {
  sed -n '2,14p' "$0"
  echo "Flags: --skip-minikube-start --skip-pixie-install --no-bridge -h"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-minikube-start) SKIP_MINIKUBE_START=1 ;;
    --skip-pixie-install)  SKIP_PIXIE_INSTALL=1 ;;
    --no-bridge)           NO_BRIDGE=1 ;;
    --foreground-bridge)
      echo "WARN: --foreground-bridge removed; pixie-gate runs as an in-cluster Deployment" >&2
      ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown flag: $1" >&2; usage; exit 1 ;;
  esac
  shift
done

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "ERROR: missing command: $1" >&2; exit 1; }
}

for cmd in kubectl curl jq; do
  need "$cmd"
done
require_minikube

# Pixie path: VM driver (ignore inherited MINIKUBE_DRIVER=none from a prior shell).
if [[ "${MINIKUBE_DRIVER:-}" == none ]] || ! pixie_supported_minikube_driver "${MINIKUBE_DRIVER:-}"; then
  unset MINIKUBE_DRIVER
fi
if [[ -z "${MINIKUBE_DRIVER:-}" ]]; then
  MINIKUBE_DRIVER=$(resolve_pixie_minikube_driver) || {
    echo "ERROR: no Pixie-compatible minikube driver (need kvm2 or virtualbox)" >&2
    exit 1
  }
  export MINIKUBE_DRIVER
fi
require_pixie_minikube_driver
assert_minikube_driver_compatible "$MINIKUBE_DRIVER"

echo "==> Local Pixie eBPF setup (driver=${MINIKUBE_DRIVER}, profile=${MINIKUBE_PROFILE})"

if [[ "$SKIP_MINIKUBE_START" -eq 0 ]]; then
  export MINIKUBE_CNI="${MINIKUBE_CNI:-flannel}"
  if ! minikube -p "$MINIKUBE_PROFILE" status --format='{{.Host}}' 2>/dev/null | grep -qi running; then
    echo "==> Start Minikube for Pixie (driver=${MINIKUBE_DRIVER}, cni=${MINIKUBE_CNI})"
    if [[ "$MINIKUBE_DRIVER" == kvm2 ]]; then
      _ensure_kvm2_libvirt_group
      _ensure_libvirtd_running
    fi
    start_args=(start --driver="$MINIKUBE_DRIVER" --cni="$MINIKUBE_CNI" --cpus=4 --memory=8192)
    err=""
    if ! err=$(_minikube_start "$MINIKUBE_DRIVER" "${start_args[@]}" 2>&1); then
      _minikube_start_failed "$MINIKUBE_DRIVER" "$err"
    fi
  fi
  verify_pixie_ebpf_ready
else
  echo "==> Skip minikube start (--skip-minikube-start)"
  verify_pixie_ebpf_ready
fi

kubectl config use-context "$MINIKUBE_CONTEXT" >/dev/null 2>&1 || true

ensure_px_cli

if [[ "$SKIP_PIXIE_INSTALL" -eq 0 ]]; then
  install_pixie_vizier
  wait_pixie_vizier_ready 300
else
  echo "==> Skip Pixie install (--skip-pixie-install)"
  if ! pixie_vizier_installed; then
    echo "ERROR: no Vizier in namespace ${PIXIE_NAMESPACE} — omit --skip-pixie-install and run px auth login first" >&2
    exit 1
  fi
  wait_pixie_vizier_ready 120
fi

if [[ "$NO_BRIDGE" -eq 1 ]]; then
  echo "==> Skip pixie-gate Deployment (--no-bridge)"
  echo "    Deploy manually: kubectl apply -k pipeline/pixie-gate/deploy/"
  exit 0
fi

deploy_pixie_gate

echo ""
echo "Pixie local sandbox ready (Vizier + pixie-gate)."
echo "  Next: ./testing/tools/e2e-reset-minikube.sh --no-reset"
echo "  Then: curl prod Service with traceparent (see docs/verification/VERIFICATION.md)"
