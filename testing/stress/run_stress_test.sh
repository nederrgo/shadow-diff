#!/usr/bin/env bash
# End-to-end stress suite: record → zero-loss checks → replay → Postgres integrity.
#
# Standalone: does NOT bootstrap Kind/Monarch. See README.md for prerequisites.
#
# Usage:
#   source testing/stress/config.env   # optional
#   ./testing/stress/run_stress_test.sh [--skip-apply] [--skip-load] [--n N] [--rps-end R]
set -euo pipefail

STRESS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "${STRESS_DIR}/../.." && pwd)"
WORK_DIR="${STRESS_WORK_DIR:-${STRESS_DIR}/.run}"
REFERENCE="${WORK_DIR}/stress_sent_traces.json"
EBPF_BASELINE="${WORK_DIR}/ebpf_baseline.json"
SUMMARY_ROWS=()

# Load config.env defaults without clobbering env vars already set (e.g. STRESS_N=10 make test-stress).
if [[ -f "${STRESS_DIR}/config.env" ]]; then
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ "$line" =~ ^[[:space:]]*# ]] && continue
    [[ "$line" =~ ^[[:space:]]*$ ]] && continue
    key="${line%%=*}"; key="${key// /}"
    val="${line#*=}"
    val="${val%%#*}"   # strip inline comments (e.g. STRESS_RUN_ID=  # auto UUID)
    val="${val%"${val##*[![:space:]]}"}"  # rtrim
    [[ -z "$val" ]] && continue
    if [[ -z "${!key+x}" ]]; then
      export "${key}=${val}"
    fi
  done <"${STRESS_DIR}/config.env"
fi

STRESS_SKIP_APPLY="${STRESS_SKIP_APPLY:-0}"
STRESS_SKIP_LOAD="${STRESS_SKIP_LOAD:-0}"
STRESS_FRESH_SESSION="${STRESS_FRESH_SESSION:-1}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-apply) STRESS_SKIP_APPLY=1; shift ;;
    --skip-load) STRESS_SKIP_LOAD=1; shift ;;
    --n) STRESS_N="$2"; shift 2 ;;
    --rps-end) STRESS_RPS_END="$2"; shift 2 ;;
    --rps-start) STRESS_RPS_START="$2"; shift 2 ;;
    --ramp-sec) STRESS_RAMP_SEC="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "$0"
      exit 0
      ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

SHADOWTEST="${SHADOWTEST:-bats-stress}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
STRESS_N="${STRESS_N:-1000}"
STRESS_RPS_START="${STRESS_RPS_START:-500}"
STRESS_RPS_END="${STRESS_RPS_END:-2000}"
STRESS_RAMP_SEC="${STRESS_RAMP_SEC:-60}"
KAISEL_NS="${KAISEL_NS:-kaisel-system}"
S3_FLUSH_WAIT_SEC="${S3_FLUSH_WAIT_SEC:-120}"
REPLAY_WAIT_SEC="${REPLAY_WAIT_SEC:-600}"
POSTGRES_STABLE_POLLS="${POSTGRES_STABLE_POLLS:-6}"
POSTGRES_STABLE_INTERVAL_SEC="${POSTGRES_STABLE_INTERVAL_SEC:-10}"
EXPECTED_S3_EGRESS_PER_REQ="${EXPECTED_S3_EGRESS_PER_REQ:-1}"
EXPECTED_EGRESS_HTTP="${EXPECTED_EGRESS_HTTP:-1}"
EXPECTED_EGRESS_AMQP="${EXPECTED_EGRESS_AMQP:-1}"
EXPECTED_EGRESS_DB="${EXPECTED_EGRESS_DB:-1}"
PROD_TARGET_URL="${PROD_TARGET_URL:-http://http-rmq-python-prod.default.svc.cluster.local:8080/publish}"
STRESS_LOAD_IN_CLUSTER="${STRESS_LOAD_IN_CLUSTER:-1}"

