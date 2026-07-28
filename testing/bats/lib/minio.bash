# Local MinIO fixture helpers for record/replay bats (BYOB stand-in).
# Manifests: testing/bats/manifests/minio/
# shellcheck shell=bash

MINIO_NS="${MINIO_NS:-monarch-system}"
MINIO_BUCKET="${MINIO_BUCKET:-shadow-diff-local}"
MINIO_ENDPOINT="${MINIO_ENDPOINT:-http://minio-service.monarch-system.svc.cluster.local:9000}"
MINIO_MANIFEST_DIR="${MINIO_MANIFEST_DIR:-${REPO}/testing/bats/manifests/minio}"

# Idempotent: Deployment + Service + credentials Secret + create-bucket Job.
minio_ensure() {
  local dir="${MINIO_MANIFEST_DIR}"
  echo "==> [minio] ensure fixture in ${MINIO_NS} (bucket ${MINIO_BUCKET})"
  kubectl apply -f "${dir}/deployment.yaml"
  kubectl apply -f "${dir}/service.yaml"
  kubectl apply -f "${dir}/credentials-secret.yaml"
  kubectl rollout status deployment/minio -n "${MINIO_NS}" --timeout=180s

  kubectl delete job minio-create-bucket -n "${MINIO_NS}" --ignore-not-found --wait=true
  kubectl apply -f "${dir}/bucket-job.yaml"
  kubectl wait --for=condition=complete "job/minio-create-bucket" \
    -n "${MINIO_NS}" --timeout=120s
}

# Session object prefix matching pipeline/pkg/s3utils Config.KeyPrefix.
# Usage: minio_session_prefix <cr_ns> <test_name> <session_id> <ingress|egress>
minio_session_prefix() {
  local ns="$1" name="$2" session="$3" kind="$4"
  echo "shadow-diff/${ns}/${name}/sessions/${session}/${kind}/"
}

# List object keys under bucket/prefix via a one-shot mc pod in default.
# Prints mc ls lines to stdout. Returns 0 even when empty (no objects).
# Usage: minio_ls_prefix <key_prefix>
minio_ls_prefix() {
  local prefix="$1"
  local pod="minio-ls-${RANDOM}"
  # Strip leading slash; mc path is local/<bucket>/<key-prefix>
  prefix="${prefix#/}"

  kubectl run "$pod" --rm -i --restart=Never -n default \
    --image=minio/mc:latest \
    --env=HOME=/tmp \
    --env=MC_CONFIG_DIR=/tmp/.mc \
    --command -- /bin/sh -c "
      set -e
      mc alias set local '${MINIO_ENDPOINT}' admin password >/dev/null
      mc ls --recursive \"local/${MINIO_BUCKET}/${prefix}\" 2>/dev/null || true
    "
}

# Poll until mc lists at least one object under prefix.
# Usage: minio_wait_objects <key_prefix> [timeout_seconds]
minio_wait_objects() {
  local prefix="$1" timeout="${2:-45}"
  local elapsed=0 out

  echo "==> [minio] wait for objects under ${prefix} (timeout=${timeout}s)"
  while true; do
    out="$(minio_ls_prefix "$prefix" 2>/dev/null || true)"
    # mc ls lines look like: [DATE] [SIZE] [NAME] — require a non-empty non-error line
    if echo "$out" | grep -qE '[[:alnum:]_./-]+\.(jsonl|json)|STANDARD|[[:digit:]]+[KkMmGg]?i?B'; then
      echo "    found:"
      echo "$out" | sed 's/^/      /'
      return 0
    fi
    # Fallback: any non-empty output that is not an error banner
    if [[ -n "$(echo "$out" | grep -v '^$' | grep -viE 'error|unable|fail' || true)" ]]; then
      echo "    found:"
      echo "$out" | sed 's/^/      /'
      return 0
    fi

    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: no MinIO objects under ${prefix} after ${timeout}s" >&2
      echo "    last mc ls:" >&2
      echo "${out:-<empty>}" | sed 's/^/      /' >&2
      return 1
    fi

    echo "    waiting objects (${elapsed}s/${timeout}s)..."
    sleep 3
    elapsed=$((elapsed + 3))
  done
}
