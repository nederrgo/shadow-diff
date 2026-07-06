#!/usr/bin/env bash
# Invoke docker with a config that omits credsStore/credHelpers when those break
# public image pulls on WSL (error getting credentials - exit status 1).
set -euo pipefail

docker_invoke() {
  local user_cfg="${HOME}/.docker/config.json"
  if [[ -f "$user_cfg" ]] && grep -qE '"credsStore"|"credHelpers"' "$user_cfg" 2>/dev/null; then
    local tmpdir
    tmpdir=$(mktemp -d)
    # shellcheck disable=SC2064
    trap "rm -rf '$tmpdir'" RETURN
    python3 - "$user_cfg" "$tmpdir/config.json" <<'PY'
import json, sys
src, dst = sys.argv[1], sys.argv[2]
with open(src) as f:
    cfg = json.load(f)
cfg.pop("credsStore", None)
cfg.pop("credHelpers", None)
with open(dst, "w") as f:
    json.dump(cfg, f)
PY
    DOCKER_CONFIG="$tmpdir" docker "$@"
    return
  fi
  docker "$@"
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  docker_invoke "$@"
fi
