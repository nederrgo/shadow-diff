---
type: Workspace Log
title: Shadow-Diff Knowledge Base Change Log
description: Chronological audit trail of architectural changes, additions, and updates.
resource: https://github.com/your-org/shadow-diff/tree/main/docs
tags: [meta, changelog, history]
timestamp: 2026-06-27T19:40:00Z
---

# Shadow-Diff Documentation Log

## [2026-07-27]
### Added
* 'pipeline/kaisel': In-kernel W3C traceparent sampling — bpf_loop 768-byte header scan, FNV-1a gate matching pkg/sample exactly, LRU 5-tuple admission for continuation and response segments, SYN invalidation, fail-open on any undecidable parse

## [2026-07-26]
### Added
* 'testing/bats': Added Spike Guard integration test — igris-http 429 load shedding under concurrent traffic
* 'pipeline/monarch,pipeline/igrises': Added Spike Guard — IGRIS_MAX_CONCURRENCY load shedding, AMQP message TTL, dynamic capacity calc
* 'pipeline/pkg/sample': shared FNV full-trace-ID SampledIn for Kaisel + igris-rabbitmq
* 'testing/bats/e2e/rabbitmq-ingress/rmq_sampling_hybrid.bats': RMQ ingress + Shop egress sampling E2E at 10%
* 'testing/example-apps/http-rmq-go-worker': Stop logging trace IDs in app logs to match Node/Python fixtures for assert_worker_trace_absent
* 'testing/bats/e2e/rabbitmq-ingress/': Fixed Mongo egress test added to the hybrid suites — asserted 'clean' when candidate unconditionally double-inserts every order (matching its existing RMQ n+1 behavior), which Beru's re-diff-on-arrival logging made intermittently false-pass on a stale pre-mismatch log line; replaced with a count-regression assertion
* 'testing/bats/e2e/rabbitmq-ingress/': Added MongoDB egress diff coverage (captured for all three roles + clean for isolated trace) to the Node.js and Python hybrid suites, which already deployed Mongo but never asserted on it; fixed docs/verification/hybrid-rmq-e2e-flow.md's stale OTLP reference and its false 'covered elsewhere' claim about a nonexistent mongo_egress.bats
* 'pipeline/beru/internal/otlp/': Removed dormant OTLP MongoDB egress route (:4317 receiver, POST /v1/traces, FromMongoEgress, beru-local otlp-grpc port, go.opentelemetry.io/proto/otlp dependency) in favor of shadow-soldier wire capture; beru-local gains ingest port 8081 to bypass the shadow pod's 8080 iptables redirect
* 'pipeline/shadow-soldier/': Added L4b database egress capture sidecar — plain-text TCP proxy decoding MongoDB/PostgreSQL/Redis/MSSQL wire protocols, Postgres SSLRequest 'N' downgrade, bounded fail-open parser tap, trace-sharded reporter posting to Beru /api/v1/egress/diff
* 'testing/bats, pipeline/beru': Kaisel→Shop→Envoy→Beru HTTP egress E2E; mirrorLegacyLogs handles http egress direction; beru_wait_http_egress_match helper
* 'pipeline/shop, pipeline/beru, pipeline/monarch, docs': Shop buffers egress request body and async-reports HTTP egress to Beru /api/v1/egress/diff; EgressSignature http case; Shop BERU_HTTP_URL env
* 'testing/bats/e2e/kaisel-capture': Replay E2E from copied prod traffic (no igris redrive)
* 'pipeline/kaisel': Forward captured request headers to igris (drop hop-by-hop)
* 'testing/bats/e2e/kaisel-capture': Kaisel→Shop→Envoy egress replay E2E
* 'testing/example-apps/egress-test-app': Header-driven egress scenarios for Kaisel E2E
* 'testing/bats/lib/platform.bash': Dropped the cluster-wide Beru health gate and bootstrap deploy — no suite points a ShadowTest at beru-system; beru-local (same image) is provisioned per ShadowTest
* 'pipeline/pixie-gate,pipeline/recorder': Removed Pixie and Recorder — Kaisel is now the sole HTTP capture path; MongoDB egress diffing withdrawn while Beru's OTLP receiver, wire parser and diff stay dormant
* 'docs/data-plane/kaisel-ebpf.md': Documented the egress pairing gate — evaluated on the request direction for both half-streams, and why reversing beats widening the predicate
* 'testing/bats/lib/kaisel.bash': Match the egress mock key itself rather than a hash= prefix — slog quotes values containing '=', so a key with a query string logged as hash=... never matched the bare-prefix pattern
* 'pipeline/kaisel/internal/decode': Fixed egress pairing gate — WantTransaction is evaluated on the request direction for both half-streams; asking with the response half's own flow tested the dependency address and discarded every response before it could pair
* 'testing/bats': Assert Recorder seeded nothing during kaisel egress tests, so a Shop mock is attributable to Kaisel rather than the concurrent Pixie/Recorder path
* 'pipeline/kaisel,pipeline/monarch,testing': Egress capture — HTTP response parsing, per-connection FIFO request/response pairing, and direct Shop mock seeding via KaiselRule.egressBaseURL; adds egress-test-app E2E workload

