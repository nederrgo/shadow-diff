#!/usr/bin/env bash
# Local smoke: OTLP logs -> Siphon :4317 -> HTTP POST (no Kubernetes).
# Verifies OTLP parsing, query-string URL resolution, and trace header forwarding.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"
# shellcheck source=testing/scripts/lib/siphon-otlp.sh
source "$REPO/testing/scripts/lib/siphon-otlp.sh"

ensure_go_path

SIPHON_PORT="${SIPHON_PORT:-4317}"
IGRIS_PORT="${IGRIS_PORT:-18080}"
TRACE_ID="${TRACE_ID:-local-otlp-$(date +%s)}"
TRACE_QUERY="${TRACE_ID}-query"

tmpdir=$(mktemp -d)
trap 'kill "${SIPHON_PID:-}" "${IGRIS_PID:-}" 2>/dev/null || true; rm -rf "$tmpdir"' EXIT

cat >"${tmpdir}/igris.log" <<'EOF'
EOF

python3 - "$IGRIS_PORT" "$tmpdir/igris.log" <<'PY' &
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

port = int(sys.argv[1])
log_path = sys.argv[2]

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self._handle()
    def do_POST(self):
        self._handle()
    def _handle(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n) if n else b""
        rec = {
            "method": self.command,
            "path": self.path,
            "trace": self.headers.get("x-shadow-trace-id"),
            "traceparent": self.headers.get("traceparent"),
            "body": body.decode("utf-8", "replace"),
        }
        with open(log_path, "a") as f:
            f.write(json.dumps(rec) + "\n")
        self.send_response(202)
        self.end_headers()
    def log_message(self, *_):
        pass

HTTPServer(("127.0.0.1", port), H).serve_forever()
PY
IGRIS_PID=$!
sleep 0.5

make -C "${REPO}/pipeline/siphon" build >/dev/null

SIPHON_IGRIS_BASE_URL="http://127.0.0.1:${IGRIS_PORT}" \
  SIPHON_OTLP_GRPC_ADDR="127.0.0.1:${SIPHON_PORT}" \
  "${REPO}/pipeline/siphon/bin/siphon" &
SIPHON_PID=$!
sleep 1

send_otlp_http_log "127.0.0.1:${SIPHON_PORT}" "$TRACE_ID" GET "/?smoke=1" ""
send_otlp_http_log "127.0.0.1:${SIPHON_PORT}" "$TRACE_QUERY" POST "/v1/users?active=true" '{"ok":true}'

sleep 1

if ! grep -Fq "\"trace\": \"${TRACE_ID}\"" "$tmpdir/igris.log"; then
  log_fail "fake igris did not receive trace ${TRACE_ID}"
  cat "$tmpdir/igris.log" >&2
  exit 1
fi
if ! grep -Fq '"/v1/users?active=true"' "$tmpdir/igris.log"; then
  log_fail "query string not preserved (check resolveIgrisURL)"
  cat "$tmpdir/igris.log" >&2
  exit 1
fi
if grep -Fq '%3F' "$tmpdir/igris.log"; then
  log_fail "found percent-encoded query in forwarded path"
  cat "$tmpdir/igris.log" >&2
  exit 1
fi

log_success "local OTLP ingress smoke OK (traces ${TRACE_ID}, ${TRACE_QUERY})"
