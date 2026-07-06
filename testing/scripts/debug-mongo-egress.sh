#!/usr/bin/env bash
# Debug the Pixie → Beru MongoDB egress pipeline layer by layer.
# Run while the E2E test cluster is up (ShadowTest must be in Ready state).
#
# Usage:
#   ./testing/scripts/debug-mongo-egress.sh [shadowtest-name] [shadowtest-namespace]
#
# Example:
#   SHADOWTEST=rmq-mongo-test-shadow ./testing/scripts/debug-mongo-egress.sh
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
SHADOWTEST="${1:-${SHADOWTEST:-rmq-mongo-test-shadow}}"
SHADOWTEST_NS="${2:-${SHADOWTEST_NS:-default}}"
PIXIE_BRIDGE_STATE_DIR="${PIXIE_BRIDGE_STATE_DIR:-${REPO}/.cache/pixie-bridge}"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

ok()   { echo -e "${GREEN}[OK]${NC}    $*"; }
fail() { echo -e "${RED}[FAIL]${NC}  $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC}  $*"; }
hdr()  { echo; echo "━━━ $* ━━━"; }

# ── Layer 1: PixieStreamRule ──────────────────────────────────────────────────
hdr "Layer 1: PixieStreamRule"

rule_name="pixie-${SHADOWTEST}"
rule_json=$(kubectl get pixiestreamrule "$rule_name" -n "$SHADOWTEST_NS" -o json 2>/dev/null || true)

if [[ -z "$rule_json" ]]; then
  fail "PixieStreamRule '${rule_name}' not found in namespace '${SHADOWTEST_NS}'"
  echo "      → Monarch may not have created it (check that ShadowTest is Ready and has a mongodb dependency)"
  echo "      → Fix: ensure pixieCaptureEnabled returns true when hasMongoDependency(st)"
else
  active=$(echo "$rule_json" | jq -r '.spec.active')
  shadow_ns=$(echo "$rule_json" | jq -r '.spec.shadowNamespace // ""')
  mongo_ep=$(echo "$rule_json" | jq -r '.spec.mongoOtelEndpoint // ""')
  phase=$(echo "$rule_json" | jq -r '.status.phase // ""')

  [[ "$active" == "true" ]] && ok "active=true" || fail "active=${active} — bridge will skip this rule"
  [[ -n "$shadow_ns" ]]    && ok "shadowNamespace=${shadow_ns}" || fail "shadowNamespace is empty — MongoDB PxL won't filter correctly"
  [[ -n "$mongo_ep" ]]     && ok "mongoOtelEndpoint=${mongo_ep}" || fail "mongoOtelEndpoint is empty — bridge will not run mongo PxL"
  echo "      status.phase=${phase:-<unset>}"

  SHADOW_NS="$shadow_ns"
  MONGO_EP="$mongo_ep"
fi

SHADOW_NS="${SHADOW_NS:-}"
MONGO_EP="${MONGO_EP:-}"

# ── Layer 2: pixie-stream-bridge ─────────────────────────────────────────────
hdr "Layer 2: pixie-stream-bridge process"

pid_file="${PIXIE_BRIDGE_STATE_DIR}/bridge.pid"
if [[ -f "$pid_file" ]] && kill -0 "$(cat "$pid_file")" 2>/dev/null; then
  ok "bridge running pid=$(cat "$pid_file")"
else
  fail "pixie-stream-bridge is NOT running (pid file: ${pid_file})"
  echo "      → Start with: ./testing/scripts/start-pixie-stream-bridge.sh"
fi

hdr "Layer 2b: rendered PxL files"

mongo_pxl=$(ls "${PIXIE_BRIDGE_STATE_DIR}/${SHADOWTEST_NS}-${rule_name}-mongo.pxl" 2>/dev/null || true)
if [[ -n "$mongo_pxl" ]]; then
  ok "mongo PxL rendered: ${mongo_pxl}"
  echo
  echo "      ── PxL content ──────────────────────────────────────"
  sed 's/^/      /' "$mongo_pxl"
  echo "      ─────────────────────────────────────────────────────"
