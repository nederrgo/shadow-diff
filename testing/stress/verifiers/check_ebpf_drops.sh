#!/usr/bin/env bash
# Assert Kaisel eBPF capture had zero loss during the stress window.
#
# Modes:
#   check_ebpf_drops.sh --snapshot <file>   Write current totals to JSON.
#   check_ebpf_drops.sh --baseline <file>   Diff current totals vs baseline; fail if any delta > 0.
#
# Counters (from Kaisel structured logs across all DaemonSet pods):
#   frag_drops   — "dropped fragmented IP datagrams" total=
#   sample_drops — "kernel trace gate dropped packets" total=  (must be 0 at 100% sample)
#   ring_lost    — sum of "kernel dropped records" lost=
#   truncated    — count of "packet truncated" lines
set -euo pipefail

KAISEL_NS="${KAISEL_NS:-kaisel-system}"

usage() {
  echo "Usage: $0 --snapshot <file> | --baseline <file>" >&2
  exit 2
}

mode=""
file=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --snapshot) mode=snapshot; file="${2:-}"; shift 2 ;;
    --baseline) mode=baseline; file="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "unknown arg: $1" >&2; usage ;;
  esac
done
[[ -n "$mode" && -n "$file" ]] || usage

collect_totals() {
  local logs frag sample lost trunc
  logs=$(kubectl logs -l app=kaisel -n "$KAISEL_NS" --tail=5000 2>/dev/null || true)

  # slog text: key=value; take the highest total= seen (monotonic counters).
  frag=$(echo "$logs" | grep -F 'dropped fragmented IP datagrams' \
    | grep -oE 'total=[0-9]+' | cut -d= -f2 | sort -n | tail -1)
  sample=$(echo "$logs" | grep -F 'kernel trace gate dropped packets' \
    | grep -oE 'total=[0-9]+' | cut -d= -f2 | sort -n | tail -1)
  # Ring loss is per-event (not a running total); sum all lost= values in the window.
  lost=$(echo "$logs" | grep -F 'kernel dropped records' \
    | grep -oE 'lost=[0-9]+' | cut -d= -f2 \
    | awk '{s+=$1} END{print s+0}')
  trunc=$(echo "$logs" | grep -cF 'packet truncated' || true)

  frag=${frag:-0}
  sample=${sample:-0}
  lost=${lost:-0}
  trunc=${trunc:-0}

  # Optional bpftool path (maps are unpinned; only works if bpftool + CAP_SYS_ADMIN in pod).
  local bpf_frag=0 bpf_sample=0
  local pod
  pod=$(kubectl get pods -n "$KAISEL_NS" -l app=kaisel \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  if [[ -n "$pod" ]] && kubectl exec -n "$KAISEL_NS" "$pod" -- \
      sh -c 'command -v bpftool >/dev/null' 2>/dev/null; then
    bpf_frag=$(bpftool_map_sum "$pod" frag_drops || echo 0)
    bpf_sample=$(bpftool_map_sum "$pod" sample_drops || echo 0)
  fi

  printf '{"frag_drops":%s,"sample_drops":%s,"ring_lost":%s,"truncated":%s,"bpf_frag_drops":%s,"bpf_sample_drops":%s}\n' \
    "$frag" "$sample" "$lost" "$trunc" "$bpf_frag" "$bpf_sample"
}

bpftool_map_sum() {
  local pod="$1" name="$2"
  local id dump
  id=$(kubectl exec -n "$KAISEL_NS" "$pod" -- \
    bpftool map list 2>/dev/null | awk -v n="$name" '$0 ~ n {print $1; exit}' | tr -d :)
  [[ -n "$id" ]] || return 1
  dump=$(kubectl exec -n "$KAISEL_NS" "$pod" -- bpftool map dump id "$id" -j 2>/dev/null || true)
  # Per-CPU array values are hex or decimal strings; best-effort sum.
  echo "$dump" | python3 -c '
import json,sys
try:
  data=json.load(sys.stdin)
except Exception:
  print(0); sys.exit(0)
total=0
for e in data if isinstance(data,list) else []:
  v=e.get("value",0)
  if isinstance(v,str):
    v=int(v,0)
  elif isinstance(v,list):
    v=sum(int(x,0) if isinstance(x,str) else int(x) for x in v)
  else:
    v=int(v)
  total+=v
print(total)
' 2>/dev/null || echo 0
}

current=$(collect_totals)
echo "==> [ebpf] current: $current"

if [[ "$mode" == "snapshot" ]]; then
  echo "$current" >"$file"
  echo "==> [ebpf] wrote snapshot $file"
  exit 0
fi

if [[ ! -f "$file" ]]; then
  echo "FAIL: baseline file missing: $file" >&2
  exit 1
fi

python3 - "$file" "$current" <<'PY'
import json, sys

base = json.load(open(sys.argv[1]))
cur = json.loads(sys.argv[2])
keys = ("frag_drops", "sample_drops", "ring_lost", "truncated", "bpf_frag_drops", "bpf_sample_drops")
failed = False
print(f"{'metric':<20} {'baseline':>10} {'current':>10} {'delta':>10} {'result':>6}")
for k in keys:
    b = int(base.get(k, 0))
    c = int(cur.get(k, 0))
    d = c - b
    # Ring lost / truncated are window sums from log tail — treat absolute current as delta
    # when baseline was also a log-tail snapshot (same log window growth).
    ok = d <= 0
    if k in ("ring_lost", "truncated") and b == 0:
        ok = c == 0
        d = c
    status = "PASS" if ok else "FAIL"
    if not ok:
        failed = True
    print(f"{k:<20} {b:>10} {c:>10} {d:>10} {status:>6}")
sys.exit(1 if failed else 0)
PY
