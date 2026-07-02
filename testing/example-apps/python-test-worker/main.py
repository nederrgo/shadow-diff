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
# recordAndReplay host (NOT *.svc.cluster.local — that bypasses HTTP_PROXY via NO_PROXY).
HTTP_EGRESS_REPLAY_HOST = os.environ.get(
    "HTTP_EGRESS_REPLAY_HOST", "user-service.prod.internal"
)
# Prod-only: dial real user-service; Host header is HTTP_EGRESS_REPLAY_HOST for Siphon/Beru hash.
HTTP_EGRESS_CONNECT_URL = os.environ.get(
    "HTTP_EGRESS_CONNECT_URL",
    "http://user-service.prod.svc.cluster.local:8080/v1/log",
)
HTTP_EGRESS_PATH = os.environ.get("HTTP_EGRESS_PATH", "/v1/log")


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
    # OTEL_SERVICE_NAME was set by OTel auto-injection (now removed). Fall back to
    # AMQP_URL which contains the shadow role name (rabbitmq-candidate / rabbitmq-control-a/b).
    for key in ("OTEL_SERVICE_NAME", "AMQP_URL", "HOSTNAME"):
        if "candidate" in env_or(key, ""):
            return True
    return False



def parse_order_id(body: bytes) -> str:
    try:
        data = json.loads(body.decode("utf-8"))
        if isinstance(data, dict) and data.get("order_id"):
            return str(data["order_id"])
    except (json.JSONDecodeError, UnicodeDecodeError):
        pass
    return "unknown"


def uses_egress_proxy() -> bool:
    return bool(os.environ.get("HTTP_PROXY") or os.environ.get("http_proxy"))


def http_egress_target() -> tuple[str, dict[str, str]]:
    """Shadow: URL host must avoid NO_PROXY so traffic hits Envoy/Beru. Prod: direct + Host."""
    if uses_egress_proxy():
        port = "8080"
        if "://" in HTTP_EGRESS_CONNECT_URL:
            from urllib.parse import urlparse

            parsed = urlparse(HTTP_EGRESS_CONNECT_URL)
            if parsed.port:
                port = str(parsed.port)
        url = f"http://{HTTP_EGRESS_REPLAY_HOST}:{port}{HTTP_EGRESS_PATH}"
        return url, {}
    return HTTP_EGRESS_CONNECT_URL, {"Host": HTTP_EGRESS_REPLAY_HOST}


def http_post(
    url: str, payload: dict, headers: dict | None = None, timeout: int = 30
) -> requests.Response:
    """POST via HTTP_PROXY on shadow; retry 599 while prod egress is recorded for replay."""
    headers = headers or {}
    deadline = time.monotonic() + 60
    while True:
        resp = requests.post(url, json=payload, headers=headers, timeout=timeout)
        if resp.status_code != 599 or time.monotonic() >= deadline:
            return resp
        time.sleep(2)


def handle_message(ch, method, properties, body, mongo_coll):
    order_id = parse_order_id(body)
    print(f"consumed routing_key={method.routing_key} order_id={order_id}", flush=True)

    # Propagate W3C traceparent from AMQP headers to HTTP egress so the mock
    # store lookup (keyed by trace ID) works in the shadow stack.
    traceparent = (properties.headers or {}).get("traceparent") if properties else None

    # Pass traceparent as MongoDB comment so Pixie eBPF captures the trace ID
    # in the raw wire bytes for Beru's MongoDB egress correlation.
    mongo_kwargs = {"comment": traceparent} if traceparent else {}
    mongo_coll.insert_one({"order_id": order_id, "status": "processed"}, **mongo_kwargs)
    print("mongo insert ok", flush=True)

    if is_candidate():
        mongo_coll.insert_one({"order_id": order_id, "audit": "candidate_n1_loop"}, **mongo_kwargs)
        print("mongo candidate n+1 insert ok", flush=True)

    try:
        url, headers = http_egress_target()
        if traceparent:
            headers["traceparent"] = traceparent
        via = "replay" if uses_egress_proxy() else "record"
        resp = http_post(
            url,
            {"status": "complete", "order_id": order_id},
            headers=headers,
        )
        print(f"http egress via={via} status={resp.status_code}", flush=True)
        if resp.status_code != 200:
            ch.basic_nack(method.delivery_tag, requeue=True)
            return
    except requests.RequestException as exc:
        print(f"http egress failed: {exc}", flush=True)
        ch.basic_nack(method.delivery_tag, requeue=True)
        return

    shipped = {"order_id": order_id, "status": "shipped"}
    rmq_headers = {"traceparent": traceparent} if traceparent else {}
    props = pika.BasicProperties(
        content_type="application/json",
        delivery_mode=2,
        headers=rmq_headers,
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
                content_type="application/json",
                delivery_mode=2,
                headers=rmq_headers,
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

    ch.basic_consume(
        queue=queue,
        on_message_callback=lambda channel, method, properties, body: handle_message(
            channel, method, properties, body, mongo_coll
        ),
    )

    def shutdown(_signum, _frame):
        conn.close()
        sys.exit(0)

    signal.signal(signal.SIGTERM, shutdown)
    ch.start_consuming()


if __name__ == "__main__":
    main()
