#!/usr/bin/env python3
"""Trace-unaware HTTP ingress -> RabbitMQ egress worker for OTel E2E."""

import json
import os
import signal
import sys

import pika
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


def main() -> None:
    listen_addr = env_or("LISTEN_ADDR", ":8080")
    port = int(listen_addr.lstrip(":") or "8080")
    amqp_url = normalize_amqp_url(env_or("AMQP_URL", ""))
    if not amqp_url:
        raise SystemExit("AMQP_URL is required")

    conn = pika.BlockingConnection(pika.URLParameters(amqp_url))
    ch = conn.channel()
    ch.exchange_declare(exchange=EGRESS_EXCHANGE, exchange_type="topic", durable=True)

    app = Flask(__name__)

    @app.get("/healthz")
    def healthz():
        return "ok", 200

    @app.post("/publish")
    def publish():
        body = request.get_json(silent=True) or {}
        payload = {**body, "source": "http-rmq-python-worker"}
        ch.basic_publish(
            exchange=EGRESS_EXCHANGE,
            routing_key=EGRESS_ROUTING_KEY,
            body=json.dumps(payload),
            properties=pika.BasicProperties(
                content_type="application/json",
                delivery_mode=2,
            ),
        )
        print(
            f"rmq egress published exchange={EGRESS_EXCHANGE} routing_key={EGRESS_ROUTING_KEY}",
            flush=True,
        )
        return jsonify(status="ok")

    print(
        f"http-rmq-python-worker listen=:{port} amqp={amqp_url} "
        f"egress={EGRESS_EXCHANGE}/{EGRESS_ROUTING_KEY}",
        flush=True,
    )

    def shutdown(_signum, _frame):
        conn.close()
        sys.exit(0)

    signal.signal(signal.SIGTERM, shutdown)
    app.run(host="0.0.0.0", port=port, threaded=True)


if __name__ == "__main__":
    main()
