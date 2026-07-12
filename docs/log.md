---
type: Workspace Log
title: Shadow-Diff Knowledge Base Change Log
description: Chronological audit trail of architectural changes, additions, and updates.
resource: https://github.com/your-org/shadow-diff/tree/main/docs
tags: [meta, changelog, history]
timestamp: 2026-06-27T19:40:00Z
---

# Shadow-Diff Documentation Log

## [2026-07-12]
### Added
* 'testing/example-apps/http-rmq-python-worker': Open pika AMQP connection per /publish so idle Flask + long bats setup cannot starve BlockingConnection heartbeats
* 'pipeline/monarch siphon + bats http-ingress': Monarch deploys Siphon Service+Deployment for HTTP ingress; remove bats-side Siphon apply
* 'docs/verification/http-ingress-e2e-flow.md': move http-ingress README into OKF verification wiki (same format as hybrid flow)
* 'docs/verification/hybrid-rmq-e2e-flow.md': document Node/Python hybrid bats setup, per-order runtime flow, and per-@test assertions
* 'testing/bats/manifests/pixie-bridge, pipeline/shop, docs/data-plane': dual-branch Pixie egress (client+server) + Shop Put first-2xx dedup'
* 'docs/, pipeline/*/README.md, DEPLOYMENT.md': remove stale recordAndReplay; document always-on Shop+Recorder and Pixie server-side egress caveat
* 'shadowtest_siphon.go', 'siphon-deployment.yaml', 'siphon-config.sh': enable Service/siphon when inputs.port matches servicePort; Pixie targetPorts use applicationPort; E2E siphon manifest includes Service

## [2026-07-11]
### Added
* 'testing/bats/lib/http_otel_rmq.bash': deploy Siphon OTLP pod in bats_http_otel_rollout_stack — Monarch only creates the Service, not the Deployment; Pixie HTTP ingress path was silently dropping all spans (no endpoints)
* 'testing/bats/e2e/http-ingress/': increased beru_wait_log timeout from 45s to 120s for Pixie ingress latency; added README.md documenting E2E flow, manifest layout, and timing notes
* 'testing/bats/e2e/http-ingress/': All 3 http-ingress bats tests upgraded to strict E2E — traffic via prod Service + Pixie eBPF capture; exit 1 if Pixie unavailable; added publish_prod_http helper, prod-rabbitmq.yaml, prod-mongodb.yaml, and real worker prod-target manifests with AMQP_URL/MONGO_URL + ClusterIP Services
* 'CLAUDE.md, docs/verification/VERIFICATION.md': Corrected mock store attribution from Beru to Shop; Shop+Recorder now always-on (no spec.recordAndReplay field)

## [2026-07-10]
### Added
* 'testing/bats/e2e/http-ingress/http_ingress_rmq_go.bats': Added Go HTTP-ingress e2e test with fixture, manifest, and http-rmq-go-worker app; wired into env.bash and platform.bash build/load
* 'testing/bats/e2e/': Reorganised e2e tests into http-ingress/ and rabbitmq-ingress/ subfolders; updated load paths, run.sh glob, and Makefile/run-one.sh examples
* 'pipeline/monarch,python-test-worker': drop HTTP_PROXY injection; python worker uses iptables egress like nodejs
* 'pipeline/monarch/internal/controller/shadowtest_helpers.go': restore HTTP_PROXY on all shadow apps now that Shop+Recorder are always-on
* 'testing/bats/lib/traffic.bash, python_hybrid.bats': fix wait_recorder_seed stale-log false positives; add recorder warmup and http replay asserts
* 'testing/bats': revert recordAndReplay fixtures; scope egress PxL by targetLabels; isolate competing hybrid prod workers on shared RMQ queue
* 'testing/bats/fixtures/e2e/rabbit-ingress*/shadowtest.yaml, traffic.bash': add missing recordAndReplay hosts to hybrid fixtures; reduce wait_recorder_seed px nudge frequency
* 'testing/bats/helpers/pixie-bridge.sh, platform.bash': fix post-migration bridge pid detection and prevent platform flock leak into pixie-stream-bridge daemon
* 'docs/, pipeline/, .claude/': updated all stale testing/scripts/ path references to testing/bats/ or testing/tools/ across READMEs, CLAUDE.md, VERIFICATION.md, Makefiles, and settings
* 'testing/': migrated helpers, manifests, and setup scripts from testing/scripts/ into testing/bats/; deleted superseded standalone E2E runners and orphaned helpers; moved dev utilities to testing/tools/
* 'pipeline/recorder + pipeline/monarch/internal/controller': Remove recordAndReplay.json host-filter dead code — deleted HostMatches, RecordAndReplayHost type, loadRecordAndReplay, ConfigMap/volume/env-var provisioning; recorder now unconditionally forwards all OTLP spans to Shop
* 'docs/data-plane/egress-record-replay.md': Added egress record-and-replay architecture spec covering Recorder→Shop→Envoy ext_proc pipeline, mock key format, host normalisation, and always-on provisioning model
* 'pipeline/shop', 'testing/bats': Fix hybrid Recorder seed — normalize host in Shop seed key to strip port (HostWithoutPort), correct HTTP_RECORD_HOST in hybrid bats files, add ext_proc warmup probe to eliminate beru-local cold-start flake on test 1
* 'pipeline/monarch/, pipeline/recorder/': always-on Shop+Recorder; remove recordAndReplay from ShadowTest spec and PixieStreamRule; egress PxL scoped by TARGET_NAMESPACE; HostMatches empty=capture-all

## [2026-07-09]
### Added
* 'pipeline/monarch/internal/controller/shadowtest_rabbitmq.go': skip prod broker dial on deletion when broker is unreachable to unblock ShadowTest finalizer

## [2026-07-06]
### Added
* 'testing/bats/lib/shadowtest.bash': Add --require-rmq-egress for HTTP-igris suites without AMQP ingress queue
* 'testing/bats/e2e': Port nodejs_hybrid and http_otel_rmq Python/Node.js suites to Bats framework
* 'testing/bats/e2e/python_hybrid.bats': Assert Mongo verdict via Beru API instead of host sqlite3
* 'testing/bats': Fix worker log grep for multi-test suites; skip mongo-clean test when hybrid has no igris-http
* 'testing/bats/lib/beru_assert.bash': Add beru_wait_log and log pattern helpers for per-test Beru log assertions
* 'testing/bats': fix docker-env order, bats_begin_suite early SHADOWTEST, guarded teardown_file
* 'testing/bats': add load test_helper to .bats files; fix test_helper REPO path from BATS_ROOT_DIR
* 'testing/bats/run.sh + run-one.sh': drop unsupported --config-file; add run-one helper for filtered single tests
* 'testing/bats + docs/infrastructure': Bats-core modular testing framework with shared ShadowTest per file, settlement assertions, integration and E2E suites
* 'testing/scripts/helpers/pixie-bridge.sh', 'siphon-config.sh', 'e2e-http-otel-rmq.sh', E2E scripts: fix pixie-stream-bridge restart race (stop_pixie_stream_bridge), mongo PxL readiness wait, E2E ordering, mongo export nudge

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