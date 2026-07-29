---
type: Architectural Decision Record
title: Asynchronous Record & Replay Pivot
description: Accepted proposal to evolve Shadow-Diff from live-traffic shadow proxy to an S3-backed asynchronous Record-and-Replay platform.
resource: https://github.com/shadow-diff/monarch/tree/main/docs/refactor
tags: [refactor, adr, record-replay, s3, igris, shop, monarch, kaisel]
timestamp: 2026-07-28T15:20:00Z
---

# Architecture Proposal: Evolving Shadow-Diff to Asynchronous Record & Replay

| Field | Value |
|-------|-------|
| Date | 2026-07-28 |
| Status | Accepted / Implementation in Progress |
| Authors | Shadow-Diff Core Team |

## Executive Summary

Shadow-Diff is pivoting its core architecture from a live-traffic shadow proxy to an asynchronous S3-backed Record-and-Replay platform.

While the live-traffic model successfully proved the value of our 3-pod differential testing strategy (Control A vs. Control B vs. Candidate), the synchronous coupling of live production traffic to shadow testing environments creates severe timing race conditions, complicates infrastructure management, and makes CI/CD integration difficult.

By decoupling the capture of data (Record Mode) from the execution of tests (Replay Mode) via object storage (S3/MinIO), we will eliminate race conditions, unlock "Shift-Left" CI/CD use cases, drastically reduce cloud costs for our users, and simplify our codebase.

## 1. The Motivation: Why We Are Pivoting

The current architecture relies on Kaisel (our eBPF agent) tapping production traffic and synchronously routing it through Igris to the shadow pods, while simultaneously trying to capture egress responses and populate the Shop mock-store.

This led to three major friction points:

| Friction | Detail |
|----------|--------|
| Microsecond race condition | Kaisel must capture the production egress response and populate the Shop in-memory store before the shadow pod makes that exact outbound call. This tight temporal coupling is inherently brittle and causes false-positive test failures. |
| Prohibitive cloud costs | Users currently have to run 3 extra copies of their application (A/B/C) 24/7 alongside production just to catch regressions. |
| Lack of CI/CD integration | Developers want to test their Pull Requests against yesterday's production traffic. A live-only tool cannot do this. |

## 2. The Target Architecture: Two Distinct Phases

To solve these issues, the pipeline will be split into two decoupled phases.

### Phase 1: Record Mode (Lightweight & Always-On)

In this phase, no shadow pods are running.

* **Ingress:** Kaisel (eBPF) or prod AMQP queues tap incoming traffic and send it to Igris. Igris extracts the trace context, batches the payloads, and writes them to S3.
* **Egress:** Kaisel taps outbound production traffic, pairs requests with responses, and sends them to Shop. Shop batches these payloads and writes them to S3.

### Phase 2: Replay Mode (On-Demand & Deterministic)

Triggered manually, via a schedule, or by a CI/CD pipeline.

1. **Setup:** Monarch spins up the 3 shadow pods (A, B, Candidate) in an isolated namespace.
2. **Pre-load:** Shop wakes up, queries S3 for the requested session, and loads all egress mock responses into its in-memory map.
3. **Execution:** Igris pulls the ingress traces from S3 and begins multicasting them to the shadow pods. Because Shop is fully pre-loaded, there are zero race conditions when the shadow pods make outbound calls.
4. **Analysis:** Beru compares the responses using our existing diff-of-diffs engine.

> **Note on time-drift:** Because we diff 3 identical pods at the exact same time, time-based drift like expired JWTs or `now()` functions will cause all 3 pods to behave identically, which Beru will correctly filter out as environmental noise.

## 3. Key Architectural Decisions

### Decision A: "Bring Your Own Bucket" (BYOB)

Monarch will not have the IAM permissions to create S3 buckets. Bucket creation is an Infrastructure-as-Code (Terraform/Platform) responsibility. The ShadowTest CRD will simply accept a bucket name and credentials. Monarch never deploys object storage.

**Developer experience fallback:** For local development and fast onboarding, [`testing/tools/e2e-reset-minikube.sh`](../../testing/tools/e2e-reset-minikube.sh) applies an ephemeral MinIO Deployment/Service under `monarch-system` (manifests in [`testing/bats/manifests/minio/`](../../testing/bats/manifests/minio/)), creates bucket `shadow-diff-local`, and a `shadow-diff-s3` credentials Secret in `default`.

### Decision B: Igris and Shop are "Storage Gateways"

Kaisel will not interact with S3. eBPF user-space daemons should be lightweight. Embedding AWS SDKs into a DaemonSet bloats the nodes and complicates security. Kaisel will remain a "dumb pipe" that POSTs data to Igris/Shop. Igris and Shop will handle S3 batching, writing, and reading via the shared [`pipeline/pkg/s3utils`](../../pipeline/pkg/s3utils) `BatchUploader` (AWS SDK v2, path-style when `S3_ENDPOINT` is set for MinIO).

### Decision C: S3 Folder Structure & Data Lifecycle

