#!/usr/bin/env bash
# Send three JSON ReportTraffic calls to Beru and print recent logs.
# Requires: kubectl, grpcurl (go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest)
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
TRACE_ID="${TRACE_ID:-trace-json-1}"
export PATH="$(go env GOPATH 2>/dev/null || echo "$HOME/go")/bin:$PATH"

if ! command -v grpcurl >/dev/null 2>&1; then
  echo "grpcurl not found. Install with:"
  echo "  go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest"
  echo "  export PATH=\"\$(go env GOPATH)/bin:\$PATH\""
  exit 1
fi

# Reuse existing port-forward or start one
PF_PID=""
if ss -tln 2>/dev/null | grep -q ':50051 '; then
  echo "Port 50051 already in use (assuming port-forward is running)."
else
  echo "Starting kubectl port-forward to beru:50051 ..."
  kubectl port-forward -n beru-system svc/beru 50051:50051 >/dev/null 2>&1 &
  PF_PID=$!
  sleep 2
  if ! ss -tln 2>/dev/null | grep -q ':50051 '; then
    echo "Port-forward failed. Run manually in another terminal:"
    echo "  kubectl port-forward -n beru-system svc/beru 50051:50051"
    exit 1
  fi
fi

cleanup() {
  if [[ -n "$PF_PID" ]]; then
    kill "$PF_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# body must be base64 in grpcurl JSON (proto bytes field)
send() {
  local role=$1 body_b64=$2
  echo "==> $role"
  grpcurl -plaintext \
    -import-path "$REPO/pipeline/beru/api/proto" \
    -proto beru/v1/traffic.proto \
    -d "{\"report\":{\"trace_id\":\"$TRACE_ID\",\"role\":\"$role\",\"direction\":\"INGRESS\",\"payload\":{\"content_type\":\"application/json\",\"body\":\"$body_b64\"}}}" \
    localhost:50051 beru.v1.TrafficReporter/ReportTraffic
}

send control-a 'eyJwcmljZSI6MTAsInRpbWVzdGFtcCI6MX0='
send control-b 'eyJwcmljZSI6MTAsInRpbWVzdGFtcCI6Mn0='
send candidate 'eyJwcmljZSI6MTIsInRpbWVzdGFtcCI6Mn0='

echo ""
echo "==> Beru logs (expect regression on price):"
sleep 1
kubectl logs -n beru-system deployment/beru --tail=8
