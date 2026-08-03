# Beru HTTP helpers, Postgres SQL cleanup, and verdict assertions.
# shellcheck shell=bash

# Base URL for beru HTTP. Prefer standalone BERU_SVC/BERU_NS (postgres_verdict);
# fall back to beru-local in SHADOW_NS for ShadowTest suites.
beru_http_base() {
  local shadow_ns="${1:-${SHADOW_NS:-}}"
  if [[ -n "${BERU_SVC:-}" ]]; then
    echo "http://${BERU_SVC}.${BERU_NS:-monarch-system}.svc.cluster.local:8080"
    return 0
  fi
  echo "http://beru-local.${shadow_ns}.svc.cluster.local:8080"
}

# Namespace used to run ephemeral curl pods (avoid restricted monarch-system PSA).
beru_curl_ns() {
  if [[ -n "${BERU_SVC:-}" ]]; then
    echo "default"
    return 0
  fi
  echo "${1:-${SHADOW_NS:-default}}"
}

beru_http_get() {
  local shadow_ns="$1" path="$2"
  bats_source_e2e_helpers
  local base curl_ns out
  base="$(beru_http_base "$shadow_ns")"
  curl_ns="$(beru_curl_ns "$shadow_ns")"
  out=$(kubectl run "bats-curl-${RANDOM}" --rm -i --restart=Never -n "$curl_ns" \
    --image=curlimages/curl:8.5.0 -- \
    curl -sf "${base}${path}" 2>&1) || true
  e2e_strip_kubectl_run_output "$out"
}

# GET /api/v1/traces/{id}?protocol=…[&direction=…]
beru_http_get_trace() {
  local shadow_ns="$1" trace_id="$2" protocol="$3" direction="${4:-}"
  local path="/api/v1/traces/${trace_id}?protocol=${protocol}"
  [[ -n "$direction" ]] && path="${path}&direction=${direction}"
  beru_http_get "$shadow_ns" "$path"
}

beru_postgres_sql() {
  local sql="$1"
  kubectl exec -n monarch-system deploy/postgres -- \
    env PGPASSWORD=beru psql -U beru -d beru -v ON_ERROR_STOP=1 -Atc "$sql"
}

# Scale the bats Postgres fixture. emptyDir is wiped on scale-to-0 — callers that
# bring it back must restart beru-verdict so migrations re-apply.
beru_postgres_scale() {
  local replicas="$1"
  kubectl scale deployment/postgres -n monarch-system --replicas="$replicas"
}

beru_postgres_wait_ready() {
  local timeout="${1:-120}" i=0
  kubectl rollout status deployment/postgres -n monarch-system --timeout="${timeout}s" || return 1
  while [[ $i -lt "$timeout" ]]; do
    if beru_postgres_sql 'SELECT 1' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done
  echo "timeout waiting for Postgres SELECT 1" >&2
  return 1
}

