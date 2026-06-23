#!/usr/bin/env bash
# Start pixie-stream-bridge in the background (creates .cache/pixie-bridge first).
#
# Usage (from any directory):
#   /home/projects/monarch/testing/scripts/start-pixie-stream-bridge.sh
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/pixie-bridge.sh
source "$REPO/testing/scripts/lib/pixie-bridge.sh"
start_pixie_stream_bridge_background
