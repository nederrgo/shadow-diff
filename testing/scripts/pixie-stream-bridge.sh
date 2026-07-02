#!/usr/bin/env bash
# Reconcile PixieStreamRule CRs → PxL px.export scripts targeting Siphon OTLP :4317.
#
# Run after setup-local-pixie.sh (Pixie Vizier in pl + px auth).
# ponytail: polls px run on an interval; upgrade path = Pixie plugin cron API.
#
# Usage:
#   ./testing/scripts/pixie-stream-bridge.sh
#   PIXIE_EXPORT_INTERVAL_SEC=3 ./testing/scripts/pixie-stream-bridge.sh
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/pixie-bridge.sh
source "$REPO/testing/scripts/lib/pixie-bridge.sh"

FOREGROUND=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --foreground) FOREGROUND=1 ;;
    -h|--help)
      sed -n '2,12p' "$0"
      exit 0
      ;;
    *) echo "Unknown flag: $1" >&2; exit 1 ;;
  esac
  shift
done

command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl required" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "ERROR: jq required" >&2; exit 1; }
command -v px >/dev/null 2>&1 || ensure_px_cli
ensure_pixie_auth

mkdir -p "$PIXIE_BRIDGE_STATE_DIR"
echo "==> PixieStreamRule bridge (export interval=${PIXIE_EXPORT_INTERVAL_SEC}s, state=${PIXIE_BRIDGE_STATE_DIR})"

reconcile_rules() {
  local rules_json rule_count i name ns active rule_json
  local otel_ep recorder_ep mongo_ep ingress_pxl egress_pxl mongo_pxl ok failed
  rules_json=$(kubectl get pixiestreamrules -A -o json 2>/dev/null || echo '{"items":[]}')
  rule_count=$(echo "$rules_json" | jq -r '.items | length' 2>/dev/null || echo 0)
  rule_count=${rule_count//[^0-9]/}
  [[ -z "$rule_count" ]] && rule_count=0
  i=0
  while (( i < rule_count )); do
    rule_json=$(echo "$rules_json" | jq -c ".items[$i]")
    active=$(echo "$rule_json" | jq -r '.spec.active // false')
    name=$(echo "$rule_json" | jq -r '.metadata.name')
    ns=$(echo "$rule_json" | jq -r '.metadata.namespace')
    otel_ep=$(echo "$rule_json" | jq -r '.spec.otelEndpoint // ""')
    recorder_ep=$(echo "$rule_json" | jq -r '.spec.recorderOtelEndpoint // ""')
    mongo_ep=$(echo "$rule_json" | jq -r '.spec.mongoOtelEndpoint // ""')
    ingress_pxl="${PIXIE_BRIDGE_STATE_DIR}/${ns}-${name}-ingress.pxl"
    egress_pxl="${PIXIE_BRIDGE_STATE_DIR}/${ns}-${name}-egress.pxl"
    mongo_pxl="${PIXIE_BRIDGE_STATE_DIR}/${ns}-${name}-mongo.pxl"
    if [[ "$active" == "true" ]]; then
      ok=0
      failed=""
      if [[ -n "$otel_ep" ]]; then
        render_pixie_ingress_pxl "$rule_json" "$ingress_pxl"
        if run_pixie_export_once "$ingress_pxl"; then
          ok=1
        else
          failed="ingress"
        fi
      else
        rm -f "$ingress_pxl"
      fi
      if [[ -n "$recorder_ep" ]]; then
        render_pixie_egress_pxl "$rule_json" "$egress_pxl"
        if run_pixie_export_once "$egress_pxl"; then
          ok=1
        else
          failed="${failed:+$failed+}egress"
        fi
      else
        rm -f "$egress_pxl"
      fi
      if [[ -n "$mongo_ep" ]]; then
        render_pixie_mongo_pxl "$rule_json" "$mongo_pxl"
        if run_pixie_export_once "$mongo_pxl"; then
          ok=1
        else
          failed="${failed:+$failed+}mongo"
        fi
      else
        rm -f "$mongo_pxl"
      fi
      if [[ -z "$otel_ep" && -z "$recorder_ep" && -z "$mongo_ep" ]]; then
        patch_pixie_stream_rule_status "$ns" "$name" "Inactive" "no export endpoints"
      elif [[ -n "$failed" ]]; then
        patch_pixie_stream_rule_status "$ns" "$name" "Error" "px.export failed ($failed)"
      else
        patch_pixie_stream_rule_status "$ns" "$name" "Active" "px.export ok"
      fi
    else
      rm -f "${PIXIE_BRIDGE_STATE_DIR}/${ns}-${name}"*.pxl
      patch_pixie_stream_rule_status "$ns" "$name" "Inactive" "spec.active=false"
    fi
    i=$((i + 1))
  done
}

trap 'echo "==> pixie-stream-bridge stopping"; exit 0' INT TERM

while true; do
  reconcile_rules
  sleep "$PIXIE_EXPORT_INTERVAL_SEC"
done
