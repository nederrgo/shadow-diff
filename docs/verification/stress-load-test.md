---
type: Verification Guide
title: Stress Load-Test Suite
description: Standalone record→zero-loss→replay→Postgres integrity load suite under testing/stress/.
resource: https://github.com/shadow-diff/monarch/tree/main/testing/stress
tags: [verification, stress, load-test, kaisel, s3, postgres, record-replay]
timestamp: 2026-08-23T10:00:00Z
---

# Stress Load-Test Suite

Automated load suite that drives Record Mode traffic, asserts zero-loss capture
(Kaisel eBPF + S3 JSONL), switches to Replay Mode pinned to the captured session,
and verifies PostgreSQL `raw_reports` integrity across Control-A / Control-B /
Candidate for ingress, HTTP egress, AMQP egress, and MongoDB egress.

Implementation lives under [`testing/stress/`](https://github.com/shadow-diff/monarch/tree/main/testing/stress).
Operational README: same directory.

## Prerequisites (standalone)

This suite does **not** bootstrap Kind or Monarch. The cluster must already have:

* Monarch controller (`MONARCH_MODE=dev`, `BERU_DB_SECRET=monarch-system/beru-postgres`)
* Kaisel DaemonSet (`kaisel-system`)
* MinIO (`shadow-diff-local`) + `shadow-diff-s3` Secret
* Postgres fixture (`monarch-system/postgres`)
* Prod stack: `http-rmq-python-prod`, `rmq-prod-broker`, `mongo-prod`, `user-service-python`
* Worker image rebuilt with optional HTTP egress (`HTTP_EGRESS_CONNECT_URL` set)

## Defaults

| Parameter | Default | Notes |
|-----------|---------|-------|
| `STRESS_N` | 1000 | Override for CI smoke (`STRESS_N=10`) |
| RPS ramp | 500 → 2000 over 60s | `STRESS_RPS_*` / `STRESS_RAMP_SEC` |
| `S3_FLUSH_WAIT_SEC` | 300 | Poll until S3 line counts reach N (not file-count alone) |
| S3 egress / request | 1 | HTTP only (Kaisel → Shop) |
| Postgres egress / role | HTTP×1 + AMQP×1 + Mongo×1 | `EXPECTED_EGRESS_*` |

## Run

```bash
STRESS_N=10 STRESS_RPS_START=5 STRESS_RPS_END=10 STRESS_RAMP_SEC=5 make test-stress
# or
./testing/stress/run_stress_test.sh --n 10 --rps-end 10
```

## Pipeline

1. Apply [`fixtures/shadowtest.yaml`](https://github.com/shadow-diff/monarch/blob/main/testing/stress/fixtures/shadowtest.yaml) (`mode: record`); wait `phase=Ready` and `kaiselPhase=Ready`.
2. Snapshot Kaisel drop counters (`check_ebpf_drops.sh --snapshot`).
3. Run `load_gen` with deterministic `traceparent` IDs → `stress_sent_traces.json`.
4. Wait MinIO object count stable, then **poll S3 JSONL line counts** until ingress = N and egress = N × `EXPECTED_S3_EGRESS_PER_REQ` (Kaisel→Shop→S3 can lag minutes at high RPS); assert eBPF deltas == 0.
5. Patch `spec.mode=replay` + `spec.sessionID=<currentSessionID>`.
6. Wait `replayState=started`, Igris log `replay loop finished`, Postgres row-count stability.
7. Assert per-role `(protocol, direction)` counts and exact `trace_id` set match.

## Platform caveats

* There is no `kaisel_ebpf_dropped_events` Prometheus metric; drops are read from Kaisel logs (`frag_drops`, `sample_drops`, ring `lost`) and optionally `bpftool`.
* Monarch does not yet set `replayState=completed`; completion is inferred from Igris + Postgres stability.
* `raw_reports` has no `report_type` column — classify with `protocol` + `direction`.

## Troubleshooting

| Symptom | Likely cause |
|---------|----------------|
| eBPF `ring_lost` / `frag_drops` > 0 | RPS above Kaisel perf-ring capacity; lower `STRESS_RPS_END` or raise per-CPU pages |
| S3 ingress < N | KaiselRule not Ready, sampling < 100%, or prod not targeted |
| S3 egress < N (ingress OK) | Export queue / S3 uploader still draining — raise `S3_FLUSH_WAIT_SEC` or wait for line-count poll; not prod loss |
| S3 egress = 0 | `HTTP_EGRESS_CONNECT_URL` unset or `user-service-python` missing |
| Postgres AMQP/Mongo missing | Shadow deps / egress-relay / shadow-soldier not roll-ready in replay |
| `load_gen` HTTP failures | Prod worker returning 500 (egress dep down) |

## Citations

* [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md) — record/replay mode GC and `replayState=started`
* [/control-plane/replay-execution-isolation.md](/control-plane/replay-execution-isolation.md) — `sessionID` / `replay_execution_id`
* [/data-plane/beru-postgres-storage.md](/data-plane/beru-postgres-storage.md) — `raw_reports` schema
* [/verification/http-ingress-e2e-flow.md](/verification/http-ingress-e2e-flow.md) — bats HTTP ingress record→replay pattern
