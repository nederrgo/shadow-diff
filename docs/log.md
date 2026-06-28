---
type: Workspace Log
title: Shadow-Diff Knowledge Base Change Log
description: Chronological audit trail of architectural changes, additions, and updates.
resource: https://github.com/your-org/shadow-diff/tree/main/docs
tags: [meta, changelog, history]
timestamp: 2026-06-27T19:40:00Z
---

# Shadow-Diff Documentation Log

## [2026-06-28]
### Added
* pipeline/monarch: wrapper.js resolves OTel SDK from operator node_modules path
* pipeline/monarch: Node.js wrapper uses NodeSDK MongoDBInstrumentation enhancedDatabaseReporting
* pipeline/monarch: ignore shell placeholder when resolving Node entrypoint
* pipeline/monarch: Node.js entrypoint wrapper for Mongo enhancedDatabaseReporting

## [2026-06-27]
### Added
* pipeline/monarch/internal/controller/shadowtest_otel.go: inject only detected language to avoid duplicate OTel volume mounts
* pipeline/monarch/internal/controller/shadowtest_otel_instrumentation.go: drop cross-namespace owner ref on Instrumentation CR (namespace GC)
* `pipeline/monarch`: Monarch reconciles OTel Instrumentation CR with prod annotation/env sanitization
* testing/scripts/lib/e2e-reset-deploy.sh: drop beru-system deploy; Monarch BERU_IMAGE + wait beru-local in shadow namespace
* `pipeline/monarch/internal/controller/shadowtest_beru_local.go`: Monarch provisions beru-local (Service+Deployment) per ShadowTest in shadow namespace; bounded readiness gate; OTel/Envoy/recorder/relay wired to local Beru; E2E scripts updated
* `docs/control-plane/monarch-security-model.md`: Initialized security specification tracking cluster RBAC boundaries, network sandboxing, and unprivileged out-of-band eBPF delegation constraints.
* `docs/control-plane/index.md`: Configured control-plane structural progressive disclosure directory index map.
* `.cursorrules`: Initialized OKF v0.1 workspace rules to enforce strict metadata schema alignment for AI agents.