---
type: Architecture Specification
title: pixie-gate PixieStreamRule Gateway
description: Least-privilege in-cluster Go service that renders PixieStreamRule CRs into PxL and runs px.export to Siphon, Recorder, and beru-local.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/pixie-gate
tags: [architecture, control-plane, security, pixie, ebpf, pixiestreamrule]
timestamp: 2026-07-24T18:00:00Z
---

# pixie-gate

`pixie-gate` is the Pixie capture gateway for Shadow-Diff. Monarch writes unprivileged `PixieStreamRule` CRs; pixie-gate polls those CRs, renders ingress/egress/mongo PxL templates, and runs `px run` so Vizier PEM exports OTLP to Siphon, Recorder, or beru-local.

It is **not** part of the Monarch operator. Kernel/eBPF privilege stays inside Pixie Vizier (`pl`); pixie-gate only needs narrow CRD RBAC plus a Pixie Cloud API key.

## Deployment

| Item | Value |
|------|-------|
| Namespace | `monarch-system` |
| Manifests | [`pipeline/pixie-gate/deploy/`](../../pipeline/pixie-gate/deploy/) |
| Image | `pixie-gate:dev` (E2E) / pinned registry tag (prod) |
| Secret | `monarch-system/pixie-gate` key `PIXIE_API_KEY` |
| ConfigMap | `monarch-system/pixie-gate` (PxL templates) |

Bootstrap:

```bash
export PIXIE_API_KEY=...
make pixie-gate-docker-build PIXIE_GATE_IMG=pixie-gate:dev
./testing/bats/setup/setup-local-pixie.sh   # Vizier + pixie-gate
# or after Vizier is up:
./testing/bats/setup/start-pixie-stream-bridge.sh
```

## Security posture

* **RBAC**: `get/list/watch` on `pixiestreamrules`; `get/patch/update` on `pixiestreamrules/status` only.
* **Pod**: `runAsNonRoot` (65532), `readOnlyRootFilesystem`, drop ALL capabilities, `RuntimeDefault` seccomp, no hostNetwork/privileged.
* **Writable mounts**: `emptyDir` for `/tmp` (rendered `.pxl`) and `$HOME` (px auth cache).
* **Secrets**: API key via Secret env; never baked into the image.
* **Blast radius**: single replica; `terminationGracePeriodSeconds: 35` covers one `px run` timeout (25s).

## Control loop

1. Every `PIXIE_EXPORT_INTERVAL_SEC` (default 3s), list all `PixieStreamRule` objects.
2. For each active rule, render and export any configured endpoints in parallel:
   * `otelEndpoint` → ingress PxL → Siphon
   * `recorderOtelEndpoint` → egress PxL → Recorder
   * `mongoOtelEndpoint` → mongo PxL → beru-local
3. Patch `status.phase` to `Active`, `Error`, or `Inactive`.

Templates live in `pipeline/pixie-gate/internal/pxl/templates/` (embedded) and the deploy ConfigMap (mounted at `/etc/pixie-gate`).

## Health

HTTP `:8081` — `/healthz` and `/readyz`.

## Citations

* [Monarch security model](/control-plane/monarch-security-model.md)
* [Platform bootstrap](/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md)
* [Pixie CLI auth](https://docs.px.dev/reference/admin/api-keys/)
