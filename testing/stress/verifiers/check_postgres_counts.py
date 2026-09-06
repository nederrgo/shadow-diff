#!/usr/bin/env python3
"""Assert Postgres raw_reports integrity for a stress replay run."""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path


def env(name: str, default: str = "") -> str:
    return (os.environ.get(name) or default).strip()


def connect():
    try:
        import psycopg2
    except ImportError:
        return None

    candidates: list[tuple[str, int]] = []
    if env("POSTGRES_HOST_HOST"):
        candidates.append(
            (env("POSTGRES_HOST_HOST"), int(env("POSTGRES_PORT_HOST") or "15432"))
        )
    # Cluster DNS only works from inside the cluster; skip unless explicitly forced.
    if env("STRESS_PG_DIRECT", "0") == "1":
        candidates.append(
            (env("POSTGRES_HOST", "127.0.0.1"), int(env("POSTGRES_PORT", "5432") or "5432"))
        )
    if not candidates:
        # Try common host port-forward, then give up → kubectl fallback.
        candidates.append(("127.0.0.1", int(env("POSTGRES_PORT_HOST") or "15432")))

    user = env("POSTGRES_USER", "beru")
    password = env("POSTGRES_PASSWORD", "beru")
    dbname = env("POSTGRES_DB", "beru")
    for host, port in candidates:
        try:
            conn = psycopg2.connect(
                host=host,
                port=port,
                user=user,
                password=password,
                dbname=dbname,
                connect_timeout=3,
            )
            return conn
        except Exception:
            continue
    return None


def kubectl_sql(sql: str) -> str:
    import subprocess

    cmd = [
        "kubectl",
        "exec",
        "-n",
        "monarch-system",
        "deploy/postgres",
        "--",
        "env",
        f"PGPASSWORD={env('POSTGRES_PASSWORD', 'beru')}",
        "psql",
        "-U",
        env("POSTGRES_USER", "beru"),
        "-d",
        env("POSTGRES_DB", "beru"),
        "-v",
        "ON_ERROR_STOP=1",
        "-Atc",
        sql,
    ]
    out = subprocess.check_output(cmd, text=True)
    return out


def sql_quote(s: str) -> str:
    return "'" + s.replace("'", "''") + "'"


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--reference", required=True)
    p.add_argument("--replay-execution-id", default=env("REPLAY_EXECUTION_ID"))
    p.add_argument("--n", type=int, default=int(env("STRESS_N", "0") or "0"))
    p.add_argument("--http", type=int, default=int(env("EXPECTED_EGRESS_HTTP", "1") or "1"))
    p.add_argument("--amqp", type=int, default=int(env("EXPECTED_EGRESS_AMQP", "1") or "1"))
    p.add_argument("--db", type=int, default=int(env("EXPECTED_EGRESS_DB", "1") or "1"))
    args = p.parse_args()

    if not args.replay_execution_id:
        print("FAIL: --replay-execution-id / REPLAY_EXECUTION_ID required", file=sys.stderr)
        return 1

    ref = json.loads(Path(args.reference).read_text())
    want_traces = {t["trace_id"] for t in ref["traces"]}
    n = args.n or ref.get("n") or len(want_traces)
    exec_id = args.replay_execution_id
    roles = ("control-a", "control-b", "candidate")

    expectations = {
        ("http", "ingress"): n,
        ("http", "egress"): n * args.http,
        ("rabbitmq", "egress"): n * args.amqp,
        ("mongodb", "egress"): n * args.db,
    }

    conn = connect()
    counts: dict[tuple[str, str, str], int] = {}
    found_traces: set[str] = set()

    if conn is not None:
        cur = conn.cursor()
        cur.execute(
            """
            SELECT shadow_role, protocol, direction, COUNT(*)
            FROM raw_reports
            WHERE replay_execution_id = %s
            GROUP BY shadow_role, protocol, direction
            """,
            (exec_id,),
        )
        for role, protocol, direction, cnt in cur.fetchall():
            counts[(role, protocol, direction)] = int(cnt)
        cur.execute(
            """
            SELECT DISTINCT trace_id FROM raw_reports
            WHERE replay_execution_id = %s
            """,
            (exec_id,),
        )
        found_traces = {r[0] for r in cur.fetchall()}
        cur.close()
        conn.close()
    else:
        qid = sql_quote(exec_id)
        # role|protocol|direction|count
        raw = kubectl_sql(
            "SELECT shadow_role || '|' || protocol || '|' || direction || '|' || COUNT(*)::text "
            f"FROM raw_reports WHERE replay_execution_id = {qid} "
            "GROUP BY shadow_role, protocol, direction"
        )
        for line in raw.splitlines():
            if not line.strip():
                continue
            role, protocol, direction, cnt = line.strip().split("|", 3)
            counts[(role, protocol, direction)] = int(cnt)
        raw_t = kubectl_sql(
            f"SELECT DISTINCT trace_id FROM raw_reports WHERE replay_execution_id = {qid}"
        )
        found_traces = {line.strip() for line in raw_t.splitlines() if line.strip()}

    failed = False
    print(
        f"{'role':<12} {'protocol':<10} {'direction':<10} "
        f"{'expected':>8} {'actual':>8} {'result':>6}"
    )
    for role in roles:
        for (protocol, direction), expected in expectations.items():
            actual = counts.get((role, protocol, direction), 0)
            ok = actual == expected
            status = "PASS" if ok else "FAIL"
            if not ok:
                failed = True
            print(
                f"{role:<12} {protocol:<10} {direction:<10} "
                f"{expected:>8} {actual:>8} {status:>6}"
            )

    missing = sorted(want_traces - found_traces)
    extra = sorted(found_traces - want_traces)
    ok_set = not missing and not extra
    print(
        f"{'trace_id_set':<12} {'-':<10} {'-':<10} "
        f"{len(want_traces):>8} {len(found_traces):>8} "
        f"{'PASS' if ok_set else 'FAIL':>6}"
    )
    if missing:
        failed = True
        print(f"  missing traces ({len(missing)}): {missing[:8]}...")
    if extra:
        failed = True
        print(f"  extra traces ({len(extra)}): {extra[:8]}...")

    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
