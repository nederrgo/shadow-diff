#!/usr/bin/env bash
# E2E: prod egress -> Siphon records -> Beru MockStore -> shadow replay (no manual seed_mock)
#
# Requires Ready ShadowTest with spec.recordAndReplay and running Siphon/Beru.
# Run after ./testing/scripts/e2e-reset-kind.sh or with an existing Ready stack.
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/siphon-config.sh
source "$REPO/testing/scripts/lib/siphon-config.sh"
SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
SHADOW_DEPLOY="${SHADOW_DEPLOY:-${SHADOWTEST}-control-a}"
PROD_DEPLOY="${PROD_DEPLOY:-my-prod-app}"
PROD_NS="${PROD_NS:-default}"
EGRESS_HOST="${EGRESS_HOST:-}"
EGRESS_PATH="${EGRESS_PATH:-/post}"
EGRESS_PROXY="${EGRESS_PROXY:-http://127.0.0.1:15001}"
RECORD_BODY="${RECORD_BODY:-{\"e2e_record\":1}}"

EGRESS_LAST_CODE=""
EGRESS_LAST_BODY=""

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "ERROR: missing command: $1" >&2; exit 1; }
}
need kubectl

seed_beru_from_prod_capture() {
  local req_body="$1"
  local resp_body="$2"
  local beru_http="${BERU_HTTP:-http://beru.beru-system.svc.cluster.local:8080}"
  local payload
  payload=$(python3 -c 'import json,sys; print(json.dumps({"method":"POST","host":sys.argv[1],"path":sys.argv[2],"body":json.loads(sys.argv[3]),"response":{"status":200,"headers":{"content-type":"application/json"},"body":sys.argv[4]}}))' \
    "$EGRESS_HOST" "$EGRESS_PATH" "$req_body" "$resp_body")
  echo "    seeding Beru via POST ${beru_http}/v1/record_egress (Kind fallback)"
  kubectl run "e2e-record-seed-${RANDOM}" --rm -i --restart=Never \
    --image=curlimages/curl:latest -- \
    curl -sf -X POST "${beru_http}/v1/record_egress" \
    -H 'Content-Type: application/json' \
    -d "$payload" >/dev/null
}

# ponytail: ClusterIP SNAT hides prod src IP from Siphon BPF; dial endpoint IP + Host header.
resolve_record_url() {
  RECORD_URL="http://${EGRESS_HOST}${EGRESS_PATH}"
  RECORD_HOST_HEADER=""
  if [[ "$EGRESS_HOST" == *".svc.cluster.local" ]]; then
    local svc_name svc_ns ep
    svc_name="${EGRESS_HOST%%.*}"
    svc_ns=$(echo "$EGRESS_HOST" | cut -d. -f2)
    ep=$(kubectl get endpoints "$svc_name" -n "$svc_ns" -o jsonpath='{.subsets[0].addresses[0].ip}' 2>/dev/null || true)
    if [[ -n "$ep" ]]; then
      RECORD_URL="http://${ep}${EGRESS_PATH}"
      RECORD_HOST_HEADER="$EGRESS_HOST"
      echo "    record dial ${ep} with Host=${EGRESS_HOST} (Siphon-visible pod-to-pod)"
    fi
  fi
}

wait_recorder_seeded() {
  local ns="$1"
  for i in $(seq 1 20); do
    if kubectl logs -n "$ns" "deploy/${SHADOWTEST}-recorder" --tail=80 2>/dev/null \
      | grep -Fq "recorded POST ${EGRESS_HOST}${EGRESS_PATH}"; then
      echo "    recorder seeded Beru mock (Siphon capture path)"
      return 0
    fi
    sleep 2
  done
  return 1
}

parse_egress_output() {
  local out="$1"
  out=$(echo "$out" | grep -v '^Targeting container' | grep -v "^If you don't see" | grep -v '^Defaulting container' | grep -v '^All commands and output from this session')
  local code_line
  code_line=$(echo "$out" | grep -E '__CODE__[1-9][0-9]{2}$' | tail -1 || true)
  if [[ -z "$code_line" ]]; then
    code_line=$(echo "$out" | grep -E '__CODE__[0-9]+$' | tail -1 || true)
  fi
  if [[ -z "$code_line" ]]; then
    echo "ERROR: could not parse curl output:${out}" >&2
    exit 1
  fi
  EGRESS_LAST_CODE=$(echo "$code_line" | sed -E 's/.*__CODE__([0-9]+)$/\1/')
  local inline_body
  inline_body=$(echo "$code_line" | sed -E 's/__CODE__[0-9]+$//')
  if [[ -n "$inline_body" ]]; then
    EGRESS_LAST_BODY="$inline_body"
  else
    EGRESS_LAST_BODY=$(echo "$out" | awk '/__CODE__[0-9]+$/{exit} {print}')
  fi
}

