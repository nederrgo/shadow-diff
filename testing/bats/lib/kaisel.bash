# Kaisel eBPF DaemonSet helpers for bats tests.
# shellcheck shell=bash

KAISEL_NS="kaisel-system"
KAISEL_DEPLOY_DIR="${REPO}/pipeline/kaisel/deploy"

# Self-contained platform setup for Kaisel E2E: builds + loads Monarch, Kaisel,
# and the HTTP shadow-stack images (igris-http, beru, shop, recorder), installs
# CRDs, and deploys the Monarch operator. Callers still deploy the Kaisel
# DaemonSet via kaisel_daemonset_deploy. Does not install Pixie.
#
# Environment:
#   SKIP_BUILD=1   skip docker build (images must already exist in cluster)
#   SKIP_LOAD=1    skip image load (images must already be in cluster registry)
kaisel_setup_platform() {
  bats_source_e2e_helpers
  bats_source_cluster_helpers
  bats_init_env

  if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
    echo "==> [kaisel] build images (monarch/kaisel/igris/beru/shop/recorder)"
    e2e_prepare_docker_build
    make -C "${REPO}/pipeline/monarch" docker-build IMG="${MONARCH_IMG}"
    make -C "${REPO}/pipeline/kaisel" docker-build KAISEL_IMG="${KAISEL_IMG}"
    make -C "${REPO}/pipeline/beru" docker-build BERU_IMG="${BERU_IMG}"
    make -C "${REPO}/pipeline/shop" docker-build SHOP_IMG="${SHOP_IMG}"
    make -C "${REPO}/pipeline/igrises/igris-http" docker-build IGRIS_IMG="${IGRIS_IMG}"
    make -C "${REPO}/pipeline/recorder" docker-build RECORDER_IMG="${RECORDER_IMG}"
  fi

  if [[ "${SKIP_LOAD:-0}" != "1" ]]; then
    echo "==> [kaisel] ensure images present in cluster docker"
    # Builds above already target minikube docker when MINIKUBE_DRIVER != none.
    # Fail hard if a :dev image is missing — do not docker-pull (no registry) or
    # swallow errors (that produced silent ErrImagePull on beru-local).
    if [[ "${MINIKUBE_DRIVER:-kvm2}" != none ]]; then
      use_minikube_docker_env
    fi
    if ! docker image inspect nginx:alpine >/dev/null 2>&1; then
      docker pull nginx:alpine
    fi
    for img in "${MONARCH_IMG}" "${KAISEL_IMG}" "${BERU_IMG}" "${SHOP_IMG}" \
      "${IGRIS_IMG}" "${RECORDER_IMG}" nginx:alpine; do
      e2e_load_image "${img}"
    done
  fi

  echo "==> [kaisel] install Monarch CRDs"
  make -C "${REPO}/pipeline/monarch" install

  echo "==> [kaisel] deploy Monarch operator (${MONARCH_IMG})"
  make -C "${REPO}/pipeline/monarch" deploy IMG="${MONARCH_IMG}"
  # MONARCH_MODE=dev resolves helper images to locally-built :dev tags.
  kubectl set env deployment/monarch-controller-manager -n monarch-system \
    MONARCH_MODE=dev 2>/dev/null || true
  # Force a rollout so the pod picks up a rebuilt image with the same tag.
  kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  kubectl rollout status deployment/monarch-controller-manager \
    -n monarch-system --timeout=180s
}

# Wait for a KaiselRule to exist with at least one targetIP.
# Usage: wait_kaiselrule_ready <name> <namespace> [timeout_seconds]
wait_kaiselrule_ready() {
  local name="$1" ns="${2:-default}" timeout="${3:-120}"
  local elapsed=0 ip

  echo "==> [kaisel] wait KaiselRule ${ns}/${name} has targetIPs (timeout=${timeout}s)"
  while true; do
    ip=$(kubectl get kaiselrule "$name" -n "$ns" \
      -o jsonpath='{.spec.targetIPs[0]}' 2>/dev/null || true)

    if [[ -n "$ip" ]]; then
      local igris_url
      igris_url=$(kubectl get kaiselrule "$name" -n "$ns" \
        -o jsonpath='{.spec.igrisBaseURL}' 2>/dev/null || true)
      echo "    KaiselRule ${name} targetIPs[0]=${ip} igrisBaseURL=${igris_url:-<empty>}"
      return 0
    fi

    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: timed out waiting for KaiselRule ${ns}/${name} to have targetIPs" >&2
      kubectl get kaiselrule "$name" -n "$ns" -o yaml 2>/dev/null >&2 || true
      kubectl get shadowtest "$name" -n "$ns" -o yaml 2>/dev/null >&2 || true
      return 1
    fi

    echo "    waiting KaiselRule ${ns}/${name} IPs (${elapsed}s/${timeout}s)..."
    sleep 3
    elapsed=$((elapsed + 3))
  done
}

