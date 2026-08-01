---
type: Directory Index
title: Control Plane Hub
description: High-level overview map for Shadow-Diff control plane specifications and operators.
resource: https://github.com/your-org/shadow-diff/tree/main/docs/control-plane
tags: [index, control-plane, monarch]
timestamp: 2026-07-30T17:20:00Z
---

# Control Plane Architecture

The control plane layer acts as the centralized automation hub of Shadow-Diff. Driven by the `Monarch` operator controller, its goal is to abstract infrastructure bootstrapping away from moving developer teams. It reads high-level testing resource declarations and builds out fully-isolated runtime environments without manual operator configuration.

## Document Map
* [/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md](/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md) - Platform bootstrap; record bottom-up (sinks → KaiselRule → AMQP bind) and replay lifecycles; beru-local teardown.
* [/control-plane/shadowtest-teardown-edge-cases.md](/control-plane/shadowtest-teardown-edge-cases.md) - ADR: queue delete fail-open + `x-expires` vs S3 finalizer retry; Failed autopsy vs delete.
* [/infrastructure/bats-testing-framework.md](/infrastructure/bats-testing-framework.md) - Bats-core harness: shared ShadowTest per file, settlement-based Beru assertions.
* [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md) - Record/replay reconcile, status/topology surface (`bootStep`, `components`, `conditions`), S3 env, auto replay trigger, S3 prefix retention finalizer, boot failure gates.
* [/control-plane/monarch-security-model.md](/control-plane/monarch-security-model.md) - Deep dive specification regarding role compartmentalization, unprivileged eBPF decoupling boundary rules, and shadow sandboxing network policies.