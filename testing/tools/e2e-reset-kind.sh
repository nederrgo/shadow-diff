#!/usr/bin/env bash
# Reset and deploy the full Monarch E2E stack on Kind (host docker + kind load).
#
# HTTP ingress and egress capture both use Kaisel eBPF (DaemonSet in kaisel-system).
# E2E assertions: make test-bats-e2e / make test-bats-kaisel.
#
# Cluster: KIND_CLUSTER (default shadow-diff) from testing/bats/kind/config.yaml.
# Port mappings (recreate cluster if missing):
#   host 18080 → node 30080 (prod NodePort)
#   host 15432 → node 30432 (Postgres NodePort)
#   kind delete cluster --name shadow-diff
#
# Port model (do not change without updating manifests):
#   prod pod          -> :80   (HTTP_PORT=80; Kaisel eBPF capture -> igris-http)
#   Igris listener    -> :80   (replays captured prod traffic)
#   Envoy ingress     -> :8888 (Igris multicasts to shadow Services here)
#   shadow app (echo) -> :80   (applicationPort; env copied from prod)
#   Envoy egress proxy -> :15001 (Shop always-on)
#
# Usage (from repo root):
#   ./testing/tools/e2e-reset-kind.sh                 # full reset + deploy + wait Ready
#   ./testing/tools/e2e-reset-kind.sh --skip-build    # reuse images already on host docker
#   ./testing/tools/e2e-reset-kind.sh --no-reset      # deploy/upgrade only (no deletes)
#   ./testing/tools/e2e-reset-kind.sh --skip-load --skip-build --no-reset  # fastest
#
# A PostgreSQL fixture is always deployed to monarch-system. The manager always
# gets BERU_DB_SECRET=monarch-system/beru-postgres so beru-local uses Postgres +
# a disk WAL EmptyDir. Reach Postgres from the host (Go conformance) via Kind
# extraPortMappings:
#   export BERU_TEST_POSTGRES_DSN="postgres://beru:beru@localhost:15432/beru?sslmode=disable"
#   go -C pipeline/beru test ./internal/storage/... -run 'Conformance|Projection' -v
#
set -euo pipefail

# testing/tools/<script> → repo root is ../..
REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
cd "$REPO"
# shellcheck source=testing/bats/helpers/e2e-helpers.sh
source "$REPO/testing/bats/helpers/e2e-helpers.sh"
# shellcheck source=testing/bats/helpers/cluster-kind.sh
source "$REPO/testing/bats/helpers/cluster-kind.sh"
# shellcheck source=testing/tools/lib/e2e-reset-deploy.sh
source "$REPO/testing/tools/lib/e2e-reset-deploy.sh"
ensure_go_path

MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
BERU_IMG="${BERU_IMG:-beru:dev}"
SHOP_IMG="${SHOP_IMG:-shop:dev}"
IGRIS_IMG="${IGRIS_IMG:-igris-http:dev}"
IGRIS_RABBITMQ_IMG="${IGRIS_RABBITMQ_IMG:-igris-rabbitmq:dev}"
EGRESS_RELAY_RABBITMQ_IMG="${EGRESS_RELAY_RABBITMQ_IMG:-egress-relay-rabbitmq:dev}"
SHADOW_SOLDIER_IMG="${SHADOW_SOLDIER_IMG:-shadow-soldier:dev}"
KAISEL_IMG="${KAISEL_IMG:-kaisel:dev}"
TUSK_IMG="${TUSK_IMG:-tusk:dev}"
THE_SYSTEM_IMG="${THE_SYSTEM_IMG:-the-system:dev}"

SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"

SKIP_BUILD=0
SKIP_LOAD=0
NO_RESET=0

usage() {
  # Comment header only (through the blank line before set -euo).
  sed -n '2,35p' "$0"
  echo "Flags: --skip-build --skip-load --no-reset -h"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-build) SKIP_BUILD=1 ;;
    --skip-load)  SKIP_LOAD=1 ;;
    --no-reset)   NO_RESET=1 ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "Unknown flag: $1" >&2; usage; exit 1 ;;
  esac
  shift
done

export SKIP_BUILD SKIP_LOAD NO_RESET

export SHADOWTEST SHADOWTEST_NS MONARCH_IMG BERU_IMG SHOP_IMG IGRIS_IMG \
  IGRIS_RABBITMQ_IMG EGRESS_RELAY_RABBITMQ_IMG SHADOW_SOLDIER_IMG \
  KAISEL_IMG TUSK_IMG THE_SYSTEM_IMG

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "ERROR: missing command: $1" >&2; exit 1; }
}

need kubectl
if [[ "$SKIP_BUILD" -eq 0 ]] || [[ "$SKIP_LOAD" -eq 0 ]]; then
  need docker
  require_kind
fi

ensure_kind_ready

echo "==> Monarch E2E reset (kind cluster=${KIND_CLUSTER}, context=${KIND_CONTEXT})"
echo "    Images: monarch=$MONARCH_IMG beru=$BERU_IMG shop=$SHOP_IMG igris=$IGRIS_IMG kaisel=$KAISEL_IMG"
if [[ "$SKIP_BUILD" -eq 1 ]]; then
  echo "WARN: --skip-build reuses existing host docker images; code changes are NOT included until you rebuild"
fi

if [[ "$SKIP_BUILD" -eq 0 ]]; then
  echo "==> Build container images (host docker daemon)"
  if [[ "${MONARCH_NO_CACHE:-0}" == "1" ]]; then
    docker build --no-cache -f "$REPO/pipeline/monarch/Dockerfile" -t "$MONARCH_IMG" "$REPO/pipeline"
  else
    make -C pipeline/monarch docker-build IMG="$MONARCH_IMG"
  fi
  make beru-docker-build BERU_IMG="$BERU_IMG"
  make shop-docker-build SHOP_IMG="$SHOP_IMG"
  make igris-docker-build IGRIS_IMG="$IGRIS_IMG"
  make kaisel-docker-build KAISEL_IMG="$KAISEL_IMG"
  make tusk-docker-build TUSK_IMG="$TUSK_IMG"
  make the-system-docker-build THE_SYSTEM_IMG="$THE_SYSTEM_IMG"
fi

if [[ "$SKIP_LOAD" -eq 0 ]]; then
  echo "==> kind load docker-image into cluster ${KIND_CLUSTER}"
  for img in "$MONARCH_IMG" "$BERU_IMG" "$SHOP_IMG" "$IGRIS_IMG" "$KAISEL_IMG" "$TUSK_IMG" "$THE_SYSTEM_IMG"; do
    load_kind_image "$img"
  done
fi

export E2E_HOST_PG_ADDR=localhost:15432
export E2E_IMAGE_REBUILD_HINT="After Monarch code fixes: make -C pipeline/monarch docker-build IMG=${MONARCH_IMG} && kind load docker-image ${MONARCH_IMG} --name ${KIND_CLUSTER} && kubectl rollout restart deployment/monarch-controller-manager -n monarch-system"

e2e_reset_deploy_stack
