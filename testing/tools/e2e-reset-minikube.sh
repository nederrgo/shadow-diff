#!/usr/bin/env bash
# Reset and deploy the full Monarch E2E stack on Minikube (kvm2 + flannel + Pixie by default).
#
# HTTP ingress capture uses Pixie eBPF. Defaults:
#   MINIKUBE_DRIVER=kvm2 (or virtualbox), MINIKUBE_CNI=flannel, Pixie Vizier bootstrap on.
# Opt out of Pixie install: --skip-pixie
# E2E assertions: make test-bats-e2e (standalone --run-*-test flags were removed).
#
# Override driver: MINIKUBE_DRIVER=virtualbox|kvm2|none
#
# Images: VM drivers use eval $(minikube docker-env); none driver uses host docker + minikube image load.
#
# Port model (do not change without updating manifests):
#   prod pod          -> :80   (HTTP_PORT=80; Kaisel eBPF capture -> igris-http)
#   Igris listener    -> :80   (replays captured prod traffic)
#   Envoy ingress     -> :8888 (Igris multicasts to shadow Services here)
#   shadow app (echo) -> :80   (applicationPort; env copied from prod)
#   Envoy egress proxy -> :15001 (Shop + Recorder always-on)
#
# Usage (from repo root):
#   ./testing/tools/e2e-reset-minikube.sh                 # full reset + deploy + wait Ready
#   ./testing/tools/e2e-reset-minikube.sh --skip-build    # reuse images already in minikube docker
#   ./testing/tools/e2e-reset-minikube.sh --no-reset      # deploy/upgrade only (no deletes; reuses running minikube)
#   ./testing/tools/e2e-reset-minikube.sh --skip-pixie    # Monarch only, no Vizier
#   ./testing/tools/e2e-reset-minikube.sh --skip-load --skip-build --no-reset  # fastest: cluster already up + images present
#
set -euo pipefail

# testing/tools/<script> → repo root is ../..
REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
cd "$REPO"
# shellcheck source=testing/bats/helpers/e2e-helpers.sh
source "$REPO/testing/bats/helpers/e2e-helpers.sh"
# shellcheck source=testing/bats/helpers/cluster-minikube.sh
source "$REPO/testing/bats/helpers/cluster-minikube.sh"
ensure_go_path

MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
BERU_IMG="${BERU_IMG:-beru:dev}"
SHOP_IMG="${SHOP_IMG:-shop:dev}"
IGRIS_IMG="${IGRIS_IMG:-igris-http:dev}"
RECORDER_IMG="${RECORDER_IMG:-recorder:dev}"
PIXIE_GATE_IMG="${PIXIE_GATE_IMG:-pixie-gate:dev}"

SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"

SKIP_BUILD=0
SKIP_LOAD=0
NO_RESET=0
SETUP_PIXIE=1
SKIP_PIXIE=0

usage() {
  # Comment header only (through the blank line before set -euo).
  sed -n '2,25p' "$0"
  echo "Flags: --skip-build --skip-load --no-reset --skip-pixie --setup-pixie -h"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-build) SKIP_BUILD=1 ;;
    --skip-load)  SKIP_LOAD=1 ;;
    --no-reset)   NO_RESET=1 ;;
    --setup-pixie) SETUP_PIXIE=1 ;;
    --skip-pixie)  SKIP_PIXIE=1; SETUP_PIXIE=0 ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "Unknown flag: $1" >&2; usage; exit 1 ;;
  esac
  shift
done

export SKIP_BUILD SKIP_LOAD NO_RESET SETUP_PIXIE SKIP_PIXIE

export SHADOWTEST SHADOWTEST_NS MONARCH_IMG BERU_IMG SHOP_IMG IGRIS_IMG RECORDER_IMG

# Pixie HTTP ingress: VM driver + flannel CNI (avoid calico/flannel mix on same profile).
# shellcheck source=testing/bats/helpers/pixie-bridge.sh
source "$REPO/testing/bats/helpers/pixie-bridge.sh"
export MINIKUBE_CNI="${MINIKUBE_CNI:-flannel}"
if [[ "${SKIP_PIXIE:-0}" -eq 0 ]]; then
  if [[ "${MINIKUBE_DRIVER:-}" == none ]] || ! pixie_supported_minikube_driver "${MINIKUBE_DRIVER:-}"; then
    unset MINIKUBE_DRIVER
  fi
  if [[ -z "${MINIKUBE_DRIVER:-}" ]]; then
    MINIKUBE_DRIVER=$(resolve_pixie_minikube_driver) || {
      echo "ERROR: Pixie E2E needs kvm2 or virtualbox minikube driver (or --skip-pixie)" >&2
      exit 1
    }
    export MINIKUBE_DRIVER
  fi
  require_pixie_minikube_driver
  assert_minikube_driver_compatible "$MINIKUBE_DRIVER"
  assert_minikube_cni_compatible "$MINIKUBE_CNI"