# Wait for a KaiselRule's targetIPs[0] to become a specific value -- used after
# a pod is replaced, to confirm Monarch's Pod watch re-reconciled the rule.
# Usage: wait_kaiselrule_targetip_is <name> <namespace> <want_ip> [timeout_seconds]
wait_kaiselrule_targetip_is() {
  local name="$1" ns="${2:-default}" want_ip="$3" timeout="${4:-120}"
  local elapsed=0 ip

  echo "==> [kaisel] wait KaiselRule ${ns}/${name} targetIPs[0] == ${want_ip} (timeout=${timeout}s)"
  while true; do
    ip=$(kubectl get kaiselrule "$name" -n "$ns" \
      -o jsonpath='{.spec.targetIPs[0]}' 2>/dev/null || true)

    if [[ "$ip" == "$want_ip" ]]; then
      echo "    KaiselRule ${name} targetIPs[0]=${ip}"
      return 0
    fi

    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: timed out waiting for KaiselRule ${ns}/${name} targetIPs[0] to become ${want_ip} (still ${ip:-<empty>})" >&2
      kubectl get kaiselrule "$name" -n "$ns" -o yaml 2>/dev/null >&2 || true
      return 1
    fi

    echo "    waiting KaiselRule targetIPs[0]=${ip:-<empty>}, want ${want_ip} (${elapsed}s/${timeout}s)..."
    sleep 3
    elapsed=$((elapsed + 3))
  done
}

# Wait for a Running pod matching the label selector whose IP differs from
# old_ip -- used after deleting a pod, to find its Deployment-created
# replacement. Prints the new IP on stdout; fails if none appears in time.
# Usage: new_ip=$(wait_new_pod_ip <label_selector> <namespace> <old_ip> [timeout_seconds])
wait_new_pod_ip() {
  local selector="$1" ns="${2:-default}" old_ip="$3" timeout="${4:-90}"
  local elapsed=0 ip

  echo "==> [kaisel] wait for a Running pod (selector=${selector}) with IP != ${old_ip} (timeout=${timeout}s)" >&2
  while true; do
    ip=$(kubectl get pod -l "$selector" -n "$ns" --field-selector=status.phase=Running \
      -o jsonpath='{.items[0].status.podIP}' 2>/dev/null || true)

    if [[ -n "$ip" && "$ip" != "$old_ip" ]]; then
      echo "    new pod IP: ${ip}" >&2
      echo "$ip"
      return 0
    fi

    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: timed out waiting for a replacement pod with a new IP (still ${ip:-<none>})" >&2
      kubectl get pod -l "$selector" -n "$ns" -o wide >&2 || true
      return 1
    fi

    sleep 2
    elapsed=$((elapsed + 2))
  done
}

# Deploy the kaisel DaemonSet into the cluster.
# Patches the container image to ${KAISEL_IMG} if it differs from the manifest
# default (kaisel:latest), so locally-built dev images are used automatically.
kaisel_daemonset_deploy() {
  echo "==> [kaisel] deploying DaemonSet from ${KAISEL_DEPLOY_DIR}"
  kubectl apply -k "$KAISEL_DEPLOY_DIR"
  if [[ -n "${KAISEL_IMG:-}" ]] && [[ "${KAISEL_IMG}" != "kaisel:latest" ]]; then
    kubectl set image daemonset/kaisel kaisel="${KAISEL_IMG}" -n "$KAISEL_NS"
  fi
  # Force pod restart so a rebuilt image with the same tag is picked up.
  kubectl rollout restart daemonset/kaisel -n "$KAISEL_NS" 2>/dev/null || true
}

# Wait for at least one kaisel DaemonSet pod to reach Running.
# Usage: kaisel_daemonset_wait_ready [timeout_seconds]
kaisel_daemonset_wait_ready() {
  local timeout="${1:-120}"
  echo "==> [kaisel] wait DaemonSet pod Running (timeout=${timeout}s)"
  kubectl rollout status daemonset/kaisel -n "$KAISEL_NS" --timeout="${timeout}s"
}

# Tear down the kaisel DaemonSet and its namespace.
# --wait=false: avoid hanging bats EXIT traps when the API is slow to finalize the ns.
kaisel_daemonset_teardown() {
  kubectl delete -k "$KAISEL_DEPLOY_DIR" --ignore-not-found --wait=false --timeout=60s || true
}

# Assert at least one kaisel pod log line matches pattern.
# Collects logs from all nodes; fails if pattern absent.
# Usage: kaisel_assert_captured <grep_pattern>
kaisel_assert_captured() {
  local pattern="$1"
  local logs
  logs=$(kubectl logs -l app=kaisel -n "$KAISEL_NS" --tail=500 2>/dev/null || true)
  if echo "$logs" | grep -q "$pattern"; then
    return 0
  fi
  echo "FAIL: kaisel pod logs missing pattern: ${pattern}" >&2
  echo "--- kaisel logs ---" >&2
  echo "$logs" >&2
  return 1
}