beru_postgres_wait_down() {
  local timeout="${1:-60}" i=0 ready
  while [[ $i -lt "$timeout" ]]; do
    ready=$(kubectl get deployment/postgres -n monarch-system \
      -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    [[ -z "$ready" || "$ready" == "0" ]] && return 0
    sleep 1
    i=$((i + 1))
  done
  echo "timeout waiting for Postgres scale-to-0" >&2
  return 1
}

# Restart standalone beru so OpenBackend re-runs migrations (after Postgres wipe).
beru_verdict_restart_and_wait() {
  local ns="${BERU_NS:-monarch-system}" i=0 pod logs
  kubectl delete pod -n "$ns" -l "app=${BERU_SVC:-beru-verdict}" --wait=true
  kubectl rollout status deployment/beru-verdict -n "$ns" --timeout=180s || return 1
  while [[ $i -lt 60 ]]; do
    pod=$(beru_verdict_pod)
    logs=$(kubectl logs -n "$ns" "$pod" --tail=40 2>/dev/null || true)
    if echo "$logs" | grep -Fq "PostgreSQL storage ready" \
      && echo "$logs" | grep -Fq "WAL flusher ready"; then
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done
  echo "beru-verdict did not log Postgres+WAL ready after restart" >&2
  return 1
}

# Resolve the Beru pod for logs/exec (standalone beru-verdict or beru-local).
beru_verdict_pod() {
  local ns pod
  if [[ -n "${BERU_SVC:-}" ]]; then
    ns="${BERU_NS:-monarch-system}"
    pod=$(kubectl get pods -n "$ns" -l "app=${BERU_SVC}" \
      -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    echo "$pod"
    return 0
  fi
  beru_local_pod
}

# Poll beru logs for a discarded poison-pill batch.
# Distroless has no tar/cat, so we cannot kubectl cp/exec the DLQ file; unit
# tests cover writeDeadLetter. Integration asserts the discard log line.
# Usage: beru_wait_dead_letter <trace_id> [timeout_sec]
beru_wait_dead_letter() {
  local trace_id="$1" timeout="${2:-100}"
  local pod ns i=0 logs
  ns="${BERU_NS:-${SHADOW_NS:-monarch-system}}"
  pod="$(beru_verdict_pod)"
  [[ -n "$pod" ]] || {
    echo "beru_wait_dead_letter: beru pod not found" >&2
    return 2
  }
  while [[ $i -lt "$timeout" ]]; do
    logs=$(kubectl logs -n "$ns" "$pod" --tail=400 2>/dev/null || true)
    if echo "$logs" | grep -Fq "Discarding failed WAL batch after max retries" \
      && echo "$logs" | grep -Fq "$trace_id"; then
      echo "dead-lettered trace=${trace_id}"
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done
  echo "timeout waiting for dead letter trace=${trace_id}" >&2
  echo "--- recent logs ---" >&2
  kubectl logs -n "$ns" "$pod" --tail=80 2>/dev/null >&2 || true
  echo "--- tip: need BERU_WAL_FLUSH_TIMEOUT=3s in beru-verdict + rebuilt image ---" >&2
  kubectl get deploy beru-verdict -n "$ns" -o jsonpath='{.spec.template.spec.containers[0].env}' 2>/dev/null >&2 || true
  echo >&2
  return 1
}

beru_cleanup_trace_postgres() {
  local tid="${1:-}"
  [[ -n "$tid" ]] || return 0
  # Escape single quotes for SQL literals.
  tid="${tid//\'/\'\'}"
  beru_postgres_sql "
DELETE FROM diff_reports WHERE trace_id = '${tid}';
DELETE FROM traces WHERE trace_id = '${tid}';
DELETE FROM verdicts WHERE trace_id = '${tid}';
DELETE FROM raw_reports WHERE trace_id = '${tid}';
" >/dev/null
}

beru_cleanup_shadow_test_postgres() {
  local name="${1:-}"
  [[ -n "$name" ]] || return 0
  name="${name//\'/\'\'}"
  beru_postgres_sql "
DELETE FROM diff_reports WHERE trace_id IN (
  SELECT DISTINCT trace_id FROM raw_reports WHERE shadow_test_name = '${name}'
  UNION SELECT trace_id FROM verdicts WHERE shadow_test_name = '${name}'
  UNION SELECT trace_id FROM traces WHERE shadow_test_name = '${name}'
);
DELETE FROM traces WHERE shadow_test_name = '${name}';
DELETE FROM verdicts WHERE shadow_test_name = '${name}';
DELETE FROM raw_reports WHERE shadow_test_name = '${name}';
DELETE FROM shadow_tests WHERE name = '${name}';
" >/dev/null
}

beru_reports_role_count() {
  local trace_id="$1" protocol="$2" via="${3:-api}" direction="${4:-}"
  local ns="${SHADOW_NS:-${BERU_NS:-monarch-system}}"
  local json roles
  json=$(beru_http_get_trace "$ns" "$trace_id" "$protocol" "$direction") || return 1
  roles=$(echo "$json" | jq -r '[.reports[].shadow_role] | unique | length' 2>/dev/null || echo "0")
  echo "$roles"
}

beru_reports_complete() {
  local trace_id="$1" protocol="$2" via="${3:-api}" direction="${4:-}"
  local count
  count=$(beru_reports_role_count "$trace_id" "$protocol" "$via" "$direction" 2>/dev/null || echo "0")
  [[ "${count:-0}" -ge 3 ]]
}

# Wait until Shop→Beru HTTP egress reports exist for all three roles and the
# Postgres-backed verdict settles MATCH (optional signature equality check).
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
    json=$(beru_http_get_trace "${SHADOW_NS:-${BERU_NS}}" "$trace_id" http egress) || return 1
    if ! echo "$json" | jq -e --arg s "$want_sig" '[.reports[].signature] | unique | . == [$s]' >/dev/null 2>&1; then
      echo "HTTP egress signature mismatch want=${want_sig} got=$(echo "$json" | jq -c '[.reports[].signature] | unique' 2>/dev/null)" >&2
      return 1
    fi
  fi

  local remain=$((timeout - i))
  [[ "$remain" -lt 30 ]] && remain=30
  beru_wait_verdict_settled "$trace_id" http \
    --expect-status=MATCH --timeout="$remain"
}

_beru_verdict_snapshot_api() {
  local trace_id="$1" protocol="$2"
  local ns="${SHADOW_NS:-${BERU_NS:-monarch-system}}"
  local json
  json=$(beru_http_get "$ns" "/api/v1/traces/${trace_id}?protocol=${protocol}") || return 1
  local status regression updated
  status=$(echo "$json" | jq -r '.verdict.Status // .verdict.status // empty' 2>/dev/null)
  regression=$(echo "$json" | jq -r '.verdict.HasCountRegression // .verdict.has_count_regression // false' 2>/dev/null)
  updated=$(echo "$json" | jq -r '.verdict.UpdatedAt // .verdict.updated_at // empty' 2>/dev/null)
  [[ "$regression" == "true" || "$regression" == "1" ]] && regression=1 || regression=0
  echo "${status}|${regression}|${updated}"
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

    snap=$(_beru_verdict_snapshot_api "$trace_id" "$protocol" 2>/dev/null || true)
    IFS='|' read -r status reg updated <<<"$snap"

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
  local ns="${SHADOW_NS:-${BERU_NS:-monarch-system}}"
  local pod
  echo "--- beru diagnostics trace=${trace_id} protocol=${protocol} ---"
  beru_http_get_trace "$ns" "$trace_id" "$protocol" 2>/dev/null | head -c 2000 || true
  echo
  if [[ -n "${BERU_SVC:-}" ]]; then
    pod=$(kubectl get pods -n "${BERU_NS:-monarch-system}" -l "app=${BERU_SVC}" \
      -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    [[ -n "$pod" ]] && kubectl logs -n "${BERU_NS:-monarch-system}" "$pod" --tail=40 2>/dev/null | grep -E "${trace_id}|${protocol}" || true
  else
    pod="$(beru_local_pod)"
    [[ -n "$pod" ]] && kubectl logs -n "${SHADOW_NS}" "$pod" --tail=40 2>/dev/null | grep -E "${trace_id}|${protocol}" || true
  fi
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

# Poll beru logs until an exact line appears (mirrorLegacyLogs output).
# Usage:
#   beru_wait_log --grep="$(beru_log_egress_count_regression "$BATS_TRACE_ID" rabbitmq)"
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

  local pod ns
  if [[ -n "${BERU_SVC:-}" ]]; then
    ns="${BERU_NS:-monarch-system}"
    pod=$(kubectl get pods -n "$ns" -l "app=${BERU_SVC}" \
      -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  else
    ns="${SHADOW_NS:-}"
    pod="$(beru_local_pod)"
  fi
  [[ -n "$pod" ]] || {
    echo "beru_wait_log: beru pod not found in ${ns:-<unset>}" >&2
    return 2
  }

  local i=0
  while [[ "$i" -lt "$timeout" ]]; do
    if kubectl logs -n "$ns" "$pod" --tail="$tail_lines" 2>/dev/null | grep -Fq "$grep_pattern"; then
      echo "beru log matched"
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done

  echo "timeout waiting for beru log (${timeout}s): ${grep_pattern}" >&2
  kubectl logs -n "$ns" "$pod" --tail=30 2>/dev/null >&2 || true
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
