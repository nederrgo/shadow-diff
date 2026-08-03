---
type: Operations Guide
title: Helm Charts for Platform Install
description: Install Shadow-Diff control plane (Monarch, Tusk, the-system) and Kaisel via Helm charts under deploy/charts/.
resource: https://github.com/shadow-diff/monarch/tree/main/deploy/charts
tags: [operations, infrastructure, helm, monarch, tusk, the-system, kaisel, deployment]
timestamp: 2026-08-02T12:40:00Z
---

# Helm Charts for Platform Install

Shadow-Diff ships two Helm charts under [`deploy/charts/`](https://github.com/shadow-diff/monarch/tree/main/deploy/charts). They package the same cluster singletons that Kustomize installs today; they do **not** replace per-ShadowTest workloads (beru-local, Shop, Igris, role pods), which Monarch still reconciles into `shadow-<ns>-<name>`.

| Chart | Installs | Typical namespace |
|-------|----------|-------------------|
| `shadow-diff` | CRDs (ShadowTest, KaiselRule), Monarch operator, Tusk BFF, the-system UI, optional S3/Postgres Secrets, Ingress | `monarch-system` |
| `shadow-agent` | Kaisel DaemonSet + RBAC | `kaisel-system` |

There is no cluster-wide Beru Deployment. Set `BERU_DB_SECRET` (via chart values) so each beru-local mounts shared PostgreSQL credentials. See [/data-plane/beru-postgres-storage.md](/data-plane/beru-postgres-storage.md).

## Install order

```bash
# 1. Control plane + UI (creates CRDs from chart/crds/)
helm upgrade --install shadow-diff deploy/charts/shadow-diff \
  --namespace monarch-system --create-namespace \
  --set postgres.createSecret=true \
  --set postgres.host=<pg-host> \
  --set postgres.user=<user> \
  --set postgres.password=<pass> \
  --set postgres.name=beru \
  --set aws.accessKeyId=<key> \
  --set aws.secretAccessKey=<secret> \
  --set aws.s3Bucket=<bucket>

# 2. Capture agent (needs KaiselRule CRD from step 1)
helm upgrade --install shadow-agent deploy/charts/shadow-agent \
  --namespace kaisel-system --create-namespace
```

Kustomize paths remain valid for local E2E:

```bash
make -C pipeline/monarch deploy IMG=monarch:dev
kubectl apply -k pipeline/kaisel/deploy/
kubectl apply -k pipeline/tusk/deploy/
kubectl apply -k pipeline/the-system/deploy/
```

## `shadow-diff` values (high signal)

| Path | Role |
|------|------|
| `global.imageRegistry` | Default `ghcr.io/shadow-diff` |
| `global.imageTag` | Default `latest` |
| `monarch.helperImages.*` | Full image refs injected as `IGRIS_HTTP_IMAGE`, `SHOP_IMAGE`, `BERU_IMAGE`, `ENVOY_IMAGE`, … |
| `monarch.beruDbSecret` | `namespace/secret` for Postgres; default `<release-ns>/<postgres secret>` |
| `postgres.createSecret` / `existingSecret` | Secret must expose `DB_HOST`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `DB_PORT`, `DB_SSLMODE` |
| `aws.createSecret` / `existingSecret` | BYOB S3 credentials (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`) for ShadowTest `credentialsSecretRef` |
| `tusk.monarchGrpcAddr` | Default points at chart Service `*-monarch-status-grpc:9090` |
| `ingress.enabled` / `className` | `nginx` or `alb` (ALB annotations applied when `className: alb`) |

the-system nginx (ConfigMap-mounted at `/etc/nginx/conf.d/default.conf`) serves the SPA and proxies `/api/` and `/ws/` to Tusk so the browser stays same-origin.

## `shadow-agent` values

| Path | Role |
|------|------|
| `kaisel.iface` | AF_PACKET interface (`any` by default) |
| `kaisel.logBodies` | `"false"` by default (production payloads in logs) |
| `kaisel.tolerations` | Tolerate all `NoSchedule` / `NoExecute` by default |

DaemonSet security matches the Kustomize bundle: `hostNetwork`, capabilities `BPF` / `NET_RAW` / `PERFMON` (not `privileged: true`).

## Citations

* [/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md](/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md) — bootstrap phases and ShadowTest lifecycle
* [/data-plane/kaisel-ebpf.md](/data-plane/kaisel-ebpf.md) — Kaisel capture model
* [pipeline/monarch/DEPLOYMENT.md](https://github.com/shadow-diff/monarch/blob/main/pipeline/monarch/DEPLOYMENT.md) — helper image env vars and `MONARCH_MODE`
