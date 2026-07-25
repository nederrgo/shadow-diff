#!/usr/bin/env bash
# Retired: host-side PixieStreamRule bridge.
# Capture is handled by the in-cluster pixie-gate Deployment.
#
# Prefer:
#   kubectl apply -k pipeline/pixie-gate/deploy/
#   ./testing/bats/setup/start-pixie-stream-bridge.sh   # deploys pixie-gate
#
# For local debug of a single rendered .pxl:
#   px run -f /tmp/example.pxl
#
set -euo pipefail
echo "ERROR: testing/bats/pixie-stream-bridge.sh has been replaced by pipeline/pixie-gate" >&2
echo "       Deploy with: ./testing/bats/setup/start-pixie-stream-bridge.sh" >&2
exit 1
