#!/usr/bin/env bash
# Ingress E2E: prod -> Pixie eBPF -> Siphon OTLP -> Igris -> Beru
#
# The legacy NetObserv / DaemonSet /v1/config path was removed.
# Use the Pixie OTLP ingress test instead.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"

echo "ERROR: e2e-pipeline-test.sh targeted the removed NetObserv PCAP capture stack." >&2
echo "" >&2
echo "Use the Pixie OTLP ingress path:" >&2
echo "  minikube delete -p minikube   # if switching from driver=none" >&2
echo "  MINIKUBE_DRIVER=kvm2 ./testing/scripts/setup-local-pixie.sh" >&2
echo "  MINIKUBE_DRIVER=kvm2 ./testing/scripts/e2e-reset-minikube.sh --setup-pixie --run-otlp-ingress-test" >&2
echo "" >&2
echo "Or run only the ingress assertion:" >&2
echo "  ./testing/scripts/e2e-siphon-otlp-ingress-test.sh" >&2
exit 1
