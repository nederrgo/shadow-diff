# Beru settlement polling and SQLite/API assertions.
# shellcheck shell=bash

beru_http_get() {
  local shadow_ns="$1" path="$2"
  bats_source_e2e_helpers
  local out
  out=$(kubectl run "bats-curl-${RANDOM}" --rm -i --restart=Never -n "$shadow_ns" \
    --image=curlimages/curl:8.5.0 -- \
    curl -sf "http://beru-local.${shadow_ns}.svc.cluster.local:8080${path}" 2>&1) || true
  e2e_strip_kubectl_run_output "$out"
}

# GET /api/v1/traces/{id}?protocol=…[&direction=…]
beru_http_get_trace() {
  local shadow_ns="$1" trace_id="$2" protocol="$3" direction="${4:-}"
  local path="/api/v1/traces/${trace_id}?protocol=${protocol}"
  [[ -n "$direction" ]] && path="${path}&direction=${direction}"
  beru_http_get "$shadow_ns" "$path"
}

beru_sqlite_query() {
  local pod="$1" ns="$2" db_path="$3" sql="$4"
  command -v sqlite3 >/dev/null 2>&1 || return 2
  local tmp
  tmp="$(mktemp "${TMPDIR:-/tmp}/beru-XXXXXX.db")"
  kubectl cp "${ns}/${pod}:${db_path}" "$tmp" 2>/dev/null || { rm -f "$tmp"; return 2; }
  sqlite3 -batch -noheader "$tmp" "$sql"
  local rc=$?
  rm -f "$tmp"
  return $rc
}

beru_reports_role_count() {
  local trace_id="$1" protocol="$2" via="${3:-api}" direction="${4:-}"
  if [[ "$via" == "sqlite" ]]; then
    local pod; pod="$(beru_local_pod)"
    local sql="SELECT COUNT(DISTINCT shadow_role) FROM raw_reports WHERE trace_id='${trace_id}' AND protocol='${protocol}'"
    [[ -n "$direction" ]] && sql="${sql} AND direction='${direction}'"
    sql="${sql};"
    beru_sqlite_query "$pod" "${SHADOW_NS}" "/data/beru.db" "$sql"
    return $?
  fi
  local json roles
  json=$(beru_http_get_trace "${SHADOW_NS}" "$trace_id" "$protocol" "$direction") || return 1
  roles=$(echo "$json" | jq -r '[.reports[].shadow_role] | unique | length' 2>/dev/null || echo "0")
  echo "$roles"
}

beru_reports_complete() {
  local trace_id="$1" protocol="$2" via="${3:-api}" direction="${4:-}"
  local count
  count=$(beru_reports_role_count "$trace_id" "$protocol" "$via" "$direction" 2>/dev/null || echo "0")
  [[ "${count:-0}" -ge 3 ]]
}

# Wait until Shop→Beru HTTP egress reports exist for all three roles and
# mirrorLegacyLogs emits a clean egress match for protocol http.
# Usage: beru_wait_http_egress_match <trace_id> [--timeout=120] [--signature=http:GET:/path]
beru_wait_http_egress_match() {
  local trace_id="$1"
  shift
  local timeout=120 want_sig=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --timeout=*) timeout="${1#*=}"; shift ;;
      --signature=*) want_sig="${1#*=}"; shift ;;
      *) echo "beru_wait_http_egress_match: unknown arg $1" >&2; return 2 ;;
    esac
  done

  local i=0 json
  while [[ "$i" -lt "$timeout" ]]; do
    if beru_reports_complete "$trace_id" http api egress; then
      break
    fi
    sleep 1
    i=$((i + 1))
  done
  if ! beru_reports_complete "$trace_id" http api egress; then
    echo "timeout waiting for 3 HTTP egress roles trace=${trace_id}" >&2
    beru_dump_trace_diagnostics "$trace_id" http >&2
    return 1
  fi

  if [[ -n "$want_sig" ]]; then
    json=$(beru_http_get_trace "${SHADOW_NS}" "$trace_id" http egress) || return 1
    if ! echo "$json" | jq -e --arg s "$want_sig" '[.reports[].signature] | unique | . == [$s]' >/dev/null 2>&1; then
      echo "HTTP egress signature mismatch want=${want_sig} got=$(echo "$json" | jq -c '[.reports[].signature] | unique' 2>/dev/null)" >&2
      return 1
    fi
  fi

  local remain=$((timeout - i))
  [[ "$remain" -lt 30 ]] && remain=30
  beru_wait_log --grep="$(beru_log_no_egress_regression "$trace_id" http)" --timeout="$remain"
}