else
  fail "No mongo PxL file found at ${PIXIE_BRIDGE_STATE_DIR}/${SHADOWTEST_NS}-${rule_name}-mongo.pxl"
  echo "      → Either the bridge hasn't run yet, or mongoOtelEndpoint was empty when it ran"
  echo "      → Check bridge log: ${PIXIE_BRIDGE_STATE_DIR}/bridge.log"
fi

hdr "Layer 2c: bridge log (last 30 lines)"
if [[ -f "${PIXIE_BRIDGE_STATE_DIR}/bridge.log" ]]; then
  tail -30 "${PIXIE_BRIDGE_STATE_DIR}/bridge.log" | sed 's/^/      /'
else
  warn "No bridge.log found at ${PIXIE_BRIDGE_STATE_DIR}/bridge.log"
fi

# ── Layer 3: Pixie tcp_events probe ──────────────────────────────────────────
hdr "Layer 3: Pixie tcp_events — is port 27017 captured?"

if ! command -v px >/dev/null 2>&1; then
  warn "px CLI not found — skipping Pixie data probe"
else
  probe=$(mktemp /tmp/px-probe-XXXX.pxl)
  cat >"$probe" <<'EOF'
import px
df = px.DataFrame(table='tcp_events', start_time='-120s')
df.namespace = df.ctx['namespace']
df.pod       = df.ctx['pod']
df = df[df.remote_port == 27017]
px.display(df[['time_', 'namespace', 'pod', 'remote_port', 'req']].head(20))
EOF

  echo "      Running: px run -f $probe (timeout 30s)"
  set +e
  px_out=$(timeout 30 px run -f "$probe" 2>&1)
  px_exit=$?
  set -e
  rm -f "$probe"

  if [[ $px_exit -ne 0 ]]; then
    fail "px run failed (exit ${px_exit})"
    echo "$px_out" | tail -10 | sed 's/^/      /'
    echo "      → Check: px get viziers   (should show CS_HEALTHY)"
  else
    row_count=$(echo "$px_out" | grep -c "27017" || true)
    if [[ "${row_count:-0}" -gt 0 ]]; then
      ok "Pixie sees ${row_count} tcp_events row(s) on port 27017"
      if [[ -n "$SHADOW_NS" ]]; then
        shadow_rows=$(echo "$px_out" | grep -c "$SHADOW_NS" || true)
        [[ "${shadow_rows:-0}" -gt 0 ]] && ok "${shadow_rows} row(s) in shadow namespace ${SHADOW_NS}" \
          || warn "0 rows in shadow namespace ${SHADOW_NS} — wrong namespace filter?"
      fi
      echo "$px_out" | head -25 | sed 's/^/      /'
    else
      fail "Pixie returned 0 rows on port 27017 in the last 120s"
      echo "      → Either no MongoDB traffic happened, or Pixie PEM isn't tracing TCP"
      echo "      → Try sending a MongoDB write and re-running this script"
      echo "$px_out" | tail -5 | sed 's/^/      /'
    fi
  fi
fi

# ── Layer 4: Beru OTLP reachability ──────────────────────────────────────────
hdr "Layer 4: Beru OTLP port reachability from bridge node"

