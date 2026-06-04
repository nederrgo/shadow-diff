# Shared helpers: Monarch -> Siphon config (targets, downstreams, recorder_host).
# Source from E2E scripts; do not execute directly.

siphon_api_host() {
  kubectl get pods -n siphon-system -l app.kubernetes.io/name=siphon-agent \
    -o jsonpath='{.items[0].status.hostIP}'
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
  prod_ip=$(kubectl get pod -n "$prod_ns" -l app=my-prod-app -o jsonpath='{.items[0].status.podIP}')
  shadow_ns=$(kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" -o jsonpath='{.status.shadowNamespace}')
  if [[ -z "$prod_ip" || -z "$shadow_ns" ]]; then
    echo "    ERROR: fallback needs prod pod IP and status.shadowNamespace" >&2
    return 1
  fi
  igris_host="${shadowtest}-igris.${shadow_ns}.svc.cluster.local"
  recorder_host="${shadowtest}-recorder.${shadow_ns}.svc.cluster.local:8080"
  if [[ -z "$egress_host" ]]; then
    egress_host=$(kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" \
      -o jsonpath='{.spec.downstreams[0].host}' 2>/dev/null || true)
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
    "downstreams": [{"host": "${egress_host}"}]
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

# Wait until Monarch has pushed ingress targets (and egress recorder fields when downstreams exist).
wait_siphon_configured() {
  local require_recorder="${1:-0}"
  local shadowtest="${SHADOWTEST:-my-app-shadow}"
  local shadowtest_ns="${SHADOWTEST_NS:-default}"
  local host status_json targets downstreams recorder_ok
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
    downstreams=$(parse_siphon_status_field "$status_json" "downstreams_count")
    recorder_ok=$(parse_siphon_status_field "$status_json" "recorder_host_configured")

    if [[ "$require_recorder" -eq 1 ]]; then
      if [[ -n "$targets" && "$targets" -gt 0 && -n "$downstreams" && "$downstreams" -gt 0 && "$recorder_ok" == "true" ]]; then
        echo "    Siphon targets=$targets downstreams=$downstreams recorder_host_configured=$recorder_ok"
        return 0
      fi
      if [[ -n "$targets" && "$targets" -gt 0 && ( -z "$downstreams" || "$downstreams" -eq 0 ) && "$fallback_pushed" -eq 0 ]]; then
        echo "    WARN: Monarch pushed targets but downstreams=0 — applying recorder config fallback"
        if push_siphon_recorder_config "$host" "$shadowtest" "$shadowtest_ns"; then
          fallback_pushed=1
        fi
        sleep 2
        continue
      fi
      if [[ -n "$targets" && "$targets" -gt 0 && -n "$downstreams" && "$downstreams" -gt 0 && "$recorder_ok" != "true" && "$i" -ge 3 && "$fallback_pushed" -eq 0 ]]; then
        echo "    WARN: downstreams present but recorder_host missing — applying fallback (rebuild monarch:dev for Monarch push)"
        if push_siphon_recorder_config "$host" "$shadowtest" "$shadowtest_ns"; then
          fallback_pushed=1
        fi
        sleep 2
        continue
      fi
      echo "    waiting for recorder config (${i}/30) status=${status_json:-<none>}"
    else
      if [[ -n "$targets" && "$targets" -gt 0 ]]; then
        echo "    Siphon targets=$targets downstreams=${downstreams:-0} recorder_host_configured=${recorder_ok:-false}"
        return 0
      fi
      echo "    waiting for Siphon targets (${i}/30) status=${status_json:-<none>}"
    fi
  done

  if [[ "$require_recorder" -eq 1 ]]; then
    echo "ERROR: Siphon missing recorder config (need targets_count>0, downstreams_count>0, recorder_host_configured=true)" >&2
  else
    echo "ERROR: Siphon missing targets (need targets_count>0)" >&2
  fi
  echo "       Check: kubectl logs -n siphon-system -l app.kubernetes.io/name=siphon-agent | grep 'Siphon target'" >&2
  return 1
}
