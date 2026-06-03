# Shared helpers: Monarch -> Siphon config (targets, downstreams, beru_http).
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

siphon_status_json() {
  local host="$1"
  kubectl run "siphon-status-${RANDOM}" --rm -i --restart=Never \
    --image=curlimages/curl:latest -- \
    curl -sf --connect-timeout 3 --max-time 5 "http://${host}:8080/v1/status" 2>/dev/null || true
}

parse_siphon_status_field() {
  local json="$1"
  local field="$2"
  echo "$json" | sed -n "s/.*\"${field}\"[[:space:]]*:[[:space:]]*\\([^,}]*\\).*/\\1/p" | head -1 | tr -d ' "'
}

# Fallback when Monarch is pre-4a.2 and omits downstreams/beru_http_host.
push_siphon_recorder_config() {
  local host="$1"
  local shadowtest="${2:-${SHADOWTEST:-my-app-shadow}}"
  local shadowtest_ns="${3:-${SHADOWTEST_NS:-default}}"
  local prod_ns="${4:-${PROD_NS:-default}}"
  local egress_host="${5:-${EGRESS_HOST:-}}"

  local prod_ip beru_ip shadow_ns igris_host
  prod_ip=$(kubectl get pod -n "$prod_ns" -l app=my-prod-app -o jsonpath='{.items[0].status.podIP}')
  beru_ip=$(kubectl get svc beru -n beru-system -o jsonpath='{.spec.clusterIP}')
  shadow_ns=$(kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" -o jsonpath='{.status.shadowNamespace}')
  igris_host="my-app-shadow-igris.${shadow_ns}.svc.cluster.local"
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
    "beru_http_host": "${beru_ip}:8080",
    "downstreams": [{"host": "${egress_host}"}]
  }]
}
EOF
)
  kubectl run "siphon-cfg-${RANDOM}" --rm -i --restart=Never \
    --image=curlimages/curl:latest -- \
    curl -sf -X POST "http://${host}:8080/v1/config" \
      -H 'Content-Type: application/json' -d "$payload" >/dev/null
}

# Wait until Monarch has pushed ingress targets (and egress recorder fields when downstreams exist).
wait_siphon_configured() {
  local require_recorder="${1:-0}"
  local shadowtest="${SHADOWTEST:-my-app-shadow}"
  local shadowtest_ns="${SHADOWTEST_NS:-default}"
  local host status_json targets downstreams beru_ok

  host=$(siphon_api_host)
  if [[ -z "$host" ]]; then
    echo "ERROR: could not determine Siphon hostIP" >&2
    return 1
  fi

  for i in $(seq 1 30); do
    nudge_siphon_config "$shadowtest" "$shadowtest_ns"
    sleep 2
    status_json=$(siphon_status_json "$host")
    targets=$(parse_siphon_status_field "$status_json" "targets_count")
    downstreams=$(parse_siphon_status_field "$status_json" "downstreams_count")
    beru_ok=$(parse_siphon_status_field "$status_json" "beru_http_configured")

    if [[ "$require_recorder" -eq 1 ]]; then
      if [[ -n "$targets" && "$targets" -gt 0 && -n "$downstreams" && "$downstreams" -gt 0 && "$beru_ok" == "true" ]]; then
        echo "    Siphon targets=$targets downstreams=$downstreams beru_http_configured=$beru_ok"
        return 0
      fi
      if [[ -n "$targets" && "$targets" -gt 0 && ( -z "$downstreams" || "$downstreams" -eq 0 ) ]]; then
        echo "    WARN: Monarch pushed targets but downstreams=0 (rebuild monarch with Sprint 4a.2) — applying recorder config"
        push_siphon_recorder_config "$host" "$shadowtest" "$shadowtest_ns"
        sleep 2
        continue
      fi
      echo "    waiting for recorder config (${i}/30) status=${status_json:-<none>}"
    else
      if [[ -n "$targets" && "$targets" -gt 0 ]]; then
        echo "    Siphon targets=$targets downstreams=${downstreams:-0} beru_http_configured=${beru_ok:-false}"
        return 0
      fi
      echo "    waiting for Siphon targets (${i}/30) status=${status_json:-<none>}"
    fi
  done

  if [[ "$require_recorder" -eq 1 ]]; then
    echo "ERROR: Siphon missing recorder config (need targets_count>0, downstreams_count>0, beru_http_configured=true)" >&2
  else
    echo "ERROR: Siphon missing targets (need targets_count>0)" >&2
  fi
  echo "       Check: kubectl logs -n siphon-system -l app.kubernetes.io/name=siphon-agent | grep 'Siphon target'" >&2
  return 1
}
