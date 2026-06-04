# Shared helpers for Monarch E2E bash scripts.
# shellcheck shell=bash

log_success() {
  echo "[SUCCESS] $*"
}

log_fail() {
  echo "[FAIL] $*" >&2
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    log_fail "missing command: $1"
    exit 1
  }
}

# wait_shadowtest_gone blocks until the ShadowTest CR is fully removed.
# No-op when the CR does not exist, or exists without a deletionTimestamp (live object).
wait_shadowtest_gone() {
  local name="$1" ns="$2" max_wait="${3:-180}"
  if ! kubectl get shadowtest "$name" -n "$ns" >/dev/null 2>&1; then
    return 0
  fi
  local deleting
  deleting=$(kubectl get shadowtest "$name" -n "$ns" -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null || true)
  if [[ -z "$deleting" ]]; then
    return 0
  fi
  local i=0
  while kubectl get shadowtest "$name" -n "$ns" >/dev/null 2>&1; do
    i=$((i + 2))
    if [[ "$i" -gt "$max_wait" ]]; then
      log_fail "timed out waiting for ShadowTest $ns/$name to finish deleting"
      kubectl get shadowtest "$name" -n "$ns" -o yaml 2>/dev/null | tail -25 >&2 || true
      echo "       To force-remove finalizer (last resort):" >&2
      echo "         kubectl patch shadowtest $name -n $ns --type=merge -p '{\"metadata\":{\"finalizers\":[]}}'" >&2
      return 1
    fi
    echo "    waiting for ShadowTest $ns/$name to finish deleting (${i}s/${max_wait}s)..."
    sleep 2
  done
  return 0
}

# scale_down_recorder_replicasets_not_matching scales ReplicaSets whose recorder
# container image differs from want_image to zero replicas. Use after kubectl set
# image or a partial rollout left an old RS at desired=1 with ErrImagePull on Kind.
scale_down_recorder_replicasets_not_matching() {
  local shadow_ns="$1" deploy_name="$2" want_image="$3"
  local rs name img
  while IFS= read -r rs; do
    [[ -z "$rs" ]] && continue
    name="${rs#replicaset.apps/}"
    img=$(kubectl get "$rs" -n "$shadow_ns" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true)
    if [[ -n "$img" && "$img" != "$want_image" ]]; then
      echo "    scale down stale recorder RS $name (image=$img, want=$want_image)"
      kubectl scale "$rs" --replicas=0 -n "$shadow_ns" >/dev/null
    fi
  done < <(kubectl get rs -n "$shadow_ns" -o name 2>/dev/null | grep "^replicaset.apps/${deploy_name}-" || true)
}

# wait_recorder_rollout waits for the Recorder Deployment after ensuring the
# ShadowTest spec and Deployment template use want_image (avoids rollout hang on
# recorder:latest pods that cannot pull on Kind).
wait_recorder_rollout() {
  local shadowtest="$1" shadowtest_ns="$2" shadow_ns="$3" want_image="$4" timeout="${5:-120s}"
  local deploy="${shadowtest}-recorder"
  if ! kubectl get deploy "$deploy" -n "$shadow_ns" >/dev/null 2>&1; then
    return 0
  fi
  kubectl patch shadowtest "$shadowtest" -n "$shadowtest_ns" --type=merge -p "$(cat <<EOF
{"spec":{"recorder":{"image":"${want_image}"}}}
EOF
)" >/dev/null 2>&1 || true
  sleep 2
  scale_down_recorder_replicasets_not_matching "$shadow_ns" "$deploy" "$want_image"
  kubectl rollout status "deployment/${deploy}" -n "$shadow_ns" --timeout="$timeout" 2>/dev/null || true
}

# shadow_app_pod_for_role returns a shadow worker pod (app container), not a
# spec.dependencies pod (rabbitmq-control-a also carries shadow-diff.io/role).
shadow_app_pod_for_role() {
  local shadow_ns="$1" shadowtest="$2" role="$3"
  kubectl get pods -n "$shadow_ns" \
    -l "shadow-diff.io/shadowtest-name=${shadowtest},shadow-diff.io/role=${role},shadow-diff.io/resource-kind!=dependency" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
}
