# Shared helpers: Monarch -> Siphon config (targets, recordAndReplay, recorder_host).
# Source from E2E scripts; do not execute directly.

siphon_api_host() {
  kubectl get pods -n siphon-system -l app.kubernetes.io/name=siphon-agent \
    -o jsonpath='{range .items[*]}{.status.hostIP}{"\n"}{end}' 2>/dev/null | head -1
}

siphon_agent_pod() {
  kubectl get pods -n siphon-system -l app.kubernetes.io/name=siphon-agent \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true
}

# NetObserv sidecar streams PCAP to Siphon on 127.0.0.1:9990 after /v1/config.
wait_siphon_pcap_stack() {
  local shadowtest="${SHADOWTEST:-my-app-shadow}"
  local shadowtest_ns="${SHADOWTEST_NS:-default}"
  local pod host status_json pcap_addr frames

  for i in $(seq 1 30); do
    pod=$(siphon_agent_pod)
    if [[ -z "$pod" ]]; then
      echo "    waiting for siphon-agent pod (${i}/30)"
      sleep 2
      continue
    fi

    if kubectl get pod "$pod" -n siphon-system -o jsonpath='{.status.containerStatuses[*].ready}' 2>/dev/null \
      | grep -q false; then
      echo "    waiting for siphon-agent containers Ready (${i}/30)"
      if [[ $(( i % 5 )) -eq 0 ]]; then
        kubectl get pod "$pod" -n siphon-system -o jsonpath='{range .status.containerStatuses[*]}{.name}={.state}{" "}{end}' 2>/dev/null || true
        echo ""
      fi
      sleep 2
      continue
    fi

    host=$(siphon_api_host)
    if [[ -z "$host" ]]; then
      sleep 2
      continue
    fi

    if [[ $(( i % 5 )) -eq 0 ]]; then
      nudge_siphon_config "$shadowtest" "$shadowtest_ns"
    fi

    status_json=$(siphon_status_json "$host")
    pcap_addr=$(parse_siphon_status_field "$status_json" "pcap_listen_addr")
    frames=$(parse_siphon_status_field "$status_json" "frames_read")

    if kubectl logs -n siphon-system "$pod" -c agent --tail=120 2>/dev/null \
      | grep -Fq "gRPC collector ready"; then
      echo "    Siphon gRPC collector ready on ${pcap_addr:-127.0.0.1:9990} (frames_read=${frames:-0})"
      return 0
    fi

    echo "    waiting for gRPC collector (${i}/30) pcap_listen_addr=${pcap_addr:-<unset>}"
    sleep 2
  done

  echo "ERROR: Siphon gRPC collector not ready (NetObserv sidecar + agent on :9990)" >&2
  pod=$(siphon_agent_pod)
  if [[ -n "$pod" ]]; then
    echo "       agent:    kubectl logs -n siphon-system $pod -c agent --tail=40" >&2
    echo "       netobserv: kubectl logs -n siphon-system $pod -c netobserv-ebpf-agent --tail=40" >&2
  fi
  return 1
}

# ponytail: netobserv and agent start together; PCA dials :9990 before collector listens unless we restart netobserv
ensure_netobserv_exports_to_collector() {
  local pod i
  pod=$(siphon_agent_pod)
  [[ -n "$pod" ]] || {
    echo "ERROR: no siphon-agent pod for NetObserv readiness check" >&2
    return 1
  }
  for i in $(seq 1 60); do
    if kubectl exec -n siphon-system "$pod" -c agent -- test -f /var/run/siphon/grpc-ready 2>/dev/null; then
      break
    fi
    sleep 1
  done
  if ! kubectl exec -n siphon-system "$pod" -c agent -- test -f /var/run/siphon/grpc-ready 2>/dev/null; then
    echo "ERROR: Siphon gRPC ready file missing (/var/run/siphon/grpc-ready)" >&2
    return 1
  fi
  if kubectl logs -n siphon-system "$pod" -c netobserv-ebpf-agent --since=30s 2>/dev/null \
    | grep -q 'connection refused'; then
    echo "==> Recreate siphon-agent pod (NetObserv connected before gRPC collector)"
    kubectl delete pod "$pod" -n siphon-system --wait=true
    wait_siphon_daemonset_rollout 180s
    wait_siphon_pcap_stack || return 1
    pod=$(siphon_agent_pod)
  fi
  for i in $(seq 1 60); do
    if kubectl logs -n siphon-system "$pod" -c netobserv-ebpf-agent --tail=15 2>/dev/null \
      | grep -Fq "Packets agent successfully started"; then
      sleep 2
      if ! kubectl logs -n siphon-system "$pod" -c netobserv-ebpf-agent --since=20s 2>/dev/null \
        | grep -q 'connection refused'; then
        echo "    NetObserv PCA export connected to Siphon"
        return 0
      fi
    fi
    sleep 1
  done
  echo "WARN: NetObserv restart did not confirm PCA export — record phase may miss traffic" >&2
  return 0
}

