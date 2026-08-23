# Stress load-test suite

Standalone record → zero-loss capture → replay → Postgres integrity suite.

Does **not** bootstrap the platform. Point it at an already-running Kind (or other)
cluster with Monarch, Kaisel DaemonSet, MinIO, Postgres, and the HTTP+RMQ+Mongo
prod stack.

## Prerequisites

1. Cluster with Monarch (`MONARCH_MODE=dev`, `BERU_DB_SECRET=monarch-system/beru-postgres`).
2. Kaisel DaemonSet in `kaisel-system`.
3. MinIO bucket `shadow-diff-local` + Secret `shadow-diff-s3` in `default`.
4. Postgres fixture in `monarch-system`.
5. Prod stack:
   - `rmq-prod-broker`, `mongo-prod`
   - `http-rmq-python-prod` (image `http-rmq-python-worker:dev` with HTTP egress enabled)
   - `user-service-python` ([prod-user-service-python.yaml](../bats/manifests/rabbitmq-otel-e2e/prod-user-service-python.yaml))
6. Rebuild worker after pulling this change:

```bash
make -C testing/example-apps/http-rmq-python-worker docker-build
# load into Kind, then:
kubectl apply -f testing/bats/manifests/rabbitmq-otel-e2e/prod-user-service-python.yaml
kubectl set env deployment/http-rmq-python-prod \
  HTTP_EGRESS_CONNECT_URL=http://user-service-python.default.svc.cluster.local:8080/v1/log \
  HTTP_EGRESS_REPLAY_HOST=user-service.prod.internal
kubectl rollout restart deployment/http-rmq-python-prod
```

## Quick start

```bash
# Optional: port-forward MinIO + Postgres for host-side verifiers
kubectl -n monarch-system port-forward svc/minio-service 9000:9000 &
kubectl -n monarch-system port-forward svc/postgres 15432:5432 &

# Smoke (N=10)
STRESS_N=10 STRESS_RPS_START=5 STRESS_RPS_END=10 STRESS_RAMP_SEC=5 \
  make test-stress

# Default N=1000 with RPS ramp 500→2000
make test-stress
```

Or:

```bash
source testing/stress/config.env
./testing/stress/run_stress_test.sh [--skip-apply] [--skip-load] [--n N] [--rps-end R]
```

## What it asserts

| Stage | Check | Expected |
|-------|-------|----------|
| Record | Kaisel eBPF drop deltas | 0 |
| Record | S3 ingress JSONL lines | N |
| Record | S3 egress JSONL lines | N × `EXPECTED_S3_EGRESS_PER_REQ` (HTTP only) |
| Replay | Postgres `http/ingress` per role | N |
| Replay | Postgres `http/egress` per role | N × `EXPECTED_EGRESS_HTTP` |
| Replay | Postgres `rabbitmq/egress` per role | N × `EXPECTED_EGRESS_AMQP` |
| Replay | Postgres `mongodb/egress` per role | N × `EXPECTED_EGRESS_DB` |
| Replay | Distinct `trace_id` set | Exact match to `stress_sent_traces.json` |

Replay completion is inferred from `status.replayState=started`, Igris log
`replay loop finished`, and a stable Postgres row count (Monarch does not yet
set `replayState=completed`).

## Layout

```
testing/stress/
├── config.env
├── run_stress_test.sh
├── fixtures/shadowtest.yaml
├── generator/load_gen.go
└── verifiers/
    ├── check_ebpf_drops.sh
    ├── check_s3_counts.py
    └── check_postgres_counts.py
```
