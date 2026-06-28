#!/usr/bin/env bash
# Reset and deploy the full Monarch E2E stack on Minikube (kvm2 + flannel + Pixie by default).
#
# HTTP ingress capture uses Pixie eBPF (NetObserv removed). Defaults:
#   MINIKUBE_DRIVER=kvm2 (or virtualbox), MINIKUBE_CNI=flannel, Pixie Vizier bootstrap on.
# Opt out of Pixie install: --skip-pixie
# Run OTLP ingress assertion after deploy: --run-otlp-ingress-test
#
# Override driver: MINIKUBE_DRIVER=virtualbox|kvm2|none
#
# Images: VM drivers use eval $(minikube docker-env); none driver uses host docker + minikube image load.
#
# Port model (do not change without updating manifests):
#   prod pod          -> :80   (HTTP_PORT=80; Pixie eBPF -> OTLP -> shadow Siphon :4317)
#   Igris listener    -> :80   (replays captured prod traffic)
#   Envoy ingress     -> :8888 (Igris multicasts to shadow Services here)
#   shadow app (echo) -> :80   (applicationPort; env copied from prod)
#   Envoy egress proxy -> :15001 (HTTP_PROXY when spec.recordAndReplay is set)
#
# Usage:
#   ./testing/scripts/e2e-reset-minikube.sh                    # full reset + deploy + wait Ready
#   ./testing/scripts/e2e-reset-minikube.sh --run-record-replay
#   ./testing/scripts/e2e-reset-minikube.sh --skip-otel-bootstrap
#   ./testing/scripts/e2e-reset-minikube.sh --skip-build       # assume images already in minikube docker
#   ./testing/scripts/e2e-reset-minikube.sh --no-reset         # deploy/upgrade only (no deletes)
#   ./testing/scripts/e2e-reset-minikube.sh --run-otlp-ingress-test
#   ./testing/scripts/e2e-reset-minikube.sh --skip-pixie           # Monarch only, no Vizier
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
cd "$REPO"
# shellcheck source=testing/scripts/lib/siphon-config.sh
source "$REPO/testing/scripts/lib/siphon-config.sh"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"
# shellcheck source=testing/scripts/lib/otel-bootstrap.sh
source "$REPO/testing/scripts/lib/otel-bootstrap.sh"
# shellcheck source=testing/scripts/lib/cluster-minikube.sh
source "$REPO/testing/scripts/lib/cluster-minikube.sh"
# shellcheck source=testing/scripts/lib/e2e-reset-deploy.sh
source "$REPO/testing/scripts/lib/e2e-reset-deploy.sh"
ensure_go_path

MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
BERU_IMG="${BERU_IMG:-beru:dev}"
IGRIS_IMG="${IGRIS_IMG:-igris-http:dev}"
SIPHON_IMG="${SIPHON_IMG:-siphon:dev}"
RECORDER_IMG="${RECORDER_IMG:-recorder:dev}"

SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"

SKIP_BUILD=0
SKIP_LOAD=0
NO_RESET=0
RUN_TEST=0
RUN_EGRESS_TEST=0
RUN_RECORD_REPLAY=0
RUN_DEPENDENCY_TEST=0
RUN_RABBITMQ_TEST=0
RUN_OTEL_RABBITMQ_TEST=0
RUN_OTLP_INGRESS_TEST=0
SETUP_PIXIE=1
SKIP_PIXIE=0
SKIP_OTEL_BOOTSTRAP=0

usage() {
  sed -n '2,22p' "$0"
  echo "Flags: --skip-build --skip-load --no-reset --skip-otel-bootstrap --skip-pixie --setup-pixie --run-test --run-egress-test --run-record-replay --run-dependency-test --run-rabbitmq-test --run-otel-rabbitmq-test --run-otlp-ingress-test -h"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-build) SKIP_BUILD=1 ;;
    --skip-load)  SKIP_LOAD=1 ;;
    --no-reset)   NO_RESET=1 ;;
    --run-test)   RUN_TEST=1 ;;
    --run-egress-test) RUN_EGRESS_TEST=1 ;;
    --run-record-replay) RUN_RECORD_REPLAY=1 ;;
    --run-dependency-test) RUN_DEPENDENCY_TEST=1 ;;
    --run-rabbitmq-test) RUN_RABBITMQ_TEST=1 ;;
    --run-otel-rabbitmq-test) RUN_OTEL_RABBITMQ_TEST=1 ;;
    --run-otlp-ingress-test) RUN_OTLP_INGRESS_TEST=1 ;;
    --setup-pixie) SETUP_PIXIE=1 ;;
    --skip-pixie)  SKIP_PIXIE=1; SETUP_PIXIE=0 ;;
    --skip-otel-bootstrap) SKIP_OTEL_BOOTSTRAP=1 ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "Unknown flag: $1" >&2; usage; exit 1 ;;
  esac
  shift