_netobserv_failure_hint() {
  local pod="$1"
  local logs node_kern
  logs=$(kubectl logs -n siphon-system "$pod" -c netobserv-ebpf-agent --tail=8 2>&1 || true)
  if echo "$logs" | grep -qE 'Verifier error|invalid zero-sized read|load program: permission denied'; then
    node_kern=$(kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.kernelVersion}' 2>/dev/null || echo unknown)
    echo "       NetObserv PCA BPF failed kernel verifier on node kernel ${node_kern}" >&2
    echo "       (permission denied in logs = verifier rejection, not missing caps/privileged)" >&2
    if [[ "$node_kern" == 6.6.* ]]; then
      echo "       minikube VM ISO ships kernel 6.6.x; NetObserv :main PCA programs may not load." >&2
      echo "       WSL host kernel is newer — try MINIKUBE_DRIVER=none (host kubelet) or Kind for NetObserv E2E." >&2
    fi
    return 0
  fi
  echo "       Kind/minikube: NetObserv sidecar needs privileged + runAsUser:0 — rebuild monarch:dev and re-run reset" >&2
}

wait_siphon_daemonset_rollout() {
  local timeout="${1:-180s}"
  if kubectl rollout status daemonset/siphon-agent -n siphon-system --timeout="$timeout" 2>/dev/null; then
    return 0
  fi
  echo "ERROR: siphon-agent DaemonSet not Ready (both agent + netobserv-ebpf-agent must be up)" >&2
  local pod ready_line
  pod=$(siphon_agent_pod)
  if [[ -n "$pod" ]]; then
    ready_line=$(kubectl get pod "$pod" -n siphon-system -o jsonpath='{range .status.containerStatuses[*]}{.name}:ready={.ready} restarts={.restartCount}{" "}{end}' 2>/dev/null || true)
    echo "       pod=$pod $ready_line" >&2
    echo "       netobserv logs (last 15 lines):" >&2
    kubectl logs -n siphon-system "$pod" -c netobserv-ebpf-agent --tail=15 2>&1 | sed 's/^/         /' >&2 || true
    _netobserv_failure_hint "$pod"
  fi
  return 1
}

# ponytail: restart after prod veths exist — netobserv bulk attach at DS startup races CNI Link not found
refresh_netobserv_hooks() {
  local prod_ns="${1:-${PROD_NS:-default}}"
  local prod_label="${2:-app=my-prod-app}"
  echo "==> Waiting for production target pods to stabilize..."
  kubectl wait -n "$prod_ns" --for=condition=Ready pod -l "$prod_label" --timeout=60s
  echo "==> Target pods ready. Cycling Siphon DaemonSet for clean eBPF hook attachments..."
  kubectl rollout restart ds/siphon-agent -n siphon-system
  wait_siphon_daemonset_rollout 180s
  nudge_siphon_config "${SHADOWTEST:-my-app-shadow}" "${SHADOWTEST_NS:-default}"
  sleep 2
}

nudge_siphon_config() {
  local shadowtest="${1:-${SHADOWTEST:-my-app-shadow}}"
  local ns="${2:-${SHADOWTEST_NS:-default}}"
  kubectl annotate shadowtest "$shadowtest" -n "$ns" \
    shadow-diff.io/reconcile="$(date +%s)" --overwrite >/dev/null
}

