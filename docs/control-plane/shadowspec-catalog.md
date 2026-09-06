---
type: Architecture Specification
title: Shadowspec — Shared Dependency and Input Catalog
description: Static vocabulary of ShadowTest dependency kinds and ingress drivers shared by Monarch defaults, Tusk topology, and The System editor menus.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/pkg/shadowspec
tags: [architecture, control-plane, shadowspec, dependencies, inputs, the-system, monarch]
timestamp: 2026-08-03T11:50:00Z
---

# Shadowspec — Shared Catalog

`pipeline/pkg/shadowspec` (`github.com/shadow-diff/shadowspec`) is the single static catalog of:

| Surface | Members |
|---------|---------|
| Dependency kinds | `rabbitmq`, `mongodb` (+ `mongo`), `redis`, `postgres` (+ `postgresql`) — default image, port, suggested env var |
| Input drivers | `http_request`, `rabbitmq_message` — which fields each needs |

Driver name constants (`DriverHTTPRequest`, `DriverRabbitMQMessage`) and `ContainsDriver` live in the same package so Monarch status, the gRPC topology feed, and Tusk share one vocabulary. Monarch's `resolveDependencyDefaults` calls this package. The System editor Add menus read a generated TypeScript copy (`shadowCatalog.ts`) produced by `make shadowspec-export`. There is no runtime schema RPC — updating the catalog is a code change plus export.

# Citations

- [pipeline/pkg/shadowspec/catalog.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/pkg/shadowspec/catalog.go) — catalog tables
- [pipeline/pkg/shadowspec/drivers.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/pkg/shadowspec/drivers.go) — driver constants + `ContainsDriver`
- [/control-plane/the-system.md](/control-plane/the-system.md) — editor UX
- [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md) — reconcile that consumes dependency defaults
- [/control-plane/tusk-bff.md](/control-plane/tusk-bff.md) — topology nodes gated by ingress drivers
