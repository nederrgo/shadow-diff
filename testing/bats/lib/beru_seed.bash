# Seed RawReports into beru-local for UI / verdict scenario bats.
# Mirrors pipeline/beru/internal/v2/diff unit-test histories via POST /api/v1/debug/seed-reports.
# shellcheck shell=bash

beru_http_post() {
  local shadow_ns="$1" path="$2" body="$3"
  bats_source_e2e_helpers
  local b64 out
  b64=$(printf '%s' "$body" | base64 -w0 2>/dev/null || printf '%s' "$body" | base64)
  out=$(kubectl run "bats-curl-${RANDOM}" --rm -i --restart=Never -n "$shadow_ns" \
    --image=curlimages/curl:8.5.0 --command -- \
    sh -c "echo '${b64}' | base64 -d | curl -sf -X POST -H 'Content-Type: application/json' --data-binary @- 'http://beru-local.${shadow_ns}.svc.cluster.local:8080${path}'" 2>&1) || true
  e2e_strip_kubectl_run_output "$out"
}

# POST {"reports":[...]} body.
beru_seed_reports_json() {
  local body="$1"
  local out
  out=$(beru_http_post "${SHADOW_NS}" "/api/v1/debug/seed-reports" "$body")
  echo "$out"
  echo "$out" | jq -e '.accepted >= 1' >/dev/null 2>&1
}

# Build one seed report object.
# Args: trace_id role protocol direction signature status_code payload_json [captured_at_rfc3339]
beru_seed_report_obj() {
  local trace_id="$1" role="$2" protocol="$3" direction="$4" signature="$5" status_code="$6" payload="$7"
  local captured="${8:-}"
  local name="${SHADOWTEST:-bats-beru-verdict-ui}"
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
  local trace_id="$1" protocol="$2" want_status="$3" want_reg="${4:-}"
  local line status reg
  # Allow engine workers a moment after seed.
  sleep 1
  line=$(beru_verdict_line_api "$trace_id" "$protocol")
  IFS='|' read -r status reg <<<"$line"
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
}

beru_print_ui_hint() {
  local tid="${1:-}"
  echo ""
  echo "==> UI: kubectl -n ${SHADOW_NS} port-forward svc/beru-local 8080:8080"
  echo "    open http://localhost:8080/dashboard/"
  [[ -n "$tid" ]] && echo "    trace_id=${tid}"
}
