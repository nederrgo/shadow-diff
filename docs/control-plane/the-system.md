---
type: Architecture Specification
title: The System — Shadow-Diff Dashboard UI
description: React SPA for live ShadowTest topology monitoring (via Tusk WebSockets) and interactive ShadowTest YAML authoring.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/the-system
tags: [architecture, control-plane, the-system, ui, react, topology, websocket]
timestamp: 2026-08-02T05:40:00Z
---

# The System — Dashboard UI

The System is the cluster-wide web dashboard for Shadow-Diff. It is a static React (Vite + TypeScript + Tailwind) SPA served by Nginx on port `80`, deployed into `monarch-system` from [`pipeline/the-system/deploy/`](https://github.com/shadow-diff/monarch/tree/main/pipeline/the-system/deploy). Like Tusk and Kaisel, it is **not** provisioned by the Monarch reconciler.

## Pages

| Route | Purpose |
|-------|---------|
| `/` (Monitor) | Live OpenShift-style topology graph for a ShadowTest |
| `/editor` | Interactive form → `engine.shadow-diff.io/v1alpha1` ShadowTest YAML |

## Topology stream

The Monitor page connects to Tusk:

```
ws://${window.location.hostname}:8082/ws/monitor?test=<name>&namespace=<ns>
```

The Monitor opens one unfiltered WebSocket (`/ws/monitor`) and builds a local catalog of every live ShadowTest. A searchable picker lists them (filter by substring on namespace/name); choosing an entry sets `?namespace=&test=` and shows that graph without reconnecting. `useTopologyStream` reconnects with exponential backoff (1s → 30s) and exposes `connectionStatus` (`connected` | `reconnecting` | `disconnected`).

Delete UX: `phase: "Deleting"` keeps the last topology and shows a red System-style `TeardownBanner` as a flex strip **above** the React Flow canvas (not an overlay — React Flow paints over absolute siblings). `phase: "Deleted"` drops the test from the picker catalog and shows the deleted banner for the current selection.

Tusk graph contract:

```
TopologyGraph { testName, namespace, phase, bootStep, mode, message?, nodes[], edges[] }
Node          { id, type, label, status }
Edge          { id, source, target, animated }
```

React Flow custom nodes map from Tusk semantic `type` strings:

| Tusk `type` | UI component |
|-------------|--------------|
| `target` | TargetAppNode |
| `capture` | KaiselNode |
| `ingress` | IgrisNode |
| `egress` | ShopNode |
| `sink` | BeruNode |
| `role` | ShadowRoleNode |

Node positions are a fixed client-side layout (Tusk does not send coordinates). Status styling: Ready (green), Provisioning (yellow pulse), Failed (red), Disabled (opacity 0.4), Degraded (amber). Edges use React Flow `animated` when Tusk sets `animated: true` (both endpoints Ready).

The node details drawer shows id / type / label / status. Progress and error text come from graph-level `TopologyGraph.message` (Tusk has no per-node message field).

## ShadowTest editor

Form fields map to CRD paths (`spec.storage.bucketName`, etc.), including required `spec.newImage`. Dependencies use collapsible Add menus; **input** is a single driver chooser (default / `http_request` / `rabbitmq_message`) with fields that swap underneath — at most one `spec.inputs` entry.

Menu options (dependency kinds and input drivers) come from the shared Go catalog [`pipeline/pkg/shadowspec`](https://github.com/shadow-diff/monarch/tree/main/pipeline/pkg/shadowspec). Monarch uses the same package for image/port defaults. Regenerate the UI module with:

```bash
make shadowspec-export   # → pipeline/the-system/src/lib/shadowCatalog.ts
```

Preview is generated with `js-yaml`; Copy / Download only — apply with kubectl outside the UI.

## Deploy

| | |
|---|---|
| Image | `the-system:dev` (E2E) / `the-system:latest` |
| Namespace | `monarch-system` |
| Container | Nginx unprivileged on `:8080` (PSA restricted / non-root) |
| Service | `the-system:80` → pod `:8080` |
| Health | `GET /healthz` → `ok` |

```bash
make the-system-docker-build THE_SYSTEM_IMG=the-system:dev
kubectl apply -k pipeline/the-system/deploy
kubectl set image deployment/the-system -n monarch-system the-system=the-system:dev
```

Local Vite dev server: `npm run dev` on `:3000` (Tusk allows localhost Origins on any port).

# Citations

- [pipeline/the-system/](https://github.com/shadow-diff/monarch/tree/main/pipeline/the-system) — SPA source
- [/control-plane/tusk-bff.md](/control-plane/tusk-bff.md) — WebSocket BFF and graph model
- [/control-plane/monarch-status-stream.md](/control-plane/monarch-status-stream.md) — upstream gRPC status contract
