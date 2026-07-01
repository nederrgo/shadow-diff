---
type: Workspace Log
title: Shadow-Diff Knowledge Base Change Log
description: Chronological audit trail of architectural changes, additions, and updates.
resource: https://github.com/your-org/shadow-diff/tree/main/docs
tags: [meta, changelog, history]
timestamp: 2026-06-27T19:40:00Z
---

# Shadow-Diff Documentation Log

## [2026-07-01]
### Added
* 'pipeline/beru/internal/dashboard': Split HTTP trace rows by ingress vs egress direction so Igris ingress diffs are visible separately from wire egress
* 'testing/scripts/e2e-http-mongo-test.sh': HTTP ingress + mongo write + RMQ egress E2E (igris-http, no OTel)
* 'pipeline/egress-relay-rabbitmq/internal/firehose/parse.go': enrich Beru egress payload with exchange + routing_key from Firehose
* 'testing/scripts/e2e-rmq-mongo-test.sh': assert RabbitMQ egress on beru-local (Monarch default egress-relay target)
* 'testing/scripts/e2e-rmq-mongo-test.sh': minikube/Kind auto-detect via e2e_load_image; deploy beru-system if missing
* 'pipeline/monarch/internal/controller/shadowtest_envoy.go': remove invalid HCM max_request_bytes (Envoy v1.26); Lua 64KB truncation remains
* 'testing/scripts/e2e-rmq-mongo-test.sh': Kind E2E for RMQ ingress + mongo write + RMQ egress with W3C traceparent
* 'pipeline/igrises': Phase 3 Igris traceparent multicast — ResolveContext, literal preserve, integration tests
* 'pipeline/beru, pipeline/monarch': Phase 2 wire ingest — POST /api/v1/ingest/wire, NetworkEventEnvelope, Lua httpCall to beru_ingest, OTLP Mongo deprecated
* 'pipeline/monarch': Plan 1 telemetry-dependent realignment — remove OTel/language injection, always-on Envoy egress with Lua/mongo_proxy, beru_ingest cluster forward ref

## [2026-06-30]
### Added
* testing/nodejs-hybrid-worker: add undici dep, lazy-load for HTTP_PROXY only
* testing/e2e-nodejs-hybrid-test.sh: minikube-only (drop kind)
* testing: Node.js hybrid E2E (RMQ ingress + mongo + HTTP replay + RMQ egress)

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