To prevent runaway AWS costs and ensure clean data management, S3 data will be strictly structured:

```text
s3://<bucket>/shadow-diff/<namespace>/<test-name>/sessions/<session-id>/[ingress|egress]/
```

The ShadowTest CRD will introduce a `retentionPolicy` (`Retain` or `Delete`). If set to `Delete`, Monarch will use Kubernetes Finalizers to automatically scrub the S3 prefix before allowing the K8s namespace/CRD to be deleted.

### Decision D: Strict Data Privacy (PII)

By utilizing a BYOB model inside the user's existing production cluster, production data never leaves the user's security boundary. The S3 bucket, the Shadow pods, and the analysis all happen inside the isolated cluster. PII masking will be applied exclusively at the Beru presentation layer (the UI).

### Decision E: One CR, two modes (`record` | `replay`)

`spec.mode` is only `record` or `replay` (kubebuilder default `record`; empty resolves to `record`). There is no live-traffic mode. `spec.storage` is **required** for every ShadowTest. Optional `spec.sessionID` pins the S3 session folder; on record Monarch mints `status.currentSessionID` (`session-<unix>`) when unset; on replay a session must already be resolvable from spec or status.

**Phase 4:** Monarch injects `OPERATING_MODE` + S3 env into Igris and Shop (credentials Secret synced CR ns → shadow ns), exposes Igris admin `:9090`, and garbage-collects by mode — record deletes ABC Deployments/Services and clears `status.replayState`; replay deletes KaiselRule and skips creating it. When the replay stack is roll-ready and `status.replayState` is empty, Monarch `POST`s `…:9090/v1/replay/start` (202/409 → `status.replayState=started`). On CR deletion, finalizer `shadow-diff.io/s3-cleanup` deletes objects under `shadow-diff/<ns>/<name>/` when `retentionPolicy=Delete` (BYOB bucket is never deleted); `Retain` skips prefix cleanup.

**Record-mode bats:** After MinIO is up (`e2e-reset-minikube.sh` or the suite’s `minio_ensure`), run `make test-bats-record` (or `make test-bats-one FILE=e2e/record/record_http.bats`). The suite applies `mode: record` + storage against the kaisel-capture prod target and asserts MinIO objects under `sessions/<status.currentSessionID>/{ingress,egress}/`. It is not part of `make test-bats-e2e` until other fixtures gain required `spec.storage`.

## 4. Implementation Roadmap (The 5 Phases)

We will execute this transition in 5 incremental mini-plans to avoid breaking the main branch.

| Phase | Focus | Scope |
|-------|-------|-------|
| 1 | Storage Foundation & Configuration | Update ShadowTest CRD with storage config (S3 endpoint, bucket, retention policy). Add MinIO via the minikube setup script for developer testing. Update Monarch to parse and validate these fields. |
| 2 | Record Mode (S3 Writers) | Shared `pipeline/pkg/s3utils` BatchUploader (JSONL, 5s/100 flush). Igris-http and Shop honor `OPERATING_MODE=record`: buffer ingress/egress to `shadow-diff/<ns>/<test>/sessions/<id>/{ingress\|egress}/`. Kaisel stays a dumb POST pipe. Monarch env injection is Phase 4. |
| 3 | Replay Mode (S3 Readers) | Shared `s3utils.S3Reader` (sorted FIFO ListObjectsV2 + GetObject). Shop `OPERATING_MODE=replay` preloads egress JSONL before gRPC (`/healthz` 503→200). Igris-http preloads ingress JSONL and exposes `POST /v1/replay/start` (202; 409 if already running) to multicast reconstructed requests with preserved `traceparent` and per-target `x-shadow-role`. |
| 4 | Monarch Orchestration & Lifecycle | Complete: mode record\|replay, required storage, session mint/pin, S3 env + Secret sync, Igris admin `:9090`, mode GC, auto `POST /v1/replay/start` → `replayState=started`, `shadow-diff.io/s3-cleanup` prefix delete when `retentionPolicy=Delete`. |
| 5 | Cleanup & Productization (Shift-Left) | Remove legacy live-traffic race-condition handling from Kaisel and Envoy sidecars. Update Beru UI to display Session ID. Document triggering Replay from a GitHub Action. |

## Conclusion

This pivot transforms Shadow-Diff from a complex live-monitoring experiment into a highly deterministic, enterprise-grade regression testing platform. It plays to our core strength (the A/B/C diff engine) while offloading our biggest weakness (microsecond network timing) to highly reliable, scalable object storage.

## Citations

* [/architecture/ARCHITECTURE.md](/architecture/ARCHITECTURE.md) — Current record/replay layer stack
* [/data-plane/egress-record-replay.md](/data-plane/egress-record-replay.md) — Shop/Kaisel egress record/replay path
* [/refactor/ARCHITACTURE_SHIFT.md](/refactor/ARCHITACTURE_SHIFT.md) — Prior telemetry-dependent architectural pivot
* [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md) — Monarch reconcile contract