## [2026-07-25]
### Added
* 'pipeline/monarch samplePercentage': unified top-level field for HTTP and AMQP sampling
* 'testing/tools/e2e-reset-minikube.sh,testing/bats/lib/platform.bash': deploy Kaisel DaemonSet in minikube reset and bats platform bootstrap
* 'testing/bats/lib/kaisel.bash,pipeline/*/go.sum': fix kaisel E2E stuck setup — fail hard on missing images; teardown --wait=false; tidy go.sum for docker builds
* 'testing/bats/e2e/kaisel-capture': full-route E2E Monarch→prod→Kaisel→igris→shadow pods (traceparent + multicast + nginx access logs)
* 'pipeline/siphon removed': deleted Siphon module; ShadowTest.spec.samplePercentage + status.kaiselPhase; HTTP ingress is Kaisel-only
* 'pipeline/igrises/igris-http': ResolveContext rejects missing/invalid traceparent (no mint); tracing is a prerequisite for HTTP multicast
* 'pipeline/kaisel + KaiselRule': Step 3b Kaisel→igris export — admit/SampledIn/Forward routed by dst IP via KaiselRule.igrisBaseURL; igris-http unchanged
* 'docs/data-plane/siphon-audit.md': Siphon audit ADR — admit/sampling/forward move into Kaisel userspace, not igris-http; index + kaisel-ebpf cross-links
* 'pipeline/kaisel': added -log-bodies flag (default false) to print captured request body content in kaisel logs, gated behind explicit opt-in since bodies are real production data and pod logs are commonly shipped off-node by cluster log aggregation. Wired through capture.Config -> decode.StreamFactory, capped at 4KB per log line, exposed as logBodies in the kaisel-config ConfigMap and threaded into the DaemonSet's env/args. Verified live: full JSON body appears in the log line when enabled, absent (only body_bytes count) by default.
* 'testing/bats/e2e/kaisel-capture,testing/bats/lib/kaisel.bash': added E2E test verifying Monarch + kaisel self-heal after a prod pod crash — force-deletes the target pod, confirms Monarch's Pod watch rewrites the KaiselRule to the replacement pod's new IP with no restart, and confirms kaisel's controller-runtime reconciler pushes that IP into the live BPF map so traffic to the new pod is captured. All 4 kaisel-capture E2E tests pass.
* 'pipeline/kaisel,pipeline/monarch': fixed kaisel E2E — moved KaiselRule creation ahead of shadow-stack readiness gates in Monarch's reconcile loop, fixed a startup deadlock where a pending MapUpdate could never be drained if no packet had yet matched the empty target_ips map, added missing kaiselrules CRD to config/crd/kustomization.yaml, fixed the kaisel/monarch go.sum + Dockerfile (golang:1.26, repo-root build context for the monarch replace directive) so docker-build actually works, fixed a duplicate -kubeconfig flag registration panic. Switched kaisel's default capture interface from eth0 to any (ifindex 0, same mechanism as tcpdump -i any) since a Linux bridge never clones same-node pod-to-pod traffic to a listener on any single device including eth0 itself (verified via live tcpdump); added an explicit lo_ifindex kernel-side exclusion (resolved via net.InterfaceByName at load time) since loopback's non-Ethernet framing cannot share the fixed l2_off constant with every other device now multiplexed onto the socket. Documented the accepted security trade-off (wider kernel-filter attack surface, stale-IP blast radius) and the eth0 fallback in docs/data-plane/kaisel-ebpf.md. All 3 kaisel-capture E2E tests and both modules' unit test suites pass.
* 'testing/bats/e2e/kaisel-capture,testing/bats/lib/kaisel.bash,testing/bats/lib/env.bash': self-contained kaisel E2E setup — kaisel_setup_platform builds+loads Monarch+kaisel images, installs CRDs, deploys operator; test no longer requires pre-deployed platform
* 'pipeline/kaisel/Dockerfile,pipeline/kaisel/Makefile': add Dockerfile and docker-build target to complete kaisel as a deployable DaemonSet image
* 'pipeline/kaisel/deploy,pipeline/monarch/config/rbac': least-privilege DaemonSet deploy manifests for kaisel (CAP_BPF/NET_RAW/PERFMON, no privileged:true, hostNetwork, cloud-agnostic ConfigMap iface) + Monarch RBAC cleanup (drop pixiestreamrules rules, delete stale shadow_deployment_role.yaml, add restricted PSA label to monarch-system namespace)
* 'testing/bats/e2e/kaisel-capture': full-stack E2E bats test — ShadowTest → KaiselRule → kaisel eBPF capture → HTTP request asserted in kaisel log
* 'pipeline/monarch,pipeline/kaisel': KaiselRule CRD + Monarch reconciliation (live pod IP resolution, pod watch predicate, no-op patch optimization) + kaisel map-sync controller replacing Pixie ingress capture
* 'pipeline/kaisel': Drop and count fragmented IPv4 datagrams in the kernel filter — gopacket already declines to decode fragments, so this adds a stated cause for an otherwise silent flow; covered by a loopback integration test asserting the counter
* 'docs/data-plane/kaisel-ebpf.md': Added Kaisel eBPF capture spec — filter chain, chunked perf transport for GSO super-packets, design rationale (socket-filter fail-open, perf array vs ringbuf, why sampling cannot protect the ring) and limitations; mapped in data-plane index
* 'pipeline/kaisel/internal/capture': Unified target_ips on host-order keys — capture.c composes both IPv4 addresses byte-wise from one 8-byte header read, so map keys no longer depend on node endianness and Go seeds with BigEndian instead of NativeEndian
* 'pipeline/kaisel': Chunked eBPF capture for GSO super-packets — 128KB reach, TCP+port kernel filters, 1MB/CPU ring, netns integration suite
* 'pipeline/kaisel': Add lean eBPF collector core — SOCKET_FILTER capture, perf-array PacketSource seam, CNI-agnostic decode, HTTP stream reassembly

