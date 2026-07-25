#!/usr/bin/env bash
# Deploy / ensure pixie-gate (in-cluster PixieStreamRule → PxL gateway).
#
# Usage:
#   ./testing/bats/setup/start-pixie-stream-bridge.sh
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../../.." && pwd)}"
# shellcheck source=testing/bats/helpers/pixie-bridge.sh
source "$REPO/testing/bats/helpers/pixie-bridge.sh"

deploy_pixie_gate
