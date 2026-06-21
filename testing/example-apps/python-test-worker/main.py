#!/usr/bin/env python3
"""Trace-unaware worker: AMQP consume -> Mongo + HTTP + RMQ egress."""

import json
import os
import signal
import sys
import time

import pika
import pymongo
import requests

EGRESS_EXCHANGE = os.environ.get("RMQ_EGRESS_EXCHANGE", "egress-events")
EGRESS_ROUTING_KEY = os.environ.get("RMQ_EGRESS_ROUTING_KEY", "order.shipped")
HTTP_EGRESS_URL = os.environ.get(
    "HTTP_EGRESS_URL",
    "http://user-service.prod.svc.cluster.local:8080/v1/log",
)


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


def is_candidate() -> bool:
    return "candidate" in env_or("OTEL_SERVICE_NAME", "")


def carrier_from_amqp_headers(headers) -> dict:
    out: dict = {}
    if not headers:
        return out
    for key, val in headers.items():
        if isinstance(val, bytes):
            out[key] = val.decode("utf-8", errors="replace")
        elif val is not None:
            out[key] = str(val)
    return out


def attach_inbound_trace(properties):
    try:
        from opentelemetry import propagate
        from opentelemetry.context import attach

        carrier = carrier_from_amqp_headers(
            properties.headers if properties is not None else None
        )
        if not carrier:
            return None
        return attach(propagate.extract(carrier))
    except ImportError:
        return None


def detach_trace(token) -> None:
    if token is None:
        return
    try:
        from opentelemetry.context import detach

        detach(token)
    except ImportError:
        return


def trace_headers(properties=None) -> dict:
    headers: dict = {}
    try:
        from opentelemetry import propagate

        propagate.inject(headers)
    except ImportError:
        pass
    if not headers.get("traceparent") and properties is not None:
        inbound = carrier_from_amqp_headers(properties.headers)
        tp = inbound.get("traceparent")
        if tp:
            headers["traceparent"] = tp
    return headers


def parse_order_id(body: bytes) -> str:
    try:
        data = json.loads(body.decode("utf-8"))
        if isinstance(data, dict) and data.get("order_id"):
            return str(data["order_id"])
    except (json.JSONDecodeError, UnicodeDecodeError):
        pass
    return "unknown"


def http_post(url: str, payload: dict, timeout: int = 30) -> requests.Response:
    """POST via HTTP_PROXY; retry 599 while prod egress is being recorded for replay."""
    deadline = time.monotonic() + 60
    while True:
        resp = requests.post(url, json=payload, timeout=timeout)
        if resp.status_code != 599 or time.monotonic() >= deadline:
            return resp
        time.sleep(2)


def handle_message(ch, method, properties, body, mongo_coll):
    order_id = parse_order_id(body)
    print(f"consumed routing_key={method.routing_key} order_id={order_id}", flush=True)

    mongo_coll.insert_one({"order_id": order_id, "status": "processed"})
    print("mongo insert ok", flush=True)

    if is_candidate():
        mongo_coll.insert_one({"order_id": order_id, "audit": "candidate_n1_loop"})
        print("mongo candidate n+1 insert ok", flush=True)

    try:
        resp = http_post(
            HTTP_EGRESS_URL,
            {"status": "complete", "order_id": order_id},
        )
        print(f"http egress status={resp.status_code}", flush=True)
        if resp.status_code != 200:
            ch.basic_nack(method.delivery_tag, requeue=True)
            return
    except requests.RequestException as exc:
        print(f"http egress failed: {exc}", flush=True)
        ch.basic_nack(method.delivery_tag, requeue=True)
        return

    shipped = {"order_id": order_id, "status": "shipped"}
    props = pika.BasicProperties(
        headers=trace_headers(properties),
        content_type="application/json",
        delivery_mode=2,
    )
    ch.basic_publish(
        exchange=EGRESS_EXCHANGE,
        routing_key=EGRESS_ROUTING_KEY,
        body=json.dumps(shipped),
        properties=props,
    )
    print(f"rmq egress published exchange={EGRESS_EXCHANGE} routing_key={EGRESS_ROUTING_KEY}", flush=True)

    if is_candidate():
        dup = {**shipped, "extra": "duplicate"}
        ch.basic_publish(
            exchange=EGRESS_EXCHANGE,
            routing_key=EGRESS_ROUTING_KEY,
            body=json.dumps(dup),
            properties=pika.BasicProperties(
                headers=trace_headers(properties),
                content_type="application/json",
                delivery_mode=2,
            ),
        )
        print("rmq candidate n+1 publish ok", flush=True)

    ch.basic_ack(method.delivery_tag)


def main() -> None:
    amqp_url = normalize_amqp_url(env_or("AMQP_URL", ""))
    if not amqp_url:
        raise SystemExit("AMQP_URL is required")

    mongo_url = env_or("MONGO_URL", "mongodb://127.0.0.1:27017")
    mongo_db = env_or("MONGO_DB", "test")
    exchange = env_or("AMQP_EXCHANGE", "orders")
    queue = env_or("AMQP_QUEUE", "orders")
    binding_key = env_or("AMQP_BINDING_KEY", "order.created")

    client = pymongo.MongoClient(mongo_url)
    mongo_coll = client[mongo_db]["orders"]

    conn = pika.BlockingConnection(pika.URLParameters(amqp_url))
    ch = conn.channel()
    ch.exchange_declare(exchange=exchange, exchange_type="topic", durable=True)
    ch.queue_declare(queue=queue, durable=True)
    ch.queue_bind(queue=queue, exchange=exchange, routing_key=binding_key)
    ch.exchange_declare(exchange=EGRESS_EXCHANGE, exchange_type="topic", durable=True)
    ch.basic_qos(prefetch_count=1)

    print(
        f"python-test-worker amqp={amqp_url} mongo={mongo_url} "
        f"exchange={exchange} queue={queue} egress={EGRESS_EXCHANGE}",
        flush=True,
    )

    def on_message(channel, method, properties, body):
        token = attach_inbound_trace(properties)
        try:
            handle_message(channel, method, properties, body, mongo_coll)
        finally:
            detach_trace(token)

    ch.basic_consume(queue=queue, on_message_callback=on_message)

    def shutdown(_signum, _frame):
        conn.close()
        sys.exit(0)

    signal.signal(signal.SIGTERM, shutdown)
    ch.start_consuming()


if __name__ == "__main__":
    main()
