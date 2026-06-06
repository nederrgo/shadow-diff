#!/usr/bin/env bash
# Reset and deploy the full Monarch E2E stack on Kind with correct ports.
#
# Port model (do not change without updating manifests):
#   prod pod          -> :80   (HTTP_PORT=80, captured by Siphon BPF)
#   Igris listener    -> :80   (replays captured prod traffic)
#   Envoy ingress     -> :8888 (Igris multicasts to shadow Services here)
#   shadow app (echo) -> :80   (applicationPort; env copied from prod)
#   Envoy egress proxy -> :15001 (HTTP_PROXY when spec.downstreams is set)
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
#
# Monarch owns the Siphon DaemonSet and POSTs /v1/config (targets, downstreams, recorder_host)
# from ShadowTest spec. Set SIPHON_IMG / IGRIS_IMG / RECORDER_IMG so images match builds (default :dev).
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
IGRIS_IMG="${IGRIS_IMG:-igris:dev}"
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

patch_shadowtest_images() {
  if ! kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" >/dev/null 2>&1; then
    echo "ERROR: ShadowTest $SHADOWTEST_NS/$SHADOWTEST not found — cannot patch images" >&2
    return 1
  fi
  echo "==> Patch ShadowTest images (Monarch reconciles Siphon DaemonSet from spec.siphon.image)"
  echo "    siphon=$SIPHON_IMG igris=$IGRIS_IMG recorder=$RECORDER_IMG"
  kubectl patch shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --type=merge -p "$(cat <<EOF
{
  "spec": {
    "siphon": {"enabled": true, "image": "${SIPHON_IMG}", "sampleRate": 100},
    "igris": {"image": "${IGRIS_IMG}", "replicas": 1},
    "recorder": {"image": "${RECORDER_IMG}"}
  }
}
EOF
)"
}

echo "==> Monarch E2E reset (cluster=${KIND_CLUSTER:-local})"
echo "    Images: monarch=$MONARCH_IMG beru=$BERU_IMG igris=$IGRIS_IMG siphon=$SIPHON_IMG recorder=$RECORDER_IMG"
if [[ "$SKIP_BUILD" -eq 1 ]]; then
  echo "WARN: --skip-build reuses existing local images; Monarch/Beru/Igris/Siphon code changes are NOT included until you rebuild"
fi

if [[ "$SKIP_BUILD" -eq 0 ]]; then
  echo "==> Build container images"
  make -C pipeline/monarch docker-build IMG="$MONARCH_IMG"
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
  echo "==> Load images into Kind ($KIND_CLUSTER)"
  kind load docker-image "$MONARCH_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$BERU_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$IGRIS_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$SIPHON_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$RECORDER_IMG" --name "$KIND_CLUSTER"
fi

echo "==> Monarch CRDs"
make -C pipeline/monarch install

if [[ "$NO_RESET" -eq 0 ]]; then
  echo "==> Delete prior E2E resources (if any)"
  kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=false
  kubectl delete deployment,service my-prod-app -n default --ignore-not-found --wait=false
  wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
fi

echo "==> Monarch operator"
make -C pipeline/monarch deploy IMG="$MONARCH_IMG"
kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s

echo "==> Beru"
kubectl apply -f pipeline/beru/deploy/
kubectl set image deployment/beru beru="$BERU_IMG" -n beru-system
kubectl rollout status deployment/beru -n beru-system --timeout=120s

echo "==> Siphon RBAC (DaemonSet image + config managed by Monarch from ShadowTest spec)"
kubectl apply -f pipeline/siphon/deploy/rbac.yaml

echo "==> Production app (echo on :80, memory limits)"
kubectl apply -f testing/scripts/manifests/e2e-prod-app.yaml
kubectl rollout status deployment/my-prod-app -n default --timeout=120s
kubectl wait -n default --for=condition=Ready pod -l app=my-prod-app --timeout=120s

echo "==> ShadowTest (servicePort=8888, applicationPort=80, Igris :80/:8888, downstreams for egress recorder)"
# A Terminating CR from a prior run causes apply/patch races ("currently being deleted").
wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
kubectl apply -f testing/scripts/manifests/e2e-shadowtest.yaml
if ! kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" >/dev/null 2>&1; then
  echo "ERROR: ShadowTest $SHADOWTEST_NS/$SHADOWTEST missing after apply" >&2
  exit 1
fi
patch_shadowtest_images
nudge_siphon_config "$SHADOWTEST" "$SHADOWTEST_NS"

echo "==> Wait for ShadowTest Ready (Monarch deploys Siphon DaemonSet + POSTs /v1/config)"
for i in $(seq 1 36); do
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  siphon=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.siphonPhase}' 2>/dev/null || true)
  message=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.message}' 2>/dev/null || true)
  shadow_ns=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  if [[ "$phase" == "Ready" && "$siphon" == "Ready" ]]; then
    break
  fi
  if [[ "$phase" == "Ready" && "$i" -ge 6 ]]; then
    nudge_siphon_config "$SHADOWTEST" "$SHADOWTEST_NS"
  fi
  echo "    phase=$phase siphon=$siphon msg=${message:-<none>} (${i}/36)"
  sleep 5
done

kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o custom-columns=\
PHASE:.status.phase,SIPHON:.status.siphonPhase,NS:.status.shadowNamespace,CAPTURE:.status.captureTargets

SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}')
phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}')
siphon_phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.siphonPhase}')
if [[ "$phase" != "Ready" ]]; then
  echo "ERROR: ShadowTest not Ready — check: kubectl describe shadowtest $SHADOWTEST -n $SHADOWTEST_NS" >&2
  echo "       If Envoy sidecars CrashLoopBackOff: kubectl logs -n \$SHADOW_NS deploy/${SHADOWTEST}-control-a -c envoy-sidecar --tail=20" >&2
  echo "       After Monarch code fixes, rebuild: make -C pipeline/monarch docker-build IMG=$MONARCH_IMG && kind load docker-image $MONARCH_IMG --name $KIND_CLUSTER" >&2
  exit 1
fi
if [[ "$siphon_phase" == "Degraded" ]]; then
  echo "WARN: siphonPhase=Degraded — Monarch could not POST /v1/config; check monarch logs and Siphon hostIP reachability"
fi

echo "==> Wait for Recorder rollout (spec.recorder.image=$RECORDER_IMG; avoids stale RS hang)"
if [[ -n "${SHADOW_NS:-}" ]]; then
  wait_recorder_rollout "$SHADOWTEST" "$SHADOWTEST_NS" "$SHADOW_NS" "$RECORDER_IMG" 120s
fi

echo "==> Nudge Monarch to re-push Siphon config (recorder_host after Recorder is up)"
nudge_siphon_config "$SHADOWTEST" "$SHADOWTEST_NS"
sleep 3

echo "==> Verify Monarch pushed Siphon config (targets + downstreams + recorder_host)"
kubectl rollout status daemonset/siphon-agent -n siphon-system --timeout=120s 2>/dev/null || true
wait_siphon_configured 1

echo "==> Verify shadow Envoy -> app port (applicationPort=80)"
for role in control-a control-b candidate; do
  deploy="${SHADOWTEST}-${role}"
  app_port=$(kubectl get deploy "$deploy" -n "$SHADOW_NS" -o jsonpath='{.spec.template.spec.containers[?(@.name=="app")].ports[0].containerPort}')
  echo "    $deploy app containerPort=$app_port"
done

echo ""
echo "E2E stack is up."
echo "  Shadow namespace: $SHADOW_NS"
prod_ip=$(kubectl get pods -n default -l app=my-prod-app -o jsonpath='{range .items[*]}{.status.podIP}{"\n"}{end}' 2>/dev/null | head -1)
siphon_host=$(kubectl get pods -n siphon-system -l app.kubernetes.io/name=siphon-agent -o jsonpath='{range .items[*]}{.status.hostIP}{"\n"}{end}' 2>/dev/null | head -1)
echo "  Prod IP:          ${prod_ip:-<pending>}"
echo "  Siphon API:       http://${siphon_host:-<pending>}:8080"
echo "  Siphon image:     $(kubectl get ds siphon-agent -n siphon-system -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo '<pending>')"
echo "  (host curl to Kind node IP often hangs from WSL; use in-cluster curl instead — see e2e-pipeline-test.sh)"
echo ""
echo "Run ingress test:  ./testing/scripts/e2e-pipeline-test.sh"
echo "Run egress test:   ./testing/scripts/e2e-egress-test.sh"
echo "Run record-replay: ./testing/scripts/e2e-record-replay.sh"
echo "Run dependency E2E: ./testing/scripts/e2e-dependency-test.sh"
echo "Run RabbitMQ E2E:  ./testing/scripts/e2e-rabbitmq-test.sh"
echo "Run OTel RabbitMQ E2E: ./testing/scripts/e2e-otel-rabbitmq-test.sh"
echo "Run k6 stress:     testing/example-apps/k6/run-stress-test.sh  (or see testing/example-apps/k6/README.md)"

if [[ "$RUN_TEST" -eq 1 ]]; then
  echo ""
  ./testing/scripts/e2e-pipeline-test.sh
fi

if [[ "$RUN_EGRESS_TEST" -eq 1 ]]; then
  echo ""
  ./testing/scripts/e2e-egress-test.sh
fi

if [[ "$RUN_RECORD_REPLAY" -eq 1 ]]; then
  echo ""
  chmod +x testing/scripts/e2e-record-replay.sh
  ./testing/scripts/e2e-record-replay.sh
fi

if [[ "$RUN_DEPENDENCY_TEST" -eq 1 ]]; then
  echo ""
  chmod +x testing/scripts/e2e-dependency-test.sh
  ./testing/scripts/e2e-dependency-test.sh
fi

if [[ "$RUN_RABBITMQ_TEST" -eq 1 ]]; then
  echo ""
  chmod +x testing/scripts/e2e-rabbitmq-test.sh
  ./testing/scripts/e2e-rabbitmq-test.sh
fi

if [[ "$RUN_OTEL_RABBITMQ_TEST" -eq 1 ]]; then
  echo ""
  chmod +x testing/scripts/e2e-otel-rabbitmq-test.sh
  ./testing/scripts/e2e-otel-rabbitmq-test.sh
fi
