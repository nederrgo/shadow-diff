#!/usr/bin/env python3
"""Download S3/MinIO session JSONL and assert ingress/egress counts vs reference."""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET
from collections import Counter
from pathlib import Path


def env(name: str, default: str = "") -> str:
    return (os.environ.get(name) or default).strip()


def s3_client():
    """Return (list_keys, get_object) callables. Prefer boto3; fall back to signed-less path via mc-style HTTP for MinIO path-style."""
    endpoint = env("MINIO_ENDPOINT_HOST") or env("MINIO_ENDPOINT")
    bucket = env("MINIO_BUCKET", "shadow-diff-local")
    access = env("MINIO_ACCESS_KEY", "admin")
    secret = env("MINIO_SECRET_KEY", "password")

    try:
        import boto3
        from botocore.client import Config

        client = boto3.client(
            "s3",
            endpoint_url=endpoint,
            aws_access_key_id=access,
            aws_secret_access_key=secret,
            region_name=env("MINIO_REGION", "us-east-1"),
            config=Config(signature_version="s3v4", s3={"addressing_style": "path"}),
        )

        def list_keys(prefix: str) -> list[str]:
            keys: list[str] = []
            token = None
            while True:
                kwargs = {"Bucket": bucket, "Prefix": prefix}
                if token:
                    kwargs["ContinuationToken"] = token
                resp = client.list_objects_v2(**kwargs)
                for obj in resp.get("Contents") or []:
                    keys.append(obj["Key"])
                if not resp.get("IsTruncated"):
                    break
                token = resp.get("NextContinuationToken")
            return keys

        def get_object(key: str) -> bytes:
            return client.get_object(Bucket=bucket, Key=key)["Body"].read()

        return list_keys, get_object
    except ImportError:
        pass

    # Stdlib fallback: MinIO anonymous/list with AWS SigV4 via hmac (minimal).
    return _stdlib_s3(endpoint, bucket, access, secret)


def _stdlib_s3(endpoint: str, bucket: str, access: str, secret: str):
    import datetime
    import hashlib
    import hmac

    endpoint = endpoint.rstrip("/")
    region = env("MINIO_REGION", "us-east-1")

    def sign(method: str, path: str, query: str, payload: bytes) -> dict[str, str]:
        now = datetime.datetime.utcnow()
        amz_date = now.strftime("%Y%m%dT%H%M%SZ")
        datestamp = now.strftime("%Y%m%d")
        payload_hash = hashlib.sha256(payload).hexdigest()
        host = urllib.parse.urlparse(endpoint).netloc
        canonical_headers = f"host:{host}\nx-amz-content-sha256:{payload_hash}\nx-amz-date:{amz_date}\n"
        signed_headers = "host;x-amz-content-sha256;x-amz-date"
        canonical_request = f"{method}\n{path}\n{query}\n{canonical_headers}\n{signed_headers}\n{payload_hash}"
        credential_scope = f"{datestamp}/{region}/s3/aws4_request"
        string_to_sign = (
            f"AWS4-HMAC-SHA256\n{amz_date}\n{credential_scope}\n"
            f"{hashlib.sha256(canonical_request.encode()).hexdigest()}"
        )

        def _sign(key: bytes, msg: str) -> bytes:
            return hmac.new(key, msg.encode(), hashlib.sha256).digest()

        k_date = _sign(("AWS4" + secret).encode(), datestamp)
        k_region = _sign(k_date, region)
        k_service = _sign(k_region, "s3")
        k_signing = _sign(k_service, "aws4_request")
        signature = hmac.new(k_signing, string_to_sign.encode(), hashlib.sha256).hexdigest()
        auth = (
            f"AWS4-HMAC-SHA256 Credential={access}/{credential_scope}, "
            f"SignedHeaders={signed_headers}, Signature={signature}"
        )
        return {
            "Authorization": auth,
            "x-amz-date": amz_date,
            "x-amz-content-sha256": payload_hash,
            "Host": host,
        }

    def list_keys(prefix: str) -> list[str]:
        keys: list[str] = []
        cont = None
        while True:
            q = [
                ("list-type", "2"),
                ("prefix", prefix),
            ]
            if cont:
                q.append(("continuation-token", cont))
            query = urllib.parse.urlencode(q)
            path = f"/{bucket}"
            headers = sign("GET", path, query, b"")
            req = urllib.request.Request(f"{endpoint}{path}?{query}", headers=headers, method="GET")
            try:
                with urllib.request.urlopen(req, timeout=60) as resp:
                    body = resp.read()
            except urllib.error.HTTPError as e:
                raise SystemExit(f"S3 ListObjects failed: {e.code} {e.read()[:200]!r}") from e
            root = ET.fromstring(body)
            ns = ""
            if root.tag.startswith("{"):
                ns = root.tag.split("}")[0] + "}"
            for contents in root.findall(f"{ns}Contents"):
                key_el = contents.find(f"{ns}Key")
                if key_el is not None and key_el.text:
                    keys.append(key_el.text)
            is_trunc = root.findtext(f"{ns}IsTruncated") == "true"
            cont = root.findtext(f"{ns}NextContinuationToken")
            if not is_trunc:
                break
        return keys

    def get_object(key: str) -> bytes:
        path = f"/{bucket}/{key}"
        headers = sign("GET", path, "", b"")
        req = urllib.request.Request(f"{endpoint}{path}", headers=headers, method="GET")
        with urllib.request.urlopen(req, timeout=60) as resp:
            return resp.read()

    return list_keys, get_object


