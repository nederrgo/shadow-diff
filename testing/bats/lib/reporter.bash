# Jest-like bats output via tap-mocha-reporter (BATS_PARALLEL_JOBS=1 only).
# shellcheck shell=bash

bats_reporter_js() {
  echo "${BATS_DIR}/node_modules/tap-mocha-reporter/index.js"
}

bats_reporter_bin() {
  echo "${BATS_DIR}/node_modules/.bin/tap-mocha-reporter"
}

# True if path looks like a native Linux node (ANSI colors work in WSL terminals).
bats_is_linux_node() {
  local p="$1"
  [[ -n "$p" ]] || return 1
  [[ "$p" == *.exe ]] && return 1
  [[ "$p" == /mnt/c/* || "$p" == /mnt/c\\* ]] && return 1
  return 0
}

# Resolve a usable Node binary. Prefer Linux node over Windows node.exe (WSL colors).
bats_resolve_node() {
  if [[ -n "${BATS_NODE:-}" && -x "${BATS_NODE}" ]]; then
    echo "${BATS_NODE}"
    return 0
  fi

  local c cand
  local -a linux_cands=() win_cands=()

  for c in node nodejs; do
    if cand="$(command -v "$c" 2>/dev/null)"; then
      if bats_is_linux_node "$cand"; then
        linux_cands+=("$cand")
      else
        win_cands+=("$cand")
      fi
    fi
  done
  if cand="$(command -v node.exe 2>/dev/null)"; then
    win_cands+=("$cand")
  fi

  local npm_path npm_dir
  npm_path="$(command -v npm 2>/dev/null || true)"
  if [[ -n "$npm_path" ]]; then
    npm_dir="$(cd "$(dirname "$npm_path")" && pwd)"
    if [[ -x "${npm_dir}/node" ]]; then
      if bats_is_linux_node "${npm_dir}/node"; then
        linux_cands+=("${npm_dir}/node")
      else
        win_cands+=("${npm_dir}/node")
      fi
    fi
    if [[ -x "${npm_dir}/node.exe" ]]; then
      win_cands+=("${npm_dir}/node.exe")
    fi
  fi

  if ((${#linux_cands[@]} > 0)); then
    echo "${linux_cands[0]}"
    return 0
  fi
  if ((${#win_cands[@]} > 0)); then
    echo "${win_cands[0]}"
    return 0
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
    echo "ERROR: Jest-like reporter needs Node.js on PATH (prefer Linux node for colors)." >&2
    echo "       Install: sudo apt install -y nodejs   # or nvm" >&2
    echo "       Or: export BATS_NODE=/usr/bin/node" >&2
    echo "       Or unset BATS_REPORTER." >&2
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
      verbose)
        echo "verbose"
        return 0
        ;;
      pretty|tap|off)
        echo "$want"
        return 0
        ;;
      *)
        echo "WARN: unknown BATS_REPORTER=${want} (use spec|pretty|tap|verbose|off); using bats default" >&2
        echo ""
        return 0
        ;;
    esac
  fi

  # Auto: Jest-like whenever a single bats process runs (jobs=1).
  # Do not require [[ -t 1 ]] — `make test-bats-e2e` often fails the TTY check
  # and would otherwise fall back to raw TAP.
  if [[ "$jobs" -eq 1 ]]; then
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

# Verbose formatter: clean ✓ for passing tests, ✗ with full inline diagnostics for failures.
# Passing tests show only the result line; failing tests show all captured output immediately
# below the ✗ mark so the context is visible without scrolling to a summary section.
bats_run_verbose() {
  local pass fail
  if [[ "${BATS_NO_COLOR:-0}" == "1" || "${FORCE_COLOR:-}" == "0" ]]; then
    pass='✓'; fail='✗'
  else
    pass=$'\033[32m✓\033[0m'; fail=$'\033[31m✗\033[0m'
  fi

  local line
  local -i n_pass=0 n_fail=0
  while IFS= read -r line; do
    if [[ "$line" =~ ^[0-9]+\.\.[0-9]+$ ]]; then
      continue
    elif [[ "$line" =~ ^ok\ [0-9]+\ (.*) ]]; then
      printf '  %s %s\n' "$pass" "${BASH_REMATCH[1]}"
      (( n_pass++ )) || true
    elif [[ "$line" =~ ^not\ ok\ [0-9]+\ (.*) ]]; then
      printf '  %s %s\n' "$fail" "${BASH_REMATCH[1]}"
      (( n_fail++ )) || true
    elif [[ "$line" == '# '* ]]; then
      printf '    %s\n' "${line:2}"
    else
      printf '%s\n' "$line"
    fi
  done

  if [[ "${BATS_NO_COLOR:-0}" == "1" || "${FORCE_COLOR:-}" == "0" ]]; then
    printf '\n  %d passed, %d failed\n' "$n_pass" "$n_fail"
  elif [[ "$n_fail" -gt 0 ]]; then
    printf '\n  \033[32m%d passed\033[0m, \033[31m%d failed\033[0m\n' "$n_pass" "$n_fail"
  else
    printf '\n  \033[32m%d passed\033[0m\n' "$n_pass"
  fi
}

bats_run_mocha_spec() {
  local node_bin
  node_bin="$(bats_resolve_node)" || return 1

  # Default: colored √ / pass / fail. Opt out with BATS_NO_COLOR=1 (or FORCE_COLOR=0).
  if [[ "${BATS_NO_COLOR:-0}" == "1" || "${FORCE_COLOR:-}" == "0" ]]; then
    env -u FORCE_COLOR TAP_COLORS=0 "$node_bin" "$(bats_reporter_js)" spec
    return
  fi
  FORCE_COLOR="${FORCE_COLOR:-1}" TAP_COLORS="${TAP_COLORS:-1}" \
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
    verbose)
      set -o pipefail
      "${BATS_BIN}" --formatter tap --timing "$@" | bats_run_verbose
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
