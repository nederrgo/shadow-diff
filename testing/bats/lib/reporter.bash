# Jest-like bats output via tap-mocha-reporter (BATS_PARALLEL_JOBS=1 only).
# shellcheck shell=bash

bats_reporter_js() {
  echo "${BATS_DIR}/node_modules/tap-mocha-reporter/index.js"
}

bats_reporter_bin() {
  echo "${BATS_DIR}/node_modules/.bin/tap-mocha-reporter"
}

# Resolve a usable Node binary. On WSL, `npm` is often Windows
# (/mnt/c/Program Files/nodejs/npm) while Linux `node` is absent — use node.exe beside it.
bats_resolve_node() {
  if [[ -n "${BATS_NODE:-}" && -x "${BATS_NODE}" ]]; then
    echo "${BATS_NODE}"
    return 0
  fi
  local c
  for c in node nodejs node.exe; do
    if command -v "$c" >/dev/null 2>&1; then
      command -v "$c"
      return 0
    fi
  done
  local npm_path npm_dir
  npm_path="$(command -v npm 2>/dev/null || true)"
  if [[ -n "$npm_path" ]]; then
    npm_dir="$(cd "$(dirname "$npm_path")" && pwd)"
    if [[ -x "${npm_dir}/node.exe" ]]; then
      echo "${npm_dir}/node.exe"
      return 0
    fi
    if [[ -x "${npm_dir}/node" ]]; then
      echo "${npm_dir}/node"
      return 0
    fi
  fi
  return 1
}

bats_have_node() {
  bats_resolve_node >/dev/null 2>&1
}

bats_ensure_reporter() {
  local js bin
  js="$(bats_reporter_js)"
  bin="$(bats_reporter_bin)"

  if ! bats_have_node; then
    echo "ERROR: Jest-like reporter needs Node.js (node/nodejs/node.exe on PATH, or next to npm)." >&2
    echo "       WSL tip: Windows Node is fine — ensure '/mnt/c/Program Files/nodejs' is on PATH," >&2
    echo "       or: export BATS_NODE='/mnt/c/Program Files/nodejs/node.exe'" >&2
    echo "       Or install a Linux Node, or unset BATS_REPORTER." >&2
    return 1
  fi

  if [[ -f "$js" ]]; then
    chmod +x "$bin" 2>/dev/null || true
    return 0
  fi

  if ! command -v npm >/dev/null 2>&1; then
    echo "ERROR: tap-mocha-reporter missing and npm not found — run: npm ci --prefix ${BATS_DIR}" >&2
    return 1
  fi
  echo "==> [bats] npm ci --prefix ${BATS_DIR} (tap-mocha-reporter)" >&2
  npm ci --prefix "${BATS_DIR}" || {
    echo "ERROR: npm ci failed — run: npm ci --prefix ${BATS_DIR}" >&2
    return 1
  }
  [[ -f "$js" ]] || {
    echo "ERROR: ${js} missing after npm ci" >&2
    return 1
  }
  chmod +x "$bin" 2>/dev/null || true
}

# Returns: spec | pretty | tap | off | "" (bats default)
bats_resolve_reporter() {
  local jobs="${BATS_PARALLEL_JOBS:-1}"
  local want="${BATS_REPORTER:-}"

  if [[ -n "$want" ]]; then
    case "$want" in
      spec)
        if [[ "$jobs" -gt 1 ]]; then
          echo "WARN: BATS_REPORTER=spec requires BATS_PARALLEL_JOBS=1 (multi-process TAP is interleaved); using native bats output" >&2
          echo "off"
          return 0
        fi
        echo "spec"
        return 0
        ;;
      pretty|tap|off)
        echo "$want"
        return 0
        ;;
      *)
        echo "WARN: unknown BATS_REPORTER=${want} (use spec|pretty|tap|off); using bats default" >&2
        echo ""
        return 0
        ;;
    esac
  fi

  # Auto: Jest-like only on an interactive TTY with a single bats process.
  if [[ "$jobs" -eq 1 ]] && [[ -t 1 ]]; then
    if ! bats_have_node; then
      echo "WARN: auto Jest-like reporter skipped (no Node found); using bats default." >&2
      echo ""
      return 0
    fi
    echo "spec"
    return 0
  fi
  echo ""
}

bats_run_mocha_spec() {
  local node_bin
  node_bin="$(bats_resolve_node)" || return 1
  # Prefer package entry (avoids non-executable shim / missing shebang path).
  "$node_bin" "$(bats_reporter_js)" spec
}

bats_invoke() {
  local mode
  mode="$(bats_resolve_reporter)"

  case "$mode" in
    spec)
      bats_ensure_reporter || return 1
      # pipefail: bats failure must be the make/run exit code
      set -o pipefail
      "${BATS_BIN}" --formatter tap --timing "$@" | bats_run_mocha_spec
      ;;
    pretty)
      "${BATS_BIN}" --formatter pretty --timing "$@"
      ;;
    tap)
      "${BATS_BIN}" --formatter tap --timing "$@"
      ;;
    off|"")
      "${BATS_BIN}" "$@"
      ;;
    *)
      "${BATS_BIN}" "$@"
      ;;
  esac
}
