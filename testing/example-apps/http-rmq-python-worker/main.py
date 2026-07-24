#!/usr/bin/env python3
"""Trace-unaware HTTP ingress -> Mongo + RabbitMQ egress worker for OTel E2E."""

import json
import os
import signal
import sys

import pika
import pymongo
from flask import Flask, jsonify, request

EGRESS_EXCHANGE = os.environ.get("RMQ_EGRESS_EXCHANGE", "egress-events")
EGRESS_ROUTING_KEY = os.environ.get("RMQ_EGRESS_ROUTING_KEY", "order.egress")


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