_beru_verdict_snapshot_api() {
  local trace_id="$1" protocol="$2"
  local json
  json=$(beru_http_get "${SHADOW_NS}" "/api/v1/traces/${trace_id}?protocol=${protocol}") || return 1
  local status regression updated
  status=$(echo "$json" | jq -r '.verdict.Status // .verdict.status // empty' 2>/dev/null)
  regression=$(echo "$json" | jq -r '.verdict.HasCountRegression // .verdict.has_count_regression // false' 2>/dev/null)
  updated=$(echo "$json" | jq -r '.verdict.UpdatedAt // .verdict.updated_at // empty' 2>/dev/null)
  [[ "$regression" == "true" || "$regression" == "1" ]] && regression=1 || regression=0
  echo "${status}|${regression}|${updated}"
}

_beru_verdict_snapshot_sqlite() {
  local trace_id="$1"
  local pod; pod="$(beru_local_pod)"
  beru_sqlite_query "$pod" "${SHADOW_NS}" "/data/beru.db" \
    "SELECT status, has_count_regression, updated_at FROM verdicts WHERE trace_id='${trace_id}';"
}

# Print status|has_count_regression (0/1) from Beru HTTP API.
beru_verdict_line_api() {
  local trace_id="$1" protocol="$2"
  local snap status reg
  snap=$(_beru_verdict_snapshot_api "$trace_id" "$protocol") || return 1
  IFS='|' read -r status reg _ <<<"$snap"
  [[ -z "$status" && "$reg" == "1" ]] && status=MISMATCH
  echo "${status}|${reg}"
}

beru_wait_verdict_settled() {
  local trace_id="$1" protocol="$2"
  shift 2
  local expect_status="" expect_regression="" timeout=120 quiescence="${BERU_QUIESCENCE_SEC:-5}" via="api"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --expect-status=*) expect_status="${1#*=}"; shift ;;
      --expect-count-regression=*) expect_regression="${1#*=}"; shift ;;
      --timeout=*) timeout="${1#*=}"; shift ;;
      --quiescence=*) quiescence="${1#*=}"; shift ;;
      --via=*) via="${1#*=}"; shift ;;
      *) shift ;;
    esac
  done

  local last_updated="" stable_since="" snap status reg updated i=0
  while [[ "$i" -lt "$timeout" ]]; do
    if ! beru_reports_complete "$trace_id" "$protocol" "$via"; then
      sleep 1
      i=$((i + 1))
      continue
    fi

    if [[ "$via" == "sqlite" ]]; then
      snap=$(_beru_verdict_snapshot_sqlite "$trace_id" 2>/dev/null || true)
      IFS='|' read -r status reg updated <<<"${snap//|/|}"
    else
      snap=$(_beru_verdict_snapshot_api "$trace_id" "$protocol" 2>/dev/null || true)
      IFS='|' read -r status reg updated <<<"$snap"
    fi

    [[ -z "$status" ]] && { sleep 1; i=$((i + 1)); continue; }

    if [[ "$updated" != "$last_updated" ]]; then
      last_updated="$updated"
      stable_since=$i
    elif [[ $((i - stable_since)) -ge "$quiescence" ]]; then
      local ok=1
      [[ -n "$expect_status" && "$status" != "$expect_status" ]] && ok=0
      [[ -n "$expect_regression" && "$reg" != "$expect_regression" ]] && ok=0
      if [[ "$ok" == "1" ]]; then
        echo "settled status=${status} regression=${reg} trace=${trace_id} protocol=${protocol}"
        return 0
      fi
      echo "settled but predicate mismatch: status=${status} regression=${reg} want=${expect_status}/${expect_regression}" >&2
      return 3
    fi

    sleep 1
    i=$((i + 1))
  done

  echo "timeout waiting for verdict settlement trace=${trace_id} protocol=${protocol}" >&2
  beru_dump_trace_diagnostics "$trace_id" "$protocol" >&2
  return 1
}