## [2026-07-24]
### Added
* 'pipeline/pixie-gate': Add least-privilege in-cluster Go service replacing host pixie-stream-bridge; wire setup/bats/docs
* 'testing/bats/helpers/pixie-bridge.sh,docs/control-plane/monarch-controller.md': Fix PxL sampling hex decode — px.atoi has no radix; use nibble select so load shed stays in Pixie
* 'pipeline/siphon,pipeline/recorder,pipeline/igrises/igris-rabbitmq,testing/bats': Prod-gate shared sampling (V*100)<(N*256) in Pixie+Go; empty trace drop; RMQ/HTTP bats proofs
* 'testing/bats/manifests/pixie-bridge,testing/bats/helpers/pixie-bridge.sh': Removed samplePercentage from shadow-pod Mongo PxL (ingress already samples)
* 'pipeline/monarch,pipeline/igrises/igris-rabbitmq,testing/bats/helpers/pixie-bridge.sh': Added samplePercentage trace-based sampling across Pixie HTTP/Mongo capture and RabbitMQ ingress
* 'testing/bats/verdict_ui': wait beru-local only; skip full ShadowTest Ready
* 'testing/bats': verdict_ui seed suite uses direct assert (no quiescence settle)
* 'testing/bats': UI seed MATCH when timestamp field differs across A/B/C (natural noise)
* 'pipeline/beru + bats': add MISSING_EGRESS case (A/B=2, candidate=1) unit + UI seed tests
* 'pipeline/beru': Ignore path only on payload field NoisePath; hide for count/signature mismatches
* 'testing/bats/integration/beru': seed-reports API + verdict_ui bats with BATS_KEEP for dashboard inspection

