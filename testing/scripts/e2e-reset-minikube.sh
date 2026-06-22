#!/usr/bin/env bash
# Reset and deploy the full Monarch E2E stack on Minikube (VM or host kubelet).
#
# Driver auto-select (no docker VM driver — same daemon as Kind): virtualbox if available,
# else none on WSL (host kernel — NetObserv BPF), else kvm2. Override:
#   MINIKUBE_DRIVER=virtualbox|kvm2|none
#
# WSL + NetObserv: use none (sudo). kvm2 VM kernel 6.6.x often rejects NetObserv PCA BPF.
# If you previously exported MINIKUBE_DRIVER=kvm2, unset it or set MINIKUBE_DRIVER=none.
#
# Images: VM drivers use eval $(minikube docker-env); none driver uses host docker + minikube image load.
#
# Port model (do not change without updating manifests):
#   prod pod          -> :80   (HTTP_PORT=80, captured via NetObserv eBPF -> Siphon PCAP :9990)
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
#   MONARCH_NO_CACHE=1 ./testing/scripts/e2e-reset-minikube.sh
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
NETOBSERV_IMG="${NETOBSERV_IMG:-quay.io/netobserv/netobserv-ebpf-agent:main}"

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
SKIP_OTEL_BOOTSTRAP=0

usage() {
  sed -n '2,22p' "$0"
  echo "Flags: --skip-build --skip-load --no-reset --skip-otel-bootstrap --run-test --run-egress-test --run-record-replay --run-dependency-test --run-rabbitmq-test --run-otel-rabbitmq-test -h"
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
    --skip-otel-bootstrap) SKIP_OTEL_BOOTSTRAP=1 ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "Unknown flag: $1" >&2; usage; exit 1 ;;
  esac
  shift
done

export SKIP_BUILD SKIP_LOAD NO_RESET RUN_TEST RUN_EGRESS_TEST RUN_RECORD_REPLAY
export RUN_DEPENDENCY_TEST RUN_RABBITMQ_TEST RUN_OTEL_RABBITMQ_TEST
export SHADOWTEST SHADOWTEST_NS MONARCH_IMG BERU_IMG IGRIS_IMG SIPHON_IMG RECORDER_IMG NETOBSERV_IMG

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

echo "==> Monarch E2E reset (minikube profile=${MINIKUBE_PROFILE}, driver=${MINIKUBE_DRIVER})"
echo "    Images: monarch=$MONARCH_IMG beru=$BERU_IMG igris=$IGRIS_IMG siphon=$SIPHON_IMG recorder=$RECORDER_IMG netobserv=$NETOBSERV_IMG"
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
  echo "==> Pull NetObserv eBPF agent (Siphon PCAP sidecar)"
  bash "$REPO/testing/scripts/lib/docker.sh" pull "$NETOBSERV_IMG"
fi

if [[ "${MINIKUBE_DRIVER:-}" == none ]]; then
  echo "==> Sync local images into containerd (none driver)"
  load_minikube_images "$MONARCH_IMG" "$BERU_IMG" "$IGRIS_IMG" "$SIPHON_IMG" "$RECORDER_IMG" "$NETOBSERV_IMG"
fi

if [[ "${MINIKUBE_DRIVER:-}" == none ]]; then
  export E2E_IMAGE_REBUILD_HINT="After Monarch code fixes: make -C pipeline/monarch docker-build IMG=${MONARCH_IMG} && load_minikube_images ${MONARCH_IMG} && kubectl rollout restart deployment/monarch-controller-manager -n monarch-system"
else
  export E2E_IMAGE_REBUILD_HINT="After Monarch code fixes: eval \$(minikube docker-env) && make -C pipeline/monarch docker-build IMG=${MONARCH_IMG} && kubectl rollout restart deployment/monarch-controller-manager -n monarch-system"
fi
e2e_reset_deploy_stack