beru_dump_trace_diagnostics() {
  local trace_id="$1" protocol="$2"
  local pod; pod="$(beru_local_pod)"
  echo "--- beru diagnostics trace=${trace_id} protocol=${protocol} ---"
  beru_sqlite_query "$pod" "${SHADOW_NS}" "/data/beru.db" \
    "SELECT shadow_role, COUNT(*) FROM raw_reports WHERE trace_id='${trace_id}' AND protocol='${protocol}' GROUP BY shadow_role;" 2>/dev/null || true
  kubectl logs -n "${SHADOW_NS}" "$pod" --tail=40 2>/dev/null | grep -E "${trace_id}|${protocol}" || true
}

# Build mirrorLegacyLogs strings (pipeline/beru/internal/v2/engine/logs.go).
beru_log_egress_count_unit() {
  local protocol="$1" count="$2"
  case "$protocol" in
    rabbitmq|kafka)
      [[ "$count" == 1 ]] && echo message || echo messages ;;
    mongodb|postgresql|redis)
      [[ "$count" == 1 ]] && echo query || echo queries ;;
    *)
      [[ "$count" == 1 ]] && echo operation || echo operations ;;
  esac
}

beru_log_egress_count_regression() {
  local trace_id="$1" protocol="$2" expected="${3:-1}" got="${4:-2}"
  local unit
  unit="$(beru_log_egress_count_unit "$protocol" "$expected")"
  echo "Egress count regression for Trace ${trace_id} (${protocol}): expected ${expected} ${unit} but got ${got}"
}

beru_log_no_egress_regression() {
  local trace_id="$1" protocol="$2"
  echo "No egress regression for Trace ${trace_id} (${protocol})"
}

beru_log_no_regression() {
  local trace_id="$1"
  echo "No regression for Trace ${trace_id}"
}

# Poll beru-local logs until an exact line appears (mirrorLegacyLogs output).
# Usage:
#   beru_wait_log --grep="$(beru_log_egress_count_regression "$BATS_TRACE_ID" rabbitmq)"
#   beru_wait_log 'Egress regression for Trace abc (http): Field ...'
beru_wait_log() {
  local grep_pattern="" timeout=120 tail_lines=400
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --grep=*) grep_pattern="${1#*=}"; shift ;;
      --timeout=*) timeout="${1#*=}"; shift ;;
      --tail=*) tail_lines="${1#*=}"; shift ;;
      -*) echo "beru_wait_log: unknown option $1" >&2; return 2 ;;
      *)
        [[ -z "$grep_pattern" ]] && grep_pattern="$1" || grep_pattern="${grep_pattern}${1}"
        shift
        ;;
    esac
  done
  [[ -n "$grep_pattern" ]] || {
    echo "beru_wait_log: pass --grep=PATTERN or a positional pattern" >&2
    return 2
  }

  local pod
  pod="$(beru_local_pod)"
  [[ -n "$pod" ]] || {
    echo "beru_wait_log: beru-local pod not found in ${SHADOW_NS:-<unset>}" >&2
    return 2
  }

  local i=0
  while [[ "$i" -lt "$timeout" ]]; do
    if kubectl logs -n "${SHADOW_NS}" "$pod" --tail="$tail_lines" 2>/dev/null | grep -Fq "$grep_pattern"; then
      echo "beru log matched"
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done

  echo "timeout waiting for beru log (${timeout}s): ${grep_pattern}" >&2
  kubectl logs -n "${SHADOW_NS}" "$pod" --tail=30 2>/dev/null >&2 || true
  return 1
}

# Wait until shadow-soldier has reported MongoDB egress for all three roles.
# Usage: wait_mongodb_egress_reports <trace_id> [timeout_seconds]
wait_mongodb_egress_reports() {
  local trace_id="$1" timeout="${2:-120}" i=0
  while [[ "$i" -lt "$timeout" ]]; do
    if beru_reports_complete "$trace_id" mongodb api egress; then
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done
  echo "timed out after ${timeout}s waiting for 3 mongodb egress roles on trace ${trace_id}" >&2
  beru_reports_role_count "$trace_id" mongodb api egress >&2 || true
  return 1
}
