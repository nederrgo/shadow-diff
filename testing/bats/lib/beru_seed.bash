# Seed RawReports into Beru for verdict scenario bats.
# Mirrors pipeline/beru/internal/v2/diff unit-test histories via POST /api/v1/debug/seed-reports.
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
  local trace_id="$1" protocol="$2" want_status="$3" want_reg="${4:-}"
  local timeout="${5:-20}"
  local line status reg i=0
  while [[ $i -lt "$timeout" ]]; do
    line=$(beru_verdict_line_api "$trace_id" "$protocol" 2>/dev/null || true)
    IFS='|' read -r status reg <<<"$line"
    if [[ -n "$status" ]]; then
      [[ "$status" == "$want_status" ]] || {
        echo "verdict status=${status} want=${want_status} line=${line}" >&2
        return 1
      }
      if [[ -n "$want_reg" ]]; then
        [[ "$reg" == "$want_reg" ]] || {
          echo "has_count_regression=${reg} want=${want_reg}" >&2
          return 1
        }
      fi
      echo "ok status=${status} regression=${reg}"
      return 0
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
    echo "==> UI: kubectl -n ${BERU_NS:-monarch-system} port-forward svc/${BERU_SVC} 8080:8080"
  else
    echo "==> UI: kubectl -n ${SHADOW_NS} port-forward svc/beru-local 8080:8080"
  fi
  echo "    open http://localhost:8080/dashboard/"
  [[ -n "$tid" ]] && echo "    trace_id=${tid}"
}
