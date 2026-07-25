---
type: Directory Index
title: Control Plane Hub
description: High-level overview map for Shadow-Diff control plane specifications and operators.
resource: https://github.com/your-org/shadow-diff/tree/main/docs/control-plane
tags: [index, control-plane, monarch]
timestamp: 2026-07-12T14:20:00Z
---

# Control Plane Architecture

The control plane layer acts as the centralized automation hub of Shadow-Diff. Driven by the `Monarch` operator controller, its goal is to abstract infrastructure bootstrapping away from moving developer teams. It reads high-level testing resource declarations and builds out fully-isolated runtime environments without manual operator configuration.

## Document Map
* [/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md](/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md) - One-time Monarch + Pixie + pixie-gate install; create and delete ShadowTests without resetting Pixie.
* [/control-plane/pixie-gate.md](/control-plane/pixie-gate.md) - Least-privilege PixieStreamRule → PxL gateway (in-cluster Deployment).
* [/infrastructure/bats-testing-framework.md](/infrastructure/bats-testing-framework.md) - Bats-core harness: shared ShadowTest per file, settlement-based Beru assertions.
* [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md) - Envoy-only shadow pod injection, egress capture, and CRD reconcile contract (telemetry-dependent architecture).
* [/control-plane/monarch-security-model.md](/control-plane/monarch-security-model.md) - Deep dive specification regarding role compartmentalization, unprivileged eBPF decoupling boundary rules, and shadow sandboxing network policies.