fi

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "ERROR: missing command: $1" >&2; exit 1; }
}

need kubectl
if [[ "$SKIP_BUILD" -eq 0 ]]; then
  need docker
fi
if [[ "$SKIP_LOAD" -eq 0 ]] || [[ "$SKIP_BUILD" -eq 0 ]]; then
  require_minikube
fi

ensure_minikube_ready

if [[ "${SETUP_PIXIE:-1}" -eq 1 && "${SKIP_PIXIE:-0}" -eq 0 ]]; then
  echo "==> Pixie eBPF (Vizier in pl + pixie-gate)"
  chmod +x "$REPO/testing/bats/setup/setup-local-pixie.sh"
  "$REPO/testing/bats/setup/setup-local-pixie.sh" --skip-minikube-start
fi

echo "==> Monarch E2E reset (minikube profile=${MINIKUBE_PROFILE}, driver=${MINIKUBE_DRIVER}, cni=${MINIKUBE_CNI:-flannel})"
echo "    Images: monarch=$MONARCH_IMG beru=$BERU_IMG shop=$SHOP_IMG (beru-local) igris=$IGRIS_IMG recorder=$RECORDER_IMG pixie-gate=$PIXIE_GATE_IMG"
if [[ "$SKIP_BUILD" -eq 1 ]]; then
  echo "WARN: --skip-build reuses existing minikube docker images; code changes are NOT included until you rebuild"
fi

if [[ "$SKIP_BUILD" -eq 0 ]]; then
  use_minikube_docker_env
  if [[ "${MINIKUBE_DRIVER:-}" != none ]]; then
    trap 'unload_minikube_docker_env' EXIT
  fi
fi

if [[ "$SKIP_BUILD" -eq 0 ]]; then
  echo "==> Build container images (minikube docker daemon)"
  if [[ "${MONARCH_NO_CACHE:-0}" == "1" ]]; then
    docker build --no-cache -t "$MONARCH_IMG" "$REPO/pipeline/monarch"
  else
    make -C pipeline/monarch docker-build IMG="$MONARCH_IMG"
  fi
  make beru-docker-build BERU_IMG="$BERU_IMG"
  make shop-docker-build SHOP_IMG="$SHOP_IMG"
  make igris-docker-build IGRIS_IMG="$IGRIS_IMG"
  make recorder-docker-build RECORDER_IMG="$RECORDER_IMG"
  make pixie-gate-docker-build PIXIE_GATE_IMG="$PIXIE_GATE_IMG"
fi

if [[ "${MINIKUBE_DRIVER:-}" == none ]]; then
  echo "==> Sync local images into containerd (none driver)"
  load_minikube_images "$MONARCH_IMG" "$BERU_IMG" "$SHOP_IMG" "$IGRIS_IMG" "$RECORDER_IMG" "$PIXIE_GATE_IMG"
fi

if [[ "${MINIKUBE_DRIVER:-}" == none ]]; then
  export E2E_IMAGE_REBUILD_HINT="After Monarch code fixes: make -C pipeline/monarch docker-build IMG=${MONARCH_IMG} && load_minikube_images ${MONARCH_IMG} && kubectl rollout restart deployment/monarch-controller-manager -n monarch-system"
else
  export E2E_IMAGE_REBUILD_HINT="After Monarch code fixes: eval \$(minikube docker-env) && make -C pipeline/monarch docker-build IMG=${MONARCH_IMG} && kubectl rollout restart deployment/monarch-controller-manager -n monarch-system"