# Curl Siphon on the node hostIP (hostNetwork agent). Response is read from pod logs (not kubectl run stdout).
siphon_curl_node() {
  local host="$1"
  local method="$2"
  local path="$3"
  local body="${4:-}"
  local pod ns phase
  pod="siphon-curl-${RANDOM}"
  ns="${SIPHON_CURL_NS:-default}"

  if [[ "$method" == "POST" && -n "$body" ]]; then
    local payload_b64
    payload_b64=$(printf '%s' "$body" | base64 | tr -d '\n')
    kubectl run "$pod" -n "$ns" --restart=Never --image=curlimages/curl:latest \
      --overrides='{"spec":{"hostNetwork":true}}' \
      --env="HOST=${host}" --env="PAYLOAD_B64=${payload_b64}" \
      --command -- sh -c \
      'echo "$PAYLOAD_B64" | base64 -d | curl -sf -X POST "http://${HOST}:8080'"${path}"'" -H "Content-Type: application/json" -d @-' \
      1>&2
  else
    kubectl run "$pod" -n "$ns" --restart=Never --image=curlimages/curl:latest \
      --overrides='{"spec":{"hostNetwork":true}}' \
      --env="HOST=${host}" \
      --command -- sh -c \
      'curl -sf "http://${HOST}:8080'"${path}"'"' \
      1>&2
  fi

  if kubectl wait --for=jsonpath='{.status.phase}'=Succeeded -n "$ns" "pod/${pod}" --timeout=45s 1>&2 2>/dev/null; then
    # kubectl run does not stream container stdout; curl output is in pod logs.
    if [[ "$method" == "GET" ]]; then
      kubectl logs "$pod" -n "$ns" 2>/dev/null
    fi
    kubectl delete pod "$pod" -n "$ns" --ignore-not-found --wait=false 1>&2 2>/dev/null
    return 0
  fi

  phase=$(kubectl get pod "$pod" -n "$ns" -o jsonpath='{.status.phase}' 2>/dev/null || echo Unknown)
  echo "    siphon curl pod ${pod} phase=${phase} (host=${host} path=${path})" >&2
  kubectl logs "$pod" -n "$ns" 2>/dev/null | tail -5 >&2 || true
  kubectl delete pod "$pod" -n "$ns" --ignore-not-found --wait=false 1>&2 2>/dev/null
  return 1
}

siphon_status_json() {
  local host="$1"
  siphon_curl_node "$host" GET /v1/status || true
}

parse_siphon_status_field() {
  local json="$1"
  local field="$2"
  echo "$json" | sed -n "s/.*\"${field}\"[[:space:]]*:[[:space:]]*\\([^,}]*\\).*/\\1/p" | head -1 | tr -d ' "'
}

