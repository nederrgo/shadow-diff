---
type: Directory Index
title: Control Plane Hub
description: High-level overview map for Shadow-Diff control plane specifications and operators.
resource: https://github.com/your-org/shadow-diff/tree/main/docs/control-plane
tags: [index, control-plane, monarch]
timestamp: 2026-06-27T19:35:00Z
---

# Control Plane Architecture

The control plane layer acts as the centralized automation hub of Shadow-Diff. Driven by the `Monarch` operator controller, its goal is to abstract infrastructure bootstrapping away from moving developer teams. It reads high-level testing resource declarations and builds out fully-isolated runtime environments without manual operator configuration.

## Document Map
* [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md) - Envoy-only shadow pod injection, egress capture, and CRD reconcile contract (telemetry-dependent architecture).
* [/control-plane/monarch-security-model.md](/control-plane/monarch-security-model.md) - Deep dive specification regarding role compartmentalization, unprivileged eBPF decoupling boundary rules, and shadow sandboxing network policies.