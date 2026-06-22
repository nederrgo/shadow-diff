#!/usr/bin/env bash
# Reset and deploy the full Monarch E2E stack on Kind with correct ports.
#
# Port model (do not change without updating manifests):
#   prod pod          -> :80   (HTTP_PORT=80, captured via NetObserv eBPF -> Siphon PCAP :9990)
#   Igris listener    -> :80   (replays captured prod traffic)
#   Envoy ingress     -> :8888 (Igris multicasts to shadow Services here)
#   shadow app (echo) -> :80   (applicationPort; env copied from prod)
#   Envoy egress proxy -> :15001 (HTTP_PROXY when spec.recordAndReplay is set)
#
# Usage:
#   ./testing/scripts/e2e-reset-kind.sh                    # full reset + deploy + wait Ready
#   ./testing/scripts/e2e-reset-kind.sh --run-test         # above, then ./testing/scripts/e2e-pipeline-test.sh
#   ./testing/scripts/e2e-reset-kind.sh --run-egress-test  # above, then ./testing/scripts/e2e-egress-test.sh
#   ./testing/scripts/e2e-reset-kind.sh --run-record-replay  # above, then ./testing/scripts/e2e-record-replay.sh
#   ./testing/scripts/e2e-reset-kind.sh --run-dependency-test  # above, then ./testing/scripts/e2e-dependency-test.sh
#   ./testing/scripts/e2e-reset-kind.sh --run-rabbitmq-test   # above, then ./testing/scripts/e2e-rabbitmq-test.sh
#   ./testing/scripts/e2e-reset-kind.sh --run-otel-rabbitmq-test  # above, then ./testing/scripts/e2e-otel-rabbitmq-test.sh
#   ./testing/scripts/e2e-reset-kind.sh --skip-otel-bootstrap  # skip cert-manager + OTel Operator install
#   ./testing/scripts/e2e-reset-kind.sh --skip-build       # assume images already built/loaded
#   ./testing/scripts/e2e-reset-kind.sh --no-reset         # deploy/upgrade only (no deletes)
#   MONARCH_NO_CACHE=1 ./testing/scripts/e2e-reset-kind.sh # force fresh monarch:dev build (avoids stale Docker cache)
#
# Monarch owns the Siphon DaemonSet and POSTs /v1/config (targets, recordAndReplay, recorder_host)
# from ShadowTest spec. Set MONARCH_MODE=dev on the operator so helper images resolve to :dev tags.
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
ensure_go_path

KIND_CLUSTER="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1)}"
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
  sed -n '2,16p' "$0"
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

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "ERROR: missing command: $1" >&2; exit 1; }
}

need kubectl
if [[ "$SKIP_BUILD" -eq 0 ]] || [[ "$SKIP_LOAD" -eq 0 ]]; then
  need docker
fi
if [[ "$SKIP_LOAD" -eq 0 ]]; then
  need kind
  [[ -n "${KIND_CLUSTER}" ]] || { echo "ERROR: no Kind cluster found; set KIND_CLUSTER or run: kind create cluster --name monarch-test" >&2; exit 1; }
fi

ensure_kind_cluster_ready() {
  local node="${KIND_CLUSTER}-control-plane"
  local ctx="kind-${KIND_CLUSTER}"

  if docker inspect "$node" >/dev/null 2>&1; then
    local state
    state=$(docker inspect "$node" --format '{{.State.Status}}' 2>/dev/null || true)
    if [[ "$state" != "running" ]]; then
      echo "ERROR: Kind node container $node is not running (state=${state:-unknown})" >&2
      echo "       This often happens after Docker/WSL restart. Try one of:" >&2
      echo "         docker start $node && kind export kubeconfig --name $KIND_CLUSTER" >&2
      echo "         kind delete cluster --name $KIND_CLUSTER && kind create cluster --name $KIND_CLUSTER" >&2
      exit 1
    fi
  elif ! kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER"; then
    echo "ERROR: Kind cluster '$KIND_CLUSTER' not found. Create it with:" >&2
    echo "         kind create cluster --name $KIND_CLUSTER" >&2
    exit 1
  else
    echo "ERROR: Kind cluster '$KIND_CLUSTER' is listed but node container $node is missing." >&2
    echo "       Recreate with: kind delete cluster --name $KIND_CLUSTER && kind create cluster --name $KIND_CLUSTER" >&2
    exit 1
  fi

  kind export kubeconfig --name "$KIND_CLUSTER" >/dev/null 2>&1 || true
  kubectl config use-context "$ctx" >/dev/null 2>&1 || true

  echo "==> Wait for Kind API server ($ctx)"
  for i in $(seq 1 30); do
    if kubectl cluster-info --context "$ctx" >/dev/null 2>&1; then
      return 0
    fi
    echo "    API not ready yet (${i}/30)"
    sleep 2
  done

  echo "ERROR: kubectl cannot reach Kind API (context=$ctx)." >&2
  echo "       Stale kubeconfig or a corrupted cluster after Docker/WSL restart." >&2
  echo "       Recreate with:" >&2
  echo "         kind delete cluster --name $KIND_CLUSTER" >&2
  echo "         kind create cluster --name $KIND_CLUSTER" >&2
  exit 1
}

echo "==> Monarch E2E reset (cluster=${KIND_CLUSTER:-local})"
echo "    Images: monarch=$MONARCH_IMG beru=$BERU_IMG igris=$IGRIS_IMG siphon=$SIPHON_IMG recorder=$RECORDER_IMG netobserv=$NETOBSERV_IMG"
if [[ "$SKIP_BUILD" -eq 1 ]]; then
  echo "WARN: --skip-build reuses existing local images; Monarch/Beru/Igris/Siphon code changes are NOT included until you rebuild"
fi

if [[ "$SKIP_BUILD" -eq 0 ]]; then
  echo "==> Build container images"
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

if [[ "$SKIP_LOAD" -eq 0 ]]; then
  ensure_kind_cluster_ready
  if [[ "$SKIP_OTEL_BOOTSTRAP" -eq 0 ]]; then
    install_otel_stack
  else
    echo "==> Skipping cert-manager + OpenTelemetry Operator (--skip-otel-bootstrap)"
  fi
  echo "==> Pull NetObserv eBPF agent (Siphon PCAP sidecar)"
  docker pull "$NETOBSERV_IMG" 2>/dev/null || bash "$REPO/testing/scripts/lib/docker.sh" pull "$NETOBSERV_IMG"
  echo "==> Load images into Kind ($KIND_CLUSTER)"
  kind load docker-image "$MONARCH_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$BERU_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$IGRIS_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$SIPHON_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$RECORDER_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$NETOBSERV_IMG" --name "$KIND_CLUSTER"
fi

export SKIP_BUILD SKIP_LOAD NO_RESET RUN_TEST RUN_EGRESS_TEST RUN_RECORD_REPLAY
export RUN_DEPENDENCY_TEST RUN_RABBITMQ_TEST RUN_OTEL_RABBITMQ_TEST
export SHADOWTEST SHADOWTEST_NS MONARCH_IMG BERU_IMG IGRIS_IMG SIPHON_IMG RECORDER_IMG NETOBSERV_IMG
export E2E_IMAGE_REBUILD_HINT="After Monarch code fixes, rebuild: make -C pipeline/monarch docker-build IMG=${MONARCH_IMG} && kind load docker-image ${MONARCH_IMG} --name ${KIND_CLUSTER}"
# shellcheck source=testing/scripts/lib/e2e-reset-deploy.sh
source "$REPO/testing/scripts/lib/e2e-reset-deploy.sh"
e2e_reset_deploy_stack