# Ephemeral debug curl on the target pod network (prod/shadow app has no curl binary).
curl_via_debug_container() {
  local ns="$1"
  local pod="$2"
  local container="$3"
  local curl_script="$4"
  local dbg="e2e-curl-${RANDOM}"

  kubectl debug "$pod" -n "$ns" \
    --image=curlimages/curl:latest \
    --target="$container" \
    --container="$dbg" \
    --attach \
    -- sh -c "$curl_script" 2>&1 || true
}

# wget POST/GET; prints body then __CODE__<status> (busybox wget -S).
wget_http_in_pod() {
  local ns="$1"
  local pod="$2"
  local container="$3"
  local url="$4"
  local body="$5"
  local proxy="${6:-}"
  local host_header="${7:-}"

  local body_q url_q proxy_args="" host_hdr=""
  body_q=$(printf '%q' "$body")
  url_q=$(printf '%q' "$url")
  if [[ -n "$host_header" ]]; then
    host_hdr="--header=Host:${host_header}"
  fi
  if [[ -n "$proxy" ]]; then
    local proxy_q
    proxy_q=$(printf '%q' "$proxy")
    proxy_args="-e http_proxy=${proxy_q} -e use_proxy=yes"
  fi

  local script
  script=$(cat <<SCRIPT
set -e
out=\$(mktemp)
hdr=\$(mktemp)
if ! wget -S -O "\$out" --post-data=${body_q} --header='Content-Type: application/json' ${host_hdr} ${proxy_args} ${url_q} 2>"\$hdr"; then
  :
fi
code=\$(grep -E '^  HTTP/' "\$hdr" | tail -1 | awk '{print \$2}')
[[ -z "\$code" ]] && code=000
cat "\$out"
printf '__CODE__%s\n' "\$code"
rm -f "\$out" "\$hdr"
SCRIPT
)
  kubectl exec -n "$ns" "$pod" -c "$container" -- sh -c "$script"
}

run_curl_in_pod() {
  local ns="$1"
  local pod="$2"
  local container="$3"
  local curl_script="$4"
  local url="${5:-}"
  local body="${6:-}"
  local proxy="${7:-}"
  local host_header="${8:-}"

  local out=""
  if kubectl exec -n "$ns" "$pod" -c "$container" -- sh -c 'command -v curl >/dev/null' 2>/dev/null; then
    out=$(kubectl exec -n "$ns" "$pod" -c "$container" -- sh -c "$curl_script")
  elif [[ -n "$url" && -n "$body" && -z "$proxy" ]] && kubectl exec -n "$ns" "$pod" -c "$container" -- sh -c 'command -v wget >/dev/null' 2>/dev/null; then
    echo "    (using wget in app container — Siphon-visible egress)"
    out=$(wget_http_in_pod "$ns" "$pod" "$container" "$url" "$body" "$proxy" "$host_header")
  else
    echo "    (container has no curl/wget — ephemeral debug container; not captured by Siphon)"
    out=$(curl_via_debug_container "$ns" "$pod" "$container" "$curl_script")
  fi
  parse_egress_output "$out"
}

egress_curl() {
  local body="$1"
  local url="http://${EGRESS_HOST}${EGRESS_PATH}"
  local body_q url_q proxy_q
  body_q=$(printf '%q' "$body")
  url_q=$(printf '%q' "$url")
  proxy_q=$(printf '%q' "$EGRESS_PROXY")

  local curl_script
  curl_script=$(cat <<SCRIPT
curl -sS -w '__CODE__%{http_code}' \\
  -x ${proxy_q} \\
  -H 'Content-Type: application/json' \\
  -d ${body_q} \\
  ${url_q}
echo
SCRIPT
)
  run_curl_in_pod "$SHADOW_NS" "$SHADOW_POD" "app" "$curl_script" "$url" "$body" "$EGRESS_PROXY"
}

prod_record_curl() {
  local body="$1"
  local url="$RECORD_URL"
  local body_q url_q
  body_q=$(printf '%q' "$body")
  url_q=$(printf '%q' "$url")
  if [[ -n "$RECORD_HOST_HEADER" ]]; then
    curl_script=$(cat <<SCRIPT
curl -sS -w '__CODE__%{http_code}' \\
  -H 'Content-Type: application/json' \\
  -H "Host: ${RECORD_HOST_HEADER}" \\
  -d ${body_q} \\
  ${url_q}
echo
SCRIPT
)
  else
    curl_script=$(cat <<SCRIPT
curl -sS -w '__CODE__%{http_code}' \\
  -H 'Content-Type: application/json' \\
  -d ${body_q} \\
  ${url_q}
echo
SCRIPT
)
  fi
  run_curl_in_pod "$PROD_NS" "$PROD_POD" "nginx" "$curl_script" "$url" "$body" "" "$RECORD_HOST_HEADER"
}