fi
e2e_reset_deploy_stack() {
  echo "==> Monarch CRDs"
  make -C pipeline/monarch install

  if [[ "${NO_RESET:-0}" -eq 0 ]]; then
    echo "==> Delete prior E2E resources (if any)"
    kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=false
    kubectl delete deployment,service my-prod-app -n default --ignore-not-found --wait=false
    kubectl delete service egress-httpbin -n default --ignore-not-found --wait=false 2>/dev/null || true
    wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
  fi

  echo "==> Monarch operator"
  make -C pipeline/monarch deploy IMG="$MONARCH_IMG"
  kubectl set env deployment/monarch-controller-manager -n monarch-system \
    MONARCH_MODE=dev BERU_IMAGE="$BERU_IMG" SHOP_IMAGE="$SHOP_IMG"
  if [[ "${SKIP_LOAD:-0}" -eq 0 ]]; then
    echo "==> Restart Monarch manager (pick up re-loaded ${MONARCH_IMG})"
    kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  fi
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s

  # beru-system is optional for ShadowTests (beru-local), but bats platform health
  # expects the Deployment; deploy YAML defaults to beru:latest — pin to BERU_IMG.
  echo "==> Beru (beru-system image=${BERU_IMG})"
  kubectl apply -f "$REPO/pipeline/beru/deploy/"
  kubectl set image deployment/beru -n beru-system beru="$BERU_IMG"
  kubectl rollout status deployment/beru -n beru-system --timeout=180s

  # shellcheck source=testing/bats/helpers/pixie-bridge.sh
  source "$REPO/testing/bats/helpers/pixie-bridge.sh"
  echo "==> pixie-gate (monarch-system image=${PIXIE_GATE_IMG})"
  deploy_pixie_gate

  echo "==> Production app (echo on :80, memory limits)"
  kubectl apply -f "$REPO/testing/bats/manifests/e2e-prod-app.yaml"
  kubectl rollout status deployment/my-prod-app -n default --timeout=120s
  kubectl wait -n default --for=condition=Ready pod -l app=my-prod-app --timeout=120s

  echo "==> ShadowTest (servicePort=8888, applicationPort=80, Igris :80/:8888; Shop+Recorder always-on)"
  wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
  kubectl apply -f "$REPO/testing/bats/manifests/e2e-shadowtest.yaml"
  if ! kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" >/dev/null 2>&1; then
    echo "ERROR: ShadowTest $SHADOWTEST_NS/$SHADOWTEST missing after apply" >&2
    exit 1
  fi

  echo "==> Wait for ShadowTest Ready (Monarch reconciles PixieStreamRule + KaiselRule)"
  local i phase kaisel message shadow_ns
  for i in $(seq 1 36); do
    phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    kaisel=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.kaiselPhase}' 2>/dev/null || true)
    message=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.message}' 2>/dev/null || true)
    shadow_ns=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
    if [[ "$phase" == "Ready" && "$kaisel" == "Ready" ]]; then
      break
    fi
    echo "    phase=$phase kaisel=$kaisel msg=${message:-<none>} (${i}/36)"
    sleep 5
  done

  kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o custom-columns=\
PHASE:.status.phase,KAISEL:.status.kaiselPhase,NS:.status.shadowNamespace,CAPTURE:.status.captureTargets

  SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}')
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}')
  kaisel_phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.kaiselPhase}')
  if [[ "$phase" != "Ready" ]]; then
    echo "ERROR: ShadowTest not Ready — check: kubectl describe shadowtest $SHADOWTEST -n $SHADOWTEST_NS" >&2
    if [[ -n "${E2E_IMAGE_REBUILD_HINT:-}" ]]; then
      echo "       ${E2E_IMAGE_REBUILD_HINT}" >&2
    fi
    exit 1
  fi
  if [[ "$kaisel_phase" == "Degraded" ]]; then
    echo "WARN: kaiselPhase=Degraded — check KaiselRule / PixieStreamRule and monarch-controller logs"
  fi

  if [[ -n "${SHADOW_NS:-}" ]]; then
    wait_local_beru_rollout "$SHADOW_NS"
  fi

  echo "==> Wait for Recorder rollout (Monarch resolves recorder:dev via MONARCH_MODE=dev)"
  if [[ -n "${SHADOW_NS:-}" ]]; then
    wait_recorder_rollout "$SHADOWTEST" "$SHADOWTEST_NS" "$SHADOW_NS" "$RECORDER_IMG" 120s
  fi

  echo ""
  echo "E2E stack is up."
  echo "  Shadow namespace: $SHADOW_NS"
  local prod_ip
  prod_ip=$(kubectl get pods -n default -l app=my-prod-app -o jsonpath='{range .items[*]}{.status.podIP}{"\n"}{end}' 2>/dev/null | head -1)
  echo "  Prod IP:          ${prod_ip:-<pending>}"
  echo "  Capture labels:   $(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.captureTargets}' 2>/dev/null || echo '<pending>')"
  echo "  Kaisel ingress:   KaiselRule kaisel-${SHADOWTEST} -> igris in ${SHADOW_NS}"
  echo "  Beru (local):     beru-local.${SHADOW_NS}.svc.cluster.local:50051"
  echo ""
  echo "Run bats tests:     make test-bats-e2e"
}

e2e_reset_deploy_stack
