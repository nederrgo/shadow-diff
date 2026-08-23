#!/usr/bin/env python3
"""Trace-unaware HTTP ingress -> Mongo + HTTP + RabbitMQ egress worker for OTel E2E."""

import json
import os
import signal
import sys
import time

import pika
import pymongo
import requests
from flask import Flask, jsonify, request

EGRESS_EXCHANGE = os.environ.get("RMQ_EGRESS_EXCHANGE", "egress-events")
EGRESS_ROUTING_KEY = os.environ.get("RMQ_EGRESS_ROUTING_KEY", "order.egress")
# Shop egress mock key (:authority / Host header). Empty CONNECT_URL skips HTTP egress
# so bats suites that omit user-service keep working.
HTTP_EGRESS_REPLAY_HOST = os.environ.get(
    "HTTP_EGRESS_REPLAY_HOST", "user-service.prod.internal"
)
HTTP_EGRESS_CONNECT_URL = (os.environ.get("HTTP_EGRESS_CONNECT_URL") or "").strip()


def env_or(name: str, default: str) -> str:
    val = (os.environ.get(name) or "").strip()
    return val or default


def normalize_amqp_url(raw: str) -> str:
    raw = (raw or "").strip()
    if not raw:
        return ""
    if raw.startswith("amqp://") or raw.startswith("amqps://"):
        return raw
    return f"amqp://guest:guest@{raw.lstrip('/')}/"


def listen_port() -> int:
    # Manifests set HTTP_PORT; LISTEN_ADDR matches Go/Node fixtures.
    if http_port := env_or("HTTP_PORT", ""):
        return int(http_port)
    return int(env_or("LISTEN_ADDR", ":8080").lstrip(":") or "8080")


def is_shadow_worker() -> bool:
    return "shadow" in env_or("AMQP_URL", "") or "shadow" in env_or("MONGO_URL", "")


def http_post(
    url: str, payload: dict, headers: dict | None = None, timeout: int = 30
) -> requests.Response:
    """Retry 599 while prod egress is recorded for Shop replay."""
    headers = headers or {}
    deadline = time.monotonic() + 60
    while True:
        resp = requests.post(url, json=payload, headers=headers, timeout=timeout)
        if resp.status_code != 599 or time.monotonic() >= deadline:
            return resp
        time.sleep(2)


def publish_egress(amqp_url: str, doc: dict, traceparent: str | None) -> None:
    # ponytail: open per request — BlockingConnection heartbeats stall under idle Flask
    # and long bats setup_file windows; Go/Node clients keep IO alive in the background.
    conn = pika.BlockingConnection(pika.URLParameters(amqp_url))
    try:
        ch = conn.channel()
        ch.exchange_declare(exchange=EGRESS_EXCHANGE, exchange_type="topic", durable=True)
        ch.basic_publish(
            exchange=EGRESS_EXCHANGE,
            routing_key=EGRESS_ROUTING_KEY,
            body=json.dumps(doc),
            properties=pika.BasicProperties(
                content_type="application/json",
                delivery_mode=2,
                headers={"traceparent": traceparent} if traceparent else {},
            ),
        )
    finally:
        conn.close()


def main() -> None:
    port = listen_port()
    amqp_url = normalize_amqp_url(env_or("AMQP_URL", ""))
    if not amqp_url:
        raise SystemExit("AMQP_URL is required")

    # Fail fast if RMQ is down at boot (same as previous long-lived connect).
    probe = pika.BlockingConnection(pika.URLParameters(amqp_url))
    probe.close()

    mongo_coll = None
    mongo_url = env_or("MONGO_URL", "")
    if mongo_url:
        mongo_db = env_or("MONGO_DB", "test")
        mongo_coll = pymongo.MongoClient(mongo_url)[mongo_db]["items"]

    app = Flask(__name__)

    @app.get("/healthz")
    def healthz():
        return "ok", 200

    @app.post("/publish")
    def publish():
        body = request.get_json(silent=True) or {}
        doc = {**body, "source": "http-rmq-python-worker"}
        traceparent = request.headers.get("traceparent")
        try:
            if mongo_coll is not None:
                mongo_opts = {"comment": traceparent} if traceparent else {}
                mongo_coll.insert_one({**doc}, **mongo_opts)
                print("mongo insert ok", flush=True)
            if HTTP_EGRESS_CONNECT_URL:
                headers = {"Host": HTTP_EGRESS_REPLAY_HOST}
                if traceparent:
                    headers["traceparent"] = traceparent
                via = "replay" if is_shadow_worker() else "record"
                resp = http_post(
                    HTTP_EGRESS_CONNECT_URL,
                    {"status": "complete", **{k: doc[k] for k in ("order_id", "seq") if k in doc}},
                    headers=headers,
                )
                print(f"http egress via={via} status={resp.status_code}", flush=True)
                if resp.status_code != 200:
                    return jsonify(error=f"http egress status={resp.status_code}"), 500
            publish_egress(amqp_url, doc, traceparent)
            print(
                f"rmq egress published exchange={EGRESS_EXCHANGE} routing_key={EGRESS_ROUTING_KEY}",
                flush=True,
            )
            return jsonify(status="ok")
        except Exception as exc:
            print(f"publish failed: {exc}", flush=True)
            return jsonify(error=str(exc)), 500

    print(
        f"http-rmq-python-worker listen=:{port} amqp={amqp_url} mongo={mongo_url or '<none>'} "
        f"http_egress={HTTP_EGRESS_CONNECT_URL or '<disabled>'} "
        f"egress={EGRESS_EXCHANGE}/{EGRESS_ROUTING_KEY}",
        flush=True,
    )

    def shutdown(_signum, _frame):
        sys.exit(0)

    signal.signal(signal.SIGTERM, shutdown)
    # ponytail: threaded=True drops OTel context in worker threads; e2e is single-request
    app.run(host="0.0.0.0", port=port, threaded=False)


if __name__ == "__main__":
    main()