mkdir -p "$WORK_DIR"
export STRESS_N SHADOWTEST SHADOWTEST_NS KAISEL_NS
export EXPECTED_S3_EGRESS_PER_REQ EXPECTED_EGRESS_HTTP EXPECTED_EGRESS_AMQP EXPECTED_EGRESS_DB
export MINIO_BUCKET MINIO_ENDPOINT MINIO_ENDPOINT_HOST MINIO_ACCESS_KEY MINIO_SECRET_KEY
export POSTGRES_HOST POSTGRES_PORT POSTGRES_HOST_HOST POSTGRES_PORT_HOST
export POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB

pass() { SUMMARY_ROWS+=("PASS|$1|$2|$3"); echo "    PASS: $1 (expected=$2 actual=$3)"; }
fail() { SUMMARY_ROWS+=("FAIL|$1|$2|$3"); echo "    FAIL: $1 (expected=$2 actual=$3)" >&2; }
die() { echo "FATAL: $*" >&2; print_summary; exit 1; }

reference_trace_n() {
  python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["n"])' "$1"
}

print_summary() {
  echo
  echo "======== STRESS SUMMARY ========"
  printf "%-6s %-36s %-12s %-12s\n" "RESULT" "CHECK" "EXPECTED" "ACTUAL"
  local row status check exp act
  for row in "${SUMMARY_ROWS[@]:-}"; do
    IFS='|' read -r status check exp act <<<"$row"
    printf "%-6s %-36s %-12s %-12s\n" "$status" "$check" "$exp" "$act"
  done
  echo "================================"
  local r
  for r in "${SUMMARY_ROWS[@]:-}"; do
    [[ "$r" == FAIL* ]] && return 1
  done
  return 0
}

jsonpath() {
  kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o "jsonpath=$1" 2>/dev/null || true
}

wait_ready() {
  local timeout="${1:-300}" elapsed=0 phase kaisel
  echo "==> wait ShadowTest Ready + kaiselPhase=Ready (timeout=${timeout}s)"
  while true; do
    phase=$(jsonpath '{.status.phase}')
    kaisel=$(jsonpath '{.status.kaiselPhase}')
    if [[ "$phase" == "Ready" && "$kaisel" == "Ready" ]]; then
      echo "    phase=Ready kaiselPhase=Ready"
      return 0
    fi
    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: phase=${phase:-?} kaiselPhase=${kaisel:-?} after ${timeout}s" >&2
      kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o yaml >&2 || true
      return 1
    fi
    echo "    waiting (${elapsed}s) phase=${phase:-?} kaisel=${kaisel:-?}"
    sleep 5
    elapsed=$((elapsed + 5))
  done
}

preflight() {
  echo "==> preflight"
  command -v kubectl >/dev/null || die "kubectl not found"
  command -v go >/dev/null || die "go not found"
  command -v python3 >/dev/null || die "python3 not found"
  kubectl cluster-info >/dev/null 2>&1 || die "kubectl cluster unreachable"
  kubectl get ns "$KAISEL_NS" >/dev/null 2>&1 || die "missing namespace $KAISEL_NS (Kaisel)"
  kubectl get daemonset kaisel -n "$KAISEL_NS" >/dev/null 2>&1 || die "kaisel DaemonSet missing"
  kubectl get deploy postgres -n monarch-system >/dev/null 2>&1 || die "postgres deploy missing"
  kubectl get svc minio-service -n monarch-system >/dev/null 2>&1 || die "minio-service missing"
  kubectl get deploy http-rmq-python-prod -n default >/dev/null 2>&1 \
    || die "http-rmq-python-prod missing (prod stack)"
  echo "    ok"
}

build_load_gen() {
  echo "==> build load_gen (static)"
  (cd "${STRESS_DIR}/generator" && CGO_ENABLED=0 GOWORK=off go build -o "${WORK_DIR}/load_gen" .)
}