def collect_trace_ids(list_keys, get_object, prefix: str) -> list[str]:
    ids: list[str] = []
    for key in list_keys(prefix):
        if not key.endswith(".jsonl"):
            continue
        raw = get_object(key).decode("utf-8", errors="replace")
        for line in raw.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            tid = obj.get("trace_id") or ""
            if tid:
                ids.append(tid)
    return ids


def collect_trace_ids_local(root: Path, kind: str) -> list[str]:
    ids: list[str] = []
    d = root / kind
    if not d.is_dir():
        return ids
    for path in sorted(d.rglob("*.jsonl")):
        raw = path.read_text(encoding="utf-8", errors="replace")
        for line in raw.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            tid = obj.get("trace_id") or ""
            if tid:
                ids.append(tid)
    return ids


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--reference", required=True, help="stress_sent_traces.json")
    p.add_argument("--session-id", default=env("SESSION_ID"))
    p.add_argument("--namespace", default=env("SHADOWTEST_NS", "default"))
    p.add_argument("--test-name", default=env("SHADOWTEST", "bats-stress"))
    p.add_argument("--n", type=int, default=int(env("STRESS_N", "0") or "0"))
    p.add_argument(
        "--expected-egress-per-req",
        type=int,
        default=int(env("EXPECTED_S3_EGRESS_PER_REQ", "1") or "1"),
    )
    p.add_argument(
        "--local-dir",
        default="",
        help="If set, read ingress/ and egress/ JSONL from this directory instead of S3",
    )
    args = p.parse_args()

    if not args.session_id and not args.local_dir:
        print("FAIL: --session-id / SESSION_ID required (unless --local-dir)", file=sys.stderr)
        return 1

    ref = json.loads(Path(args.reference).read_text())
    want_traces = {t["trace_id"] for t in ref["traces"]}
    n = args.n or ref.get("n") or len(want_traces)
    want_egress = n * args.expected_egress_per_req

    if args.local_dir:
        root = Path(args.local_dir)
        ingress = collect_trace_ids_local(root, "ingress")
        egress = collect_trace_ids_local(root, "egress")
    else:
        base = f"shadow-diff/{args.namespace}/{args.test_name}/sessions/{args.session_id}/"
        list_keys, get_object = s3_client()
        ingress = collect_trace_ids(list_keys, get_object, base + "ingress/")
        egress = collect_trace_ids(list_keys, get_object, base + "egress/")

    # Scope to this run's reference trace_ids (session may contain prior runs).
    ingress_run = [t for t in ingress if t in want_traces]
    egress_run = [t for t in egress if t in want_traces]
    ingress_counts = Counter(ingress_run)
    egress_counts = Counter(egress_run)

    failed = False

    def row(name: str, expected, actual, ok: bool) -> None:
        nonlocal failed
        status = "PASS" if ok else "FAIL"
        if not ok:
            failed = True
        print(f"{name:<28} expected={expected:<8} actual={actual:<8} {status}")

    row("s3_ingress_for_run", n, len(ingress_run), len(ingress_run) == n)
    ingress_once = all(ingress_counts.get(t, 0) == 1 for t in want_traces)
    row("s3_ingress_unique", n, len(ingress_counts), ingress_once and len(ingress_counts) == n)

    egress_per_trace_ok = all(
        egress_counts.get(t, 0) == args.expected_egress_per_req for t in want_traces
    )
    row(
        "s3_egress_for_run",
        want_egress,
        len(egress_run),
        len(egress_run) == want_egress and egress_per_trace_ok,
    )

    missing_ing = sorted(want_traces - set(ingress_run))
    if missing_ing:
        failed = True
        print(f"  missing ingress traces ({len(missing_ing)}): {missing_ing[:5]}...")

    missing_egr = sorted(
        t
        for t in want_traces
        if egress_counts.get(t, 0) < args.expected_egress_per_req
    )
    row("egress_covers_run", 0, len(missing_egr), len(missing_egr) == 0)
    if missing_egr:
        print(f"  missing egress traces ({len(missing_egr)}): {missing_egr[:5]}...")

    extra_session = len(set(ingress) - want_traces)
    if extra_session:
        print(
            f"  note: session has {extra_session} other ingress trace_ids "
            f"({len(ingress)} total lines) — ignored for this run"
        )

    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
