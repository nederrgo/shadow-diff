# Shared helpers for Monarch E2E bash scripts.
# shellcheck shell=bash

log_success() {
  echo "[SUCCESS] $*"
}

log_fail() {
  echo "[FAIL] $*" >&2
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    log_fail "missing command: $1"
    exit 1
  }
}