done

export SKIP_BUILD SKIP_LOAD NO_RESET RUN_TEST RUN_EGRESS_TEST RUN_RECORD_REPLAY
export RUN_DEPENDENCY_TEST RUN_RABBITMQ_TEST RUN_OTEL_RABBITMQ_TEST RUN_OTLP_INGRESS_TEST SETUP_PIXIE SKIP_PIXIE
export SHADOWTEST SHADOWTEST_NS MONARCH_IMG BERU_IMG IGRIS_IMG SIPHON_IMG RECORDER_IMG

# Pixie HTTP ingress: VM driver + flannel CNI (avoid calico/flannel mix on same profile).
# shellcheck source=testing/scripts/lib/pixie-bridge.sh
source "$REPO/testing/scripts/lib/pixie-bridge.sh"
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
if [[ "$SKIP_BUILD" -eq 0 ]] || [[ "$SKIP_OTEL_BOOTSTRAP" -eq 0 ]]; then
  need docker
fi
if [[ "$SKIP_LOAD" -eq 0 ]] || [[ "$SKIP_BUILD" -eq 0 ]] || [[ "$SKIP_OTEL_BOOTSTRAP" -eq 0 ]]; then
  require_minikube
fi

ensure_minikube_ready

if [[ "${SETUP_PIXIE:-1}" -eq 1 && "${SKIP_PIXIE:-0}" -eq 0 ]]; then
  echo "==> Pixie eBPF (Vizier in pl + stream bridge)"
  chmod +x "$REPO/testing/scripts/setup-local-pixie.sh"
  "$REPO/testing/scripts/setup-local-pixie.sh" --skip-minikube-start
fi

echo "==> Monarch E2E reset (minikube profile=${MINIKUBE_PROFILE}, driver=${MINIKUBE_DRIVER}, cni=${MINIKUBE_CNI:-flannel})"
echo "    Images: monarch=$MONARCH_IMG beru=$BERU_IMG (beru-local) igris=$IGRIS_IMG siphon=$SIPHON_IMG recorder=$RECORDER_IMG"
if [[ "$SKIP_BUILD" -eq 1 ]]; then
  echo "WARN: --skip-build reuses existing minikube docker images; code changes are NOT included until you rebuild"
fi

if [[ "$SKIP_BUILD" -eq 0 ]] || [[ "$SKIP_OTEL_BOOTSTRAP" -eq 0 ]]; then
  use_minikube_docker_env
  if [[ "${MINIKUBE_DRIVER:-}" != none ]]; then
    trap 'unload_minikube_docker_env' EXIT
  fi
fi

if [[ "$SKIP_OTEL_BOOTSTRAP" -eq 0 ]]; then
  install_otel_stack
else
  echo "==> Skipping cert-manager + OpenTelemetry Operator (--skip-otel-bootstrap)"
fi

if [[ "$SKIP_BUILD" -eq 0 ]]; then
  echo "==> Build container images (minikube docker daemon)"
  if [[ "${MONARCH_NO_CACHE:-0}" == "1" ]]; then
    bash "$REPO/testing/scripts/lib/docker.sh" build --no-cache -t "$MONARCH_IMG" "$REPO/pipeline/monarch"
  else
    make -C pipeline/monarch docker-build IMG="$MONARCH_IMG"
  fi
  make beru-docker-build BERU_IMG="$BERU_IMG"
  make igris-docker-build IGRIS_IMG="$IGRIS_IMG"
  make siphon-docker-build SIPHON_IMG="$SIPHON_IMG"
  make recorder-docker-build RECORDER_IMG="$RECORDER_IMG"
fi

if [[ "${MINIKUBE_DRIVER:-}" == none ]]; then
  echo "==> Sync local images into containerd (none driver)"
  load_minikube_images "$MONARCH_IMG" "$BERU_IMG" "$IGRIS_IMG" "$SIPHON_IMG" "$RECORDER_IMG"
fi

if [[ "${MINIKUBE_DRIVER:-}" == none ]]; then
  export E2E_IMAGE_REBUILD_HINT="After Monarch code fixes: make -C pipeline/monarch docker-build IMG=${MONARCH_IMG} && load_minikube_images ${MONARCH_IMG} && kubectl rollout restart deployment/monarch-controller-manager -n monarch-system"
else
  export E2E_IMAGE_REBUILD_HINT="After Monarch code fixes: eval \$(minikube docker-env) && make -C pipeline/monarch docker-build IMG=${MONARCH_IMG} && kubectl rollout restart deployment/monarch-controller-manager -n monarch-system"
fi
e2e_reset_deploy_stack