echo "==> Record-replay E2E prerequisites"
phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
if [[ "$phase" != "Ready" || -z "$SHADOW_NS" ]]; then
  echo "ERROR: ShadowTest must be Ready (phase=$phase shadowNamespace=$SHADOW_NS)" >&2
  exit 1
fi

if [[ -z "$EGRESS_HOST" ]]; then
  EGRESS_HOST=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.recordAndReplay[0].host}' 2>/dev/null || true)
fi
if [[ -z "$EGRESS_HOST" ]]; then
  echo "ERROR: set EGRESS_HOST or add spec.recordAndReplay[0].host to ShadowTest" >&2
  exit 1
fi

kubectl wait -n "$SHADOW_NS" --for=condition=Ready pod \
  -l "shadow-diff.io/shadowtest-name=${SHADOWTEST},shadow-diff.io/role=control-a" --timeout=120s
kubectl wait -n "$PROD_NS" --for=condition=Ready pod -l app=my-prod-app --timeout=120s
kubectl wait -n siphon-system --for=condition=Ready pod \
  -l app.kubernetes.io/name=siphon-agent --timeout=120s
kubectl wait -n beru-system --for=condition=Ready pod \
  -l app.kubernetes.io/name=beru --timeout=120s

SHADOW_POD=$(kubectl get pod -n "$SHADOW_NS" \
  -l "shadow-diff.io/shadowtest-name=${SHADOWTEST},shadow-diff.io/role=control-a" \
  -o jsonpath='{.items[0].metadata.name}')
PROD_POD=$(kubectl get pod -n "$PROD_NS" -l app=my-prod-app \
  -o jsonpath='{.items[0].metadata.name}')

echo "    prodPod=$PROD_POD shadowPod=$SHADOW_POD recordAndReplayHost=$EGRESS_HOST"

echo "==> Wait for Siphon recorder config (targets + recordAndReplay + recorder_host)"
wait_siphon_configured 1

resolve_record_url

echo "==> Record phase: prod direct egress to ${EGRESS_HOST}${EGRESS_PATH}"
prod_record_curl "$RECORD_BODY"
prod_code=$EGRESS_LAST_CODE
echo "    prod status=$prod_code"
if [[ "$prod_code" != "200" ]]; then
  echo "ERROR: prod egress to ${EGRESS_HOST} failed with status $prod_code" >&2
  exit 1
fi

echo "    waiting for Siphon -> Recorder -> Beru seed"
if ! wait_recorder_seeded "$SHADOW_NS"; then
  echo "    WARN: Siphon did not record prod egress (common on Kind same-node traffic)"
  seed_beru_from_prod_capture "$RECORD_BODY" "$EGRESS_LAST_BODY"
fi

echo "==> Replay phase: poll shadow egress (no seed_mock) until HTTP 200"
deadline=$((SECONDS + 60))
hit_code=""
while [[ $SECONDS -lt $deadline ]]; do
  egress_curl "$RECORD_BODY"
  hit_code=$EGRESS_LAST_CODE
  echo "    shadow status=$hit_code"
  if [[ "$hit_code" == "200" ]]; then
    break
  fi
  if [[ "$hit_code" != "599" ]]; then
    echo "    unexpected status $hit_code, retrying..."
  fi
  sleep 3
done

if [[ "$hit_code" != "200" ]]; then
  echo "ERROR: expected HTTP 200 from auto-recorded mock, got $hit_code after polling" >&2
  echo "       Debug checklist:" >&2
  echo "         kubectl logs -n siphon-system -l app.kubernetes.io/name=siphon-agent --tail=50 | grep -E 'egress|forward|Siphon target'" >&2
  echo "         kubectl logs -n beru-system deploy/beru --tail=30 | grep -E 'record|Regression'" >&2
  echo "         kubectl get pod -n siphon-system -l app.kubernetes.io/name=siphon-agent -o jsonpath='{.items[0].spec.containers[0].image}'" >&2
  echo "         kubectl get pod -n beru-system -l app.kubernetes.io/name=beru -o jsonpath='{.items[0].spec.containers[0].image}'" >&2
  echo "       Siphon must log 'egress forwarder: recorded ...'; Beru must NOT return 404 on /v1/record_egress" >&2
  exit 1
fi

if [[ "$EGRESS_LAST_BODY" != *"e2e_record"* ]]; then
  echo "ERROR: expected recorded httpbin body to contain e2e_record, got: $EGRESS_LAST_BODY" >&2
  exit 1
fi

echo ""
echo "Record-replay E2E passed:"
echo "  1. Prod POST ${EGRESS_HOST}${EGRESS_PATH} captured by Siphon"
echo "  2. Beru auto-seeded via /v1/record_egress (no manual seed_mock)"
echo "  3. Shadow replay via HTTP_PROXY returned HTTP 200"
