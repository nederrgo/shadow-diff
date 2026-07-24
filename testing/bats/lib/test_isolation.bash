# Per-test isolation between @test blocks sharing one ShadowTest CR.
# shellcheck shell=bash

isolate_test_state() {
  export BATS_TRACE_ID="$(openssl rand -hex 16)"
  export BATS_SPAN_ID="$(openssl rand -hex 8)"
  export BATS_ORDER_ID="bats-${BATS_TRACE_ID:0:8}"
  export BATS_TRACE_TP="00-${BATS_TRACE_ID}-${BATS_SPAN_ID}-01"

  case "${BATS_ISOLATE_MODE:-trace}" in
    wipe-beru|full)
      [[ "${BATS_WIPE_BERU_EACH_TEST:-0}" == "1" || "${BATS_ISOLATE_MODE}" == "wipe-beru" || "${BATS_ISOLATE_MODE}" == "full" ]] \
        && beru_wipe_sqlite_tables || true
      ;;
  esac
  [[ "${BATS_ISOLATE_MODE:-trace}" == "full" ]] && reset_dependency_state || true
}

beru_local_pod() {
  local shadow_ns="${1:-${SHADOW_NS:-}}"
  kubectl get pods -n "$shadow_ns" -l app=beru-local \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null \
    || kubectl get pods -n "$shadow_ns" --no-headers 2>/dev/null | awk '/^beru-local-/{print $1; exit}'
}

beru_wipe_sqlite_tables() {
  local pod ns="${SHADOW_NS:-}"
  pod="$(beru_local_pod "$ns")"
  [[ -n "$pod" ]] || return 1
  command -v sqlite3 >/dev/null 2>&1 || return 1

  local tmp db_path="/data/beru.db"
  tmp="$(mktemp "${TMPDIR:-/tmp}/beru-wipe-XXXXXX.db")"
  kubectl cp "${ns}/${pod}:${db_path}" "$tmp" || { rm -f "$tmp"; return 1; }
  sqlite3 "$tmp" "DELETE FROM raw_reports; DELETE FROM verdicts;" || { rm -f "$tmp"; return 1; }
  kubectl cp "$tmp" "${ns}/${pod}:${db_path}" || { rm -f "$tmp"; return 1; }
  rm -f "$tmp"
}

reset_dependency_state() {
  : # scenario-specific; extend when global Mongo/RMQ asserts are added
}