# Assert kaisel pod logs do NOT contain pattern.
kaisel_assert_not_captured() {
  local pattern="$1"
  local logs
  logs=$(kubectl logs -l app=kaisel -n "$KAISEL_NS" --tail=500 2>/dev/null || true)
  if ! echo "$logs" | grep -q "$pattern"; then
    return 0
  fi
  echo "FAIL: kaisel pod logs unexpectedly match: ${pattern}" >&2
  echo "$logs" | grep "$pattern" >&2
  return 1
}

# Curl a prod pod IP with a W3C traceparent (required for Kaisel→igris export).
# Usage: kaisel_publish_prod_traced <path> [trace_id_hex32]
kaisel_publish_prod_traced() {
  local path="$1"
  local trace_id="${2:-$(openssl rand -hex 16)}"
  local span_hex
  span_hex="$(openssl rand -hex 8)"
  local trace_tp="00-${trace_id}-${span_hex}-01"
  local prod_ip
  prod_ip=$(kubectl get pod -l app=kaisel-capture-prod -n default \
    -o jsonpath='{.items[0].status.podIP}')
  if [[ -z "$prod_ip" ]]; then
    echo "kaisel_publish_prod_traced: prod pod has no IP" >&2
    return 1
  fi

  echo "==> [kaisel] prod GET http://${prod_ip}:80${path} trace_id=${trace_id}" >&2
  kubectl run "kaisel-route-${RANDOM}" --restart=Never --rm -i \
    --image=curlimages/curl:latest -n default -- \
    curl -sS --max-time 10 \
    -H "traceparent: ${trace_tp}" \
    "http://${prod_ip}:80${path}" >/dev/null || true
  # stdout is only the trace id (safe under `run` + tail).
  printf '%s\n' "$trace_id"
}

# Wait for igris-http to log multicast complete for a trace id.
# Usage: kaisel_assert_igris_multicast <trace_id_hex32> [timeout_seconds]
kaisel_assert_igris_multicast() {
  local trace_id="$1" timeout="${2:-60}"
  local shadow_ns="${SHADOW_NS:?SHADOW_NS unset}"
  local shadowtest="${SHADOWTEST:?SHADOWTEST unset}"
  local elapsed=0 logs

  echo "==> [kaisel] wait igris multicast complete trace_id=${trace_id}"
  while true; do
    logs=$(kubectl logs -n "$shadow_ns" "deploy/${shadowtest}-igris" --tail=200 2>/dev/null || true)
    if echo "$logs" | grep -F "multicast complete" | grep -Fq "trace_id=${trace_id}"; then
      if echo "$logs" | grep -F "trace_id=${trace_id}" | grep -q 'target=control-a' \
        && echo "$logs" | grep -F "trace_id=${trace_id}" | grep -q 'target=control-b' \
        && echo "$logs" | grep -F "trace_id=${trace_id}" | grep -q 'target=candidate'; then
        echo "    igris multicast complete for ${trace_id} → control-a/b/candidate"
        return 0
      fi
      # slog may put targets on the same line as multicast complete
      if echo "$logs" | grep -F "trace_id=${trace_id}" | grep -q 'control-a' \
        && echo "$logs" | grep -F "trace_id=${trace_id}" | grep -q 'control-b' \
        && echo "$logs" | grep -F "trace_id=${trace_id}" | grep -q 'candidate'; then
        echo "    igris multicast complete for ${trace_id} (roles present)"
        return 0
      fi
    fi

    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: igris never logged multicast complete for trace_id=${trace_id}" >&2
      echo "--- igris logs ---" >&2
      echo "$logs" >&2
      kubectl logs -l app=kaisel -n "$KAISEL_NS" --tail=100 2>/dev/null >&2 || true
      return 1
    fi
    sleep 2
    elapsed=$((elapsed + 2))
  done
}

# Assert each shadow role's app container logged the request path (nginx access log).
# Usage: kaisel_assert_shadow_roles_saw_path <path>
kaisel_assert_shadow_roles_saw_path() {
  local path="$1"
  local role
  for role in control-a control-b candidate; do
    echo "==> [kaisel] wait shadow role=${role} saw path=${path}"
    assert_worker_log_grep "$role" "$path" || {
      local pod
      pod=$(bats_shadow_app_pod "$role" 2>/dev/null || true)
      echo "FAIL: shadow role=${role} never logged path=${path}" >&2
      [[ -n "$pod" ]] && kubectl logs -n "${SHADOW_NS}" "$pod" -c app --tail=50 >&2 || true
      return 1
    }
  done
}