run_load() {
  echo "==> load_gen N=${STRESS_N} rps=${STRESS_RPS_START}→${STRESS_RPS_END}"
  if [[ "$STRESS_LOAD_IN_CLUSTER" == "1" ]] \
    && [[ "$PROD_TARGET_URL" != http://127.0.0.1* ]] \
    && [[ "$PROD_TARGET_URL" != http://localhost* ]]; then
    run_load_in_cluster
  else
    local args=(
      -url "$PROD_TARGET_URL"
      -n "$STRESS_N"
      -rps-start "$STRESS_RPS_START"
      -rps-end "$STRESS_RPS_END"
      -ramp-sec "$STRESS_RAMP_SEC"
      -out "$REFERENCE"
      -timeout "${STRESS_REQUEST_TIMEOUT_SEC:-30}"
    )
    [[ -n "${STRESS_RUN_ID:-}" ]] && args+=(-run-id "$STRESS_RUN_ID")
    "${WORK_DIR}/load_gen" "${args[@]}"
  fi
}

run_load_in_cluster() {
  local pod="stress-loadgen-${RANDOM}"
  echo "    starting in-cluster load pod ${pod}"
  kubectl run "$pod" -n default --restart=Never \
    --image=debian:bookworm-slim \
    --command -- sleep 3600 >/dev/null
  kubectl wait --for=condition=Ready "pod/${pod}" -n default --timeout=180s
  kubectl cp "${WORK_DIR}/load_gen" "default/${pod}:/tmp/load_gen"
  kubectl exec -n default "$pod" -- chmod +x /tmp/load_gen
  set +e
  kubectl exec -n default "$pod" -- /tmp/load_gen \
    -url "$PROD_TARGET_URL" \
    -n "$STRESS_N" \
    -rps-start "$STRESS_RPS_START" \
    -rps-end "$STRESS_RPS_END" \
    -ramp-sec "$STRESS_RAMP_SEC" \
    -out /tmp/stress_sent_traces.json \
    -timeout "${STRESS_REQUEST_TIMEOUT_SEC:-30}" \
    ${STRESS_RUN_ID:+-run-id "$STRESS_RUN_ID"}
  local rc=$?
  set -e
  kubectl cp "default/${pod}:/tmp/stress_sent_traces.json" "$REFERENCE" 2>/dev/null || true
  kubectl delete pod "$pod" -n default --wait=false --ignore-not-found >/dev/null 2>&1 || true
  [[ $rc -eq 0 ]] || die "load_gen failed (exit $rc)"
}

download_session_jsonl() {
  local session="$1" dest="$2"
  local prefix="shadow-diff/${SHADOWTEST_NS}/${SHADOWTEST}/sessions/${session}"
  local pod="stress-mc-dl-${RANDOM}"
  local bucket="${MINIO_BUCKET:-shadow-diff-local}"
  local endpoint="${MINIO_ENDPOINT:-http://minio-service.monarch-system.svc.cluster.local:9000}"
  local access="${MINIO_ACCESS_KEY:-admin}"
  local secret="${MINIO_SECRET_KEY:-password}"
  rm -rf "${dest}/ingress" "${dest}/egress"
  mkdir -p "${dest}/ingress" "${dest}/egress"
  echo "==> download session JSONL via mc → ${dest}"
  # minio/mc image has no tar — kubectl cp fails silently. Use debian + mc binary.
  kubectl run "$pod" -n default --restart=Never \
    --image=debian:bookworm-slim \
    --command -- sleep 600 >/dev/null
  kubectl wait --for=condition=Ready "pod/${pod}" -n default --timeout=180s
  kubectl exec -n default "$pod" -- env \
    "MC_ENDPOINT=${endpoint}" \
    "MC_ACCESS=${access}" \
    "MC_SECRET=${secret}" \
    "MC_BUCKET=${bucket}" \
    "MC_PREFIX=${prefix}" \
    sh -c '
      set -e
      apt-get update -qq && apt-get install -qq -y curl ca-certificates >/dev/null
      curl -fsSL https://dl.min.io/client/mc/release/linux-amd64/mc -o /usr/local/bin/mc
      chmod +x /usr/local/bin/mc
      mc alias set local "$MC_ENDPOINT" "$MC_ACCESS" "$MC_SECRET" >/dev/null
      mkdir -p /tmp/out/ingress /tmp/out/egress
      mc cp --recursive "local/${MC_BUCKET}/${MC_PREFIX}/ingress/" /tmp/out/ingress/ 2>/dev/null || true
      mc cp --recursive "local/${MC_BUCKET}/${MC_PREFIX}/egress/" /tmp/out/egress/ 2>/dev/null || true
    '
  kubectl cp "default/${pod}:/tmp/out/ingress/." "${dest}/ingress/" || die "kubectl cp ingress failed"
  kubectl cp "default/${pod}:/tmp/out/egress/." "${dest}/egress/" || die "kubectl cp egress failed"
  local n_ing n_egr
  n_ing=$(find "${dest}/ingress" -name '*.jsonl' 2>/dev/null | wc -l | tr -d ' ')
  n_egr=$(find "${dest}/egress" -name '*.jsonl' 2>/dev/null | wc -l | tr -d ' ')
  echo "    downloaded ingress_files=${n_ing} egress_files=${n_egr}"
  [[ "$n_ing" -gt 0 ]] || die "no ingress JSONL downloaded from S3 (kubectl cp or session empty)"
  kubectl delete pod "$pod" -n default --wait=false --ignore-not-found >/dev/null 2>&1 || true
}

wait_s3_flush() {
  local session="$1" timeout="${S3_FLUSH_WAIT_SEC}" elapsed=0
  local last=0 cur=0
  local prefix="shadow-diff/${SHADOWTEST_NS}/${SHADOWTEST}/sessions/${session}/"
  echo "==> wait S3 flush under ${prefix} (timeout=${timeout}s)"
  while true; do
    # minio/mc image has no grep — count .jsonl lines on the host.
    cur=$(kubectl run "stress-mc-${RANDOM}" --rm -i --restart=Never -n default \
      --image=minio/mc:latest \
      --env=HOME=/tmp --env=MC_CONFIG_DIR=/tmp/.mc \
      --env="MC_ENDPOINT=${MINIO_ENDPOINT:-http://minio-service.monarch-system.svc.cluster.local:9000}" \
      --env="MC_ACCESS=${MINIO_ACCESS_KEY:-admin}" \
      --env="MC_SECRET=${MINIO_SECRET_KEY:-password}" \
      --env="MC_BUCKET=${MINIO_BUCKET:-shadow-diff-local}" \
      --env="MC_PREFIX=${prefix}" \
      --command -- /bin/sh -c \
      'mc alias set local "$MC_ENDPOINT" "$MC_ACCESS" "$MC_SECRET" >/dev/null
       mc ls --recursive "local/${MC_BUCKET}/${MC_PREFIX}" 2>/dev/null || true' \
      2>/dev/null | grep -vE '^pod ".*" deleted' | grep -c '\.jsonl' || true)
    cur=${cur:-0}
    echo "    jsonl objects=${cur} (last=${last})"
    if [[ "$cur" -gt 0 && "$cur" -eq "$last" && "$elapsed" -ge 15 ]]; then
      echo "    stable"
      return 0
    fi
    if [[ "$elapsed" -ge "$timeout" ]]; then
      die "S3 flush did not stabilize (objects=${cur})"
    fi
    last=$cur
    sleep 5
    elapsed=$((elapsed + 5))
  done
}

patch_replay() {
  local session="$1"
  echo "==> patch mode=replay sessionID=${session}"
  kubectl patch shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --type=merge \
    -p "{\"spec\":{\"mode\":\"replay\",\"sessionID\":\"${session}\"}}"
}

wait_replay_started() {
  local timeout="${REPLAY_WAIT_SEC}" elapsed=0 state
  echo "==> wait replayState=started"
  while true; do
    state=$(jsonpath '{.status.replayState}')
    if [[ "$state" == "started" ]]; then
      echo "    replayState=started"
      return 0
    fi
    if [[ "$elapsed" -ge "$timeout" ]]; then
      die "replayState=${state:-empty} after ${timeout}s"
    fi
    sleep 3
    elapsed=$((elapsed + 3))
  done
}

wait_igris_replay_finished() {
  local timeout="${REPLAY_WAIT_SEC}" elapsed=0
  local shadow_ns igris_deploy logs
  shadow_ns=$(jsonpath '{.status.shadowNamespace}')
  [[ -n "$shadow_ns" ]] || die "status.shadowNamespace empty"
  igris_deploy="${SHADOWTEST}-igris"
  echo "==> wait igris 'replay loop finished' in ${shadow_ns}"
  while true; do
    logs=$(kubectl logs -n "$shadow_ns" "deploy/${igris_deploy}" --tail=200 2>/dev/null || true)
    if echo "$logs" | grep -Fq 'replay loop finished'; then
      echo "    replay loop finished"
      echo "$logs" | grep -F 'replay loop finished' | tail -1
      return 0
    fi
    if [[ "$elapsed" -ge "$timeout" ]]; then
      die "igris did not log replay loop finished after ${timeout}s"
    fi
    echo "    waiting igris replay (${elapsed}s)..."
    sleep 5
    elapsed=$((elapsed + 5))
  done
}

wait_postgres_stable() {
  local exec_id="$1"
  local polls="${POSTGRES_STABLE_POLLS}" interval="${POSTGRES_STABLE_INTERVAL_SEC}"
  local i last=-1 cur safe_id
  # Escape single quotes for SQL literal.
  safe_id="${exec_id//\'/\'\'}"
  echo "==> wait Postgres raw_reports stable for exec=${exec_id}"
  for ((i = 1; i <= polls; i++)); do
    cur=$(kubectl exec -n monarch-system deploy/postgres -- \
      env PGPASSWORD="${POSTGRES_PASSWORD:-beru}" \
      psql -U "${POSTGRES_USER:-beru}" -d "${POSTGRES_DB:-beru}" -Atc \
      "SELECT COUNT(*) FROM raw_reports WHERE replay_execution_id='${safe_id}'" 2>/dev/null || echo 0)
    cur=${cur:-0}
    echo "    poll ${i}/${polls}: rows=${cur}"
    if [[ "$cur" -eq "$last" && "$cur" -gt 0 ]]; then
      echo "    stable at ${cur}"
      return 0
    fi
    last=$cur
    sleep "$interval"
  done
  [[ "$cur" -gt 0 ]] || die "Postgres still empty for ${exec_id}"
  echo "    proceeding with rows=${cur} (may still be flushing)"
}

ensure_fresh_shadowtest() {
  if [[ "$STRESS_FRESH_SESSION" != "1" ]]; then
    echo "==> apply ShadowTest fixture (reuse session)"
    kubectl apply -f "${STRESS_DIR}/fixtures/shadowtest.yaml"
    return
  fi
  echo "==> fresh session: delete and recreate ${SHADOWTEST_NS}/${SHADOWTEST}"
  if kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" >/dev/null 2>&1; then
    # Must wait for CR + finalizers + shadow ns — not just the namespace.
    # Applying while deletionTimestamp is set patches a terminating CR (Kubernetes warning).
    WAIT_SECS="${STRESS_DELETE_WAIT_SEC:-180}" \
      "${REPO}/testing/bats/setup/delete-shadowtest.sh" "$SHADOWTEST" "$SHADOWTEST_NS" \
      || die "ShadowTest delete did not finish (finalizer / shadow ns)"
  fi
  if kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" >/dev/null 2>&1; then
    die "ShadowTest ${SHADOWTEST_NS}/${SHADOWTEST} still exists after delete"
  fi
  kubectl apply -f "${STRESS_DIR}/fixtures/shadowtest.yaml"
}

# ---- main ----
echo "==> stress suite work_dir=${WORK_DIR} N=${STRESS_N}"
preflight
build_load_gen
chmod +x "${STRESS_DIR}/verifiers/"*.sh

if [[ "$STRESS_SKIP_APPLY" != "1" ]]; then
  ensure_fresh_shadowtest
fi
wait_ready 300 || die "ShadowTest not Ready"

echo "==> eBPF baseline snapshot"
"${STRESS_DIR}/verifiers/check_ebpf_drops.sh" --snapshot "$EBPF_BASELINE"
pass "ebpf_baseline_written" "file" "$EBPF_BASELINE"

if [[ "$STRESS_SKIP_LOAD" != "1" ]]; then
  run_load
  [[ -f "$REFERENCE" ]] || die "missing reference $REFERENCE"
  pass "load_gen" "$STRESS_N" "$(reference_trace_n "$REFERENCE")"
else
  [[ -f "$REFERENCE" ]] || die "--skip-load requires existing $REFERENCE"
  echo "==> skip load (using existing reference)"
fi

SESSION_ID=$(jsonpath '{.status.currentSessionID}')
[[ -n "$SESSION_ID" ]] || die "status.currentSessionID empty"
export SESSION_ID
echo "==> session=${SESSION_ID}"

wait_s3_flush "$SESSION_ID"

echo "==> check eBPF drops"
if "${STRESS_DIR}/verifiers/check_ebpf_drops.sh" --baseline "$EBPF_BASELINE"; then
  pass "ebpf_zero_loss" "0" "0"
else
  fail "ebpf_zero_loss" "0" ">0"
fi

echo "==> check S3 counts"
S3_LOCAL="${WORK_DIR}/s3_session"
rm -rf "$S3_LOCAL"
download_session_jsonl "$SESSION_ID" "$S3_LOCAL"
set +e
python3 "${STRESS_DIR}/verifiers/check_s3_counts.py" \
  --reference "$REFERENCE" \
  --session-id "$SESSION_ID" \
  --namespace "$SHADOWTEST_NS" \
  --test-name "$SHADOWTEST" \
  --n "$STRESS_N" \
  --expected-egress-per-req "$EXPECTED_S3_EGRESS_PER_REQ" \
  --local-dir "$S3_LOCAL"
s3_rc=$?
set -e
if [[ $s3_rc -eq 0 ]]; then
  pass "s3_counts" "N + N*${EXPECTED_S3_EGRESS_PER_REQ}" "ok"
else
  fail "s3_counts" "N + N*${EXPECTED_S3_EGRESS_PER_REQ}" "mismatch"
  die "S3 count verification failed — fix before replay"
fi

patch_replay "$SESSION_ID"
wait_replay_started
wait_igris_replay_finished

EXEC_ID=$(jsonpath '{.status.currentReplayExecutionID}')
[[ -n "$EXEC_ID" ]] || die "status.currentReplayExecutionID empty"
export REPLAY_EXECUTION_ID="$EXEC_ID"
echo "==> replay_execution_id=${EXEC_ID}"

wait_postgres_stable "$EXEC_ID"

echo "==> check Postgres counts"
set +e
python3 "${STRESS_DIR}/verifiers/check_postgres_counts.py" \
  --reference "$REFERENCE" \
  --replay-execution-id "$EXEC_ID" \
  --n "$STRESS_N" \
  --http "$EXPECTED_EGRESS_HTTP" \
  --amqp "$EXPECTED_EGRESS_AMQP" \
  --db "$EXPECTED_EGRESS_DB"
pg_rc=$?
set -e
if [[ $pg_rc -eq 0 ]]; then
  pass "postgres_integrity" "3 roles × protocols" "ok"
else
  fail "postgres_integrity" "3 roles × protocols" "mismatch"
fi

if print_summary; then
  echo "STRESS RESULT: PASS"
  exit 0
fi
echo "STRESS RESULT: FAIL" >&2
exit 1