## [2026-07-23]
### Added
* 'pipeline/beru/internal/v2': single-trace verdicts, baseline void, compound diffs, WAITING_FOR_ROLES timeout
* 'pipeline/beru': purge legacy internal/diff package, wire user noise filters into v2 EvaluateTraceHistory, remove committed beru binary, add .gitignore
* 'testing/bats/integration/monarch/deps_update.bats': add integration test for live dependency add (mongodb deps created, shadow app pods roll with MONGO_URL)
* 'testing/bats/integration/monarch/lifecycle.bats': add 4 ShadowTest lifecycle integration tests (mid-delete, re-apply while deleting, recreate after clean, delete after Ready)
* 'pipeline/monarch/internal/controller/shadowtest_delete_lifecycle_test.go': add fake-client unit tests for mid-bring-up delete race (late creates cleaned, no recreate/Ready while deleting)
* 'testing/bats/lib/shadowtest.bash': Dump matched pods, waiting reasons/images, and Warning events on wait_shadowtest_ready fail-fast (stdout so bats reporters keep it)

## [2026-07-16]
### Added
* 'pipeline/monarch/README.md': Align with current CRD — drop otelInjection/recordAndReplay; document Envoy+iptables, beru-local, always-on Shop+Recorder
* 'docs/data-plane/egress-record-replay.md': Apply doc-hygiene — strip legacy negatives (recordAndReplay / host allowlist absences)
* 'docs/architecture/ARCHITECTURE.md': Apply doc-hygiene — strip legacy negatives (recordAndReplay absences) and always-on Shop+Recorder wording

## [2026-07-14]
### Added
* 'testing/bats/lib/reporter.bash': Add BATS_REPORTER=verbose mode — streams ✓/✗ marks with all diagnostic lines shown inline for both passing and failing tests; uses bats --show-output-of-passing-tests
* 'testing/bats/fixtures/{e2e/rabbit-ingress,integration/mongo-egress}': retarget broken prod-*.yaml symlinks from scripts/manifests to testing/bats/manifests
* 'testing/bats/integration/monarch/ambiguous_ports.bats + fixtures/integration/monarch-ambiguous-ports/ + lib/monarch_assert.bash': Add integration test for multi-port disambiguation error; ShadowTest with two unnamed container ports reaches Failed with clear applicationPort error; monarch_wait_shadowtest_failed helper added
* 'pipeline/monarch/internal/controller/shadowtest_helpers.go + shadowtest_controller.go + api/v1alpha1/shadowtest_types.go': resolveSpecDefaults derives oldImage/applicationPort/servicePort from target Deployment at reconcile time; users now only need targetDeployment + newImage in ShadowTest spec; CRD regenerated, all integration tests passing
* 'testing/bats/fixtures/integration/': Simplified ShadowTest fixture YAMLs — removed oldImage, servicePort, applicationPort (now derived by resolveSpecDefaults from target Deployment); updated monarch-http-input and monarch-basic fixtures
* 'testing/bats/integration/monarch/http_input.bats': Add Monarch HTTP input integration tests — igris-http and siphon deployment readiness assertions using monarch_assert lib; add prod-target fixture and monarch-http-input ShadowTest fixture; extend monarch_assert with monarch_wait_igris_running and monarch_wait_siphon_running
* 'testing/bats/lib/monarch_assert.bash + integration/monarch/': Add Monarch integration testing framework — monarch_assert lib with pod-readiness polling, CrashLoop detection, and diagnostics; integration/monarch/ directory and monarch-basic fixture for the integration testing pyramid layer

## [2026-07-13]
### Added
* 'testing/bats/lib/reporter.bash': Auto Jest reporter on BATS_PARALLEL_JOBS=1 even when make/stdout fails TTY check
* 'testing/bats/lib/reporter.bash': Default FORCE_COLOR/TAP_COLORS for Jest reporter; prefer Linux node over node.exe

## [2026-07-12]
### Added
* 'testing/bats/lib/reporter.bash': Resolve WSL Windows node.exe beside npm for tap-mocha-reporter
* 'testing/bats': Jest-like tap-mocha-reporter for BATS_PARALLEL_JOBS=1; roadmap doc for future isolation + bats --jobs
* 'testing/bats/manifests': remove stale spec.recordAndReplay from e2e-shadowtest and rabbitmq* ShadowTest YAMLs (CRD rejects unknown field)
* 'testing/bats/helpers/cluster-minikube.sh': Reuse healthy Running minikube instead of always calling minikube start (fixes --no-reset bouncing an existing cluster+bridge)
* 'testing/tools/e2e-reset-minikube.sh': Fix REPO to ../.. after move from testing/bats/; pin beru:dev; scrub stale bats/e2e-reset and deleted --run-*-test docs
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