# Fallback when Monarch omits recorder_host on egress targets.
push_siphon_recorder_config() {
  local host="$1"
  local shadowtest="${2:-${SHADOWTEST:-my-app-shadow}}"
  local shadowtest_ns="${3:-${SHADOWTEST_NS:-default}}"
  local prod_ns="${4:-${PROD_NS:-default}}"
  local egress_host="${5:-${EGRESS_HOST:-}}"

  local prod_ip shadow_ns igris_host recorder_host
  prod_ip=$(kubectl get pod -n "$prod_ns" -l app=my-prod-app -o jsonpath='{range .items[*]}{.status.podIP}{"\n"}{end}' 2>/dev/null | head -1)
  shadow_ns=$(kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" -o jsonpath='{.status.shadowNamespace}')
  if [[ -z "$prod_ip" || -z "$shadow_ns" ]]; then
    echo "    ERROR: fallback needs prod pod IP and status.shadowNamespace" >&2
    return 1
  fi
  igris_host="${shadowtest}-igris.${shadow_ns}.svc.cluster.local"
  recorder_host="${shadowtest}-recorder.${shadow_ns}.svc.cluster.local:8080"
  if [[ -z "$egress_host" ]]; then
    egress_host=$(kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" \
      -o jsonpath='{.spec.recordAndReplay[0].host}' 2>/dev/null || true)
  fi

  local payload
  payload=$(cat <<EOF
{
  "sample_rate": 100,
  "targets": [{
    "shadowtest": "${shadowtest_ns}/${shadowtest}",
    "target_ips": ["${prod_ip}"],
    "target_ports": [80, 8888],
    "igris_host": "${igris_host}",
    "listeners": [
      {"port": 80, "driver": "http_request"},
      {"port": 8888, "driver": "http_request"}
    ],
    "recorder_host": "${recorder_host}",
    "recordAndReplay": [{"host": "${egress_host}"}]
  }]
}
EOF
)
  echo "    fallback POST recorder_host=${recorder_host}" >&2
  if ! siphon_curl_node "$host" POST /v1/config "$payload"; then
    return 1
  fi
  local status_json ok
  status_json=$(siphon_curl_node "$host" GET /v1/status || true)
  ok=$(parse_siphon_status_field "$status_json" "recorder_host_configured")
  echo "    fallback verify: recorder_host_configured=${ok:-false}" >&2
  [[ "$ok" == "true" ]]
}

# Wait until Monarch has pushed ingress targets (and egress recorder fields when recordAndReplay exist).
wait_siphon_configured() {
  local require_recorder="${1:-0}"
  local shadowtest="${SHADOWTEST:-my-app-shadow}"
  local shadowtest_ns="${SHADOWTEST_NS:-default}"
  local host status_json targets record_and_replay recorder_ok
  local fallback_pushed=0

  host=$(siphon_api_host)
  if [[ -z "$host" ]]; then
    echo "ERROR: could not determine Siphon hostIP" >&2
    return 1
  fi

  for i in $(seq 1 30); do
    # Do not nudge after fallback: an old Monarch controller re-pushes config without recorder_host.
    if [[ "$fallback_pushed" -eq 0 && ( "$i" -eq 1 || $(( i % 5 )) -eq 0 ) ]]; then
      nudge_siphon_config "$shadowtest" "$shadowtest_ns"
    fi
    sleep 2
    status_json=$(siphon_status_json "$host")
    targets=$(parse_siphon_status_field "$status_json" "targets_count")
    record_and_replay=$(parse_siphon_status_field "$status_json" "record_and_replay_count")
    recorder_ok=$(parse_siphon_status_field "$status_json" "recorder_host_configured")

    if [[ "$require_recorder" -eq 1 ]]; then
      if [[ -n "$targets" && "$targets" -gt 0 && -n "$record_and_replay" && "$record_and_replay" -gt 0 && "$recorder_ok" == "true" ]]; then
        echo "    Siphon targets=$targets record_and_replay=$record_and_replay recorder_host_configured=$recorder_ok"
        return 0
      fi
      if [[ -n "$targets" && "$targets" -gt 0 && ( -z "$record_and_replay" || "$record_and_replay" -eq 0 ) && "$fallback_pushed" -eq 0 ]]; then
        echo "    WARN: Monarch pushed targets but record_and_replay=0 — applying recorder config fallback"
        if push_siphon_recorder_config "$host" "$shadowtest" "$shadowtest_ns"; then
          fallback_pushed=1
        fi
        sleep 2
        continue
      fi
      if [[ -n "$targets" && "$targets" -gt 0 && -n "$record_and_replay" && "$record_and_replay" -gt 0 && "$recorder_ok" != "true" && "$i" -ge 3 && "$fallback_pushed" -eq 0 ]]; then
        echo "    WARN: recordAndReplay present but recorder_host missing — applying fallback (rebuild monarch:dev for Monarch push)"
        if push_siphon_recorder_config "$host" "$shadowtest" "$shadowtest_ns"; then
          fallback_pushed=1
        fi
        sleep 2
        continue
      fi
      echo "    waiting for recorder config (${i}/30) status=${status_json:-<none>}"
    else
      if [[ -n "$targets" && "$targets" -gt 0 ]]; then
        echo "    Siphon targets=$targets record_and_replay=${record_and_replay:-0} recorder_host_configured=${recorder_ok:-false}"
        return 0
      fi
      echo "    waiting for Siphon targets (${i}/30) status=${status_json:-<none>}"
    fi
  done

  if [[ "$require_recorder" -eq 1 ]]; then
    echo "ERROR: Siphon missing recorder config (need targets_count>0, record_and_replay_count>0, recorder_host_configured=true)" >&2
  else
    echo "ERROR: Siphon missing targets (need targets_count>0)" >&2
  fi
  echo "       Check: kubectl logs -n siphon-system -l app.kubernetes.io/name=siphon-agent -c agent | grep -E 'Siphon target|gRPC collector'" >&2
  return 1
}