if [[ -n "$SHADOW_NS" && -n "$MONGO_EP" ]]; then
  beru_host="${MONGO_EP%%:*}"
  beru_port="${MONGO_EP##*:}"

  echo "      Testing: nc -z ${beru_host} ${beru_port} (via kubectl exec into a shadow pod)"
  test_pod=$(kubectl get pods -n "$SHADOW_NS" --no-headers 2>/dev/null | awk '$3=="Running"{print $1; exit}')
  if [[ -n "$test_pod" ]]; then
    if kubectl exec -n "$SHADOW_NS" "$test_pod" -- nc -z "$beru_host" "$beru_port" 2>/dev/null; then
      ok "beru-local OTLP port ${beru_port} is reachable from shadow namespace"
    else
      fail "Cannot reach ${beru_host}:${beru_port} — beru-local may not be running"
      echo "      → Check: kubectl get pods -n ${SHADOW_NS} | grep beru"
    fi
  else
    warn "No Running pod found in ${SHADOW_NS} to test connectivity"
  fi

  echo
  echo "      beru-local pods in ${SHADOW_NS}:"
  kubectl get pods -n "$SHADOW_NS" -l app=beru-local --no-headers 2>/dev/null | sed 's/^/        /' || echo "        (none found)"
else
  warn "Skipping reachability check — shadow namespace or endpoint unknown"
fi

# ── Layer 5: Beru logs ────────────────────────────────────────────────────────
hdr "Layer 5: beru-local logs — OTLP / MongoDB activity"

if [[ -n "$SHADOW_NS" ]]; then
  beru_pod=$(kubectl get pods -n "$SHADOW_NS" -l app=beru-local --no-headers 2>/dev/null | awk '{print $1; exit}')
  if [[ -n "$beru_pod" ]]; then
    echo "      Pod: ${beru_pod}"
    echo
    echo "      ── All OTLP/Mongo log lines (last 200) ──────────────"
    kubectl logs -n "$SHADOW_NS" "$beru_pod" --tail=200 2>/dev/null \
      | grep -iE "OTLP|mongo|export|routed|skipped|warn|error" \
      | sed 's/^/      /' \
      || echo "      (no matching lines)"
    echo
    echo "      ── Last 20 lines (raw) ──────────────────────────────"
    kubectl logs -n "$SHADOW_NS" "$beru_pod" --tail=20 2>/dev/null | sed 's/^/      /'
  else
    fail "No beru-local pod found in ${SHADOW_NS}"
    echo "      → kubectl get pods -n ${SHADOW_NS}"
  fi
else
  warn "Skipping beru-local log check — shadow namespace unknown"
fi

# ── Summary ───────────────────────────────────────────────────────────────────
hdr "What to check next based on findings"
cat <<'HINTS'
  Layer 1 FAIL  → Monarch didn't create PixieStreamRule. Check `hasMongoDependency` and `pixieCaptureEnabled`.
  Layer 2 FAIL  → Bridge not running. Run: ./testing/scripts/start-pixie-stream-bridge.sh
  Layer 2b FAIL → Bridge ran but didn't render mongo.pxl. mongoOtelEndpoint was empty when bridge ran.
                  Restart bridge after Monarch sets the endpoint.
  Layer 3 FAIL  → Pixie isn't seeing MongoDB traffic. Verify:
                  a) Shadow pods are actually running MongoDB writes
                  b) Pixie PEM is healthy: kubectl get pods -n pl
                  c) debugfs is mounted: minikube ssh -- mount | grep debugfs
  Layer 4 FAIL  → beru-local isn't up or its OTLP port isn't exposed. Check the beru-local Service.
  Layer 5 shows "OTLP export received" with mongo_spans=0
              → Pixie spans arrive but aren't recognised as MongoDB.
                  Check: span has db.system=mongodb and db.raw_payload is non-empty.
  Layer 5 shows "OTLP Mongo: no trace ID"
              → Pixie captured traffic but $comment has no traceparent.
                  Check: mongo-driver SetComment(traceparent) is being called.
  Layer 5 shows "OTLP Mongo: routed" but no "No egress regression"
              → Beru is receiving spans for the right trace but diff hasn't completed.
                  All 3 roles (control-a, control-b, candidate) must arrive before diff runs.
                  Check signature consistency across roles.
  Layer 5 shows nothing at all
              → Beru OTLP port isn't reachable from the Pixie bridge host (Layer 4).
HINTS
