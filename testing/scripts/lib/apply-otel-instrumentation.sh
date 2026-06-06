#!/usr/bin/env bash
# Create shadow namespace (if needed) and apply Instrumentation CR before shadow pods start.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"
SHADOW_NS="${1:?usage: apply-otel-instrumentation.sh <shadow-namespace>}"

kubectl create namespace "$SHADOW_NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n "$SHADOW_NS" -f "$REPO/testing/scripts/manifests/otel-instrumentation.yaml"
echo "Applied Instrumentation shadow-otel-instrumentation in namespace ${SHADOW_NS}"
