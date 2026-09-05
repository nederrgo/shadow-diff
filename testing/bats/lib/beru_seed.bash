# Seed RawReports into Beru for verdict scenario bats.
# Mirrors pipeline/beru/internal/diff unit-test histories via POST /api/v1/debug/seed-reports.
# shellcheck shell=bash

beru_http_post() {
  local shadow_ns="$1" path="$2" body="$3"
  bats_source_e2e_helpers
  local b64 out base curl_ns
  base="$(beru_http_base "$shadow_ns")"
  curl_ns="$(beru_curl_ns "$shadow_ns")"
  b64=$(printf '%s' "$body" | base64 -w0 2>/dev/null || printf '%s' "$body" | base64)
  out=$(kubectl run "bats-curl-${RANDOM}" --rm -i --restart=Never -n "$curl_ns" \
    --image=curlimages/curl:8.5.0 --command -- \
    sh -c "echo '${b64}' | base64 -d | curl -sf -X POST -H 'Content-Type: application/json' --data-binary @- '${base}${path}'" 2>&1) || true
  e2e_strip_kubectl_run_output "$out"
}

# POST with empty reports → HTTP 400 when /api/v1/debug/seed-reports is live.
beru_probe_seed_endpoint() {
  local shadow_ns="${1:-${SHADOW_NS:-${BERU_NS:-monarch-system}}}"
  bats_source_e2e_helpers
  local base curl_ns out code
  base="$(beru_http_base "$shadow_ns")"
  curl_ns="$(beru_curl_ns "$shadow_ns")"
  out=$(kubectl run "bats-curl-${RANDOM}" --rm -i --restart=Never -n "$curl_ns" \
    --image=curlimages/curl:8.5.0 --command -- \
    curl -sS -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' \
    --data '{"reports":[]}' "${base}/api/v1/debug/seed-reports" 2>&1) || true
  out=$(e2e_strip_kubectl_run_output "$out")
  code=$(printf '%s\n' "$out" | grep -E '^[0-9]{3}$' | head -1)
  case "$code" in
    400|202) return 0 ;;
    *)
      echo "seed endpoint probe failed (HTTP ${code:-unknown}) at ${base}/api/v1/debug/seed-reports" >&2
      echo "probe output: ${out}" >&2
      return 1
      ;;
  esac
}

# POST {"reports":[...]} body.
beru_seed_reports_json() {
  local body="$1"
  local out ns="${SHADOW_NS:-${BERU_NS:-monarch-system}}"
  out=$(beru_http_post "$ns" "/api/v1/debug/seed-reports" "$body")
  echo "$out"
  echo "$out" | jq -e '.accepted >= 1' >/dev/null 2>&1
}

# Build one seed report object.
# Args: trace_id role protocol direction signature status_code payload_json [captured_at_rfc3339]
beru_seed_report_obj() {
  local trace_id="$1" role="$2" protocol="$3" direction="$4" signature="$5" status_code="$6" payload="$7"
  local captured="${8:-}"
  local name="${SHADOWTEST:-bats-beru-postgres-verdict}"
  if [[ -n "$captured" ]]; then
    jq -nc \
      --arg tid "$trace_id" --arg role "$role" --arg proto "$protocol" \
      --arg dir "$direction" --arg sig "$signature" --arg sc "$status_code" \
      --arg name "$name" --argjson payload "$payload" --arg captured "$captured" \
      '{
        trace_id:$tid, shadow_role:$role, shadow_test_name:$name,
        protocol:$proto, direction:$dir, signature:$sig, status_code:$sc,
        payload:$payload, captured_at:$captured
      }'
  else
    jq -nc \
      --arg tid "$trace_id" --arg role "$role" --arg proto "$protocol" \
      --arg dir "$direction" --arg sig "$signature" --arg sc "$status_code" \
      --arg name "$name" --argjson payload "$payload" \
      '{
        trace_id:$tid, shadow_role:$role, shadow_test_name:$name,
        protocol:$proto, direction:$dir, signature:$sig, status_code:$sc,
        payload:$payload
      }'
  fi
}

# Seed N report objects passed as separate JSON strings.
beru_seed_reports() {
  local arr="["
  local i=0
  local obj
  for obj in "$@"; do
    [[ $i -gt 0 ]] && arr+=","
    arr+="$obj"
    i=$((i + 1))
  done
  arr+="]"
  local body
  body=$(jq -nc --argjson reports "$arr" '{reports:$reports}')
  beru_seed_reports_json "$body"
}

beru_assert_verdict_status() {
  # Seed-only assert: one GET per attempt, no quiescence (history is static).
  # Args: trace_id protocol want_status [want_regression] [timeout_sec]
  # WAITING_FOR_ROLES is non-terminal while the WAL flusher catches up after a
  # multi-report seed — keep polling until want_status or timeout.
  local trace_id="$1" protocol="$2" want_status="$3" want_reg="${4:-}"
  local timeout="${5:-20}"
  local line status reg i=0
  while [[ $i -lt "$timeout" ]]; do
    line=$(beru_verdict_line_api "$trace_id" "$protocol" 2>/dev/null || true)
    IFS='|' read -r status reg <<<"$line"
    if [[ -n "$status" ]]; then
      if [[ "$status" == "$want_status" ]]; then
        if [[ -n "$want_reg" ]]; then
          [[ "$reg" == "$want_reg" ]] || {
            echo "has_count_regression=${reg} want=${want_reg}" >&2
            return 1
          }
        fi
        echo "ok status=${status} regression=${reg}"
        return 0
      fi
      if [[ "$status" == "WAITING_FOR_ROLES" && "$want_status" != "WAITING_FOR_ROLES" ]]; then
        sleep 1
        i=$((i + 1))
        continue
      fi
      echo "verdict status=${status} want=${want_status} line=${line}" >&2
      return 1
    fi
    sleep 1
    i=$((i + 1))
  done
  echo "timeout waiting for verdict status=${want_status} trace=${trace_id} (last=${line})" >&2
  return 1
}

beru_print_ui_hint() {
  local tid="${1:-}"
  echo ""
  if [[ -n "${BERU_SVC:-}" ]]; then
    echo "==> Trace API: kubectl -n ${BERU_NS:-monarch-system} port-forward svc/${BERU_SVC} 8080:8080"
  else
    echo "==> Trace API: kubectl -n ${SHADOW_NS} port-forward svc/beru-local 8080:8080"
  fi
  echo "    GET /api/v1/traces/<id>?protocol=…  (or The System /diffs)"
  [[ -n "$tid" ]] && echo "    trace_id=${tid}"
}
