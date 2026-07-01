'use strict';

// ponytail: mongodb@4.17 — OTel operator bundles instrumentation-mongodb@0.35, broken with driver 6.x

const amqp = require('amqplib');
const { MongoClient } = require('mongodb');

const EGRESS_EXCHANGE = (process.env.RMQ_EGRESS_EXCHANGE || 'egress-events').trim();
const EGRESS_ROUTING_KEY = (process.env.RMQ_EGRESS_ROUTING_KEY || 'order.shipped').trim();
const HTTP_EGRESS_REPLAY_HOST = (process.env.HTTP_EGRESS_REPLAY_HOST || 'user-service.prod.internal').trim();
const HTTP_EGRESS_CONNECT_URL = (
  process.env.HTTP_EGRESS_CONNECT_URL || 'http://user-service.prod.svc.cluster.local:8080/v1/log'
).trim();
const HTTP_EGRESS_PATH = (process.env.HTTP_EGRESS_PATH || '/v1/log').trim();

function envOr(name, fallback) {
  const value = (process.env[name] || '').trim();
  return value || fallback;
}

function normalizeAmqpUrl(raw) {
  const trimmed = (raw || '').trim();
  if (!trimmed) {
    return '';
  }
  if (trimmed.startsWith('amqp://') || trimmed.startsWith('amqps://')) {
    return trimmed;
  }
  return `amqp://guest:guest@${trimmed.replace(/^\/\//, '')}/`;
}

function isCandidate() {
  return envOr('OTEL_SERVICE_NAME', '').includes('candidate');
}

function parseOrderId(body) {
  try {
    const data = JSON.parse(body.toString('utf8'));
    if (data && typeof data === 'object' && data.order_id) {
      return String(data.order_id);
    }
  } catch {
    // ponytail: malformed body → unknown
  }
  return 'unknown';
}

function usesEgressProxy() {
  return Boolean(process.env.HTTP_PROXY || process.env.http_proxy);
}

function httpEgressTarget() {
  if (usesEgressProxy()) {
    let port = '8080';
    try {
      const parsed = new URL(HTTP_EGRESS_CONNECT_URL);
      if (parsed.port) {
        port = parsed.port;
      }
    } catch {
      // keep default port
    }
    return {
      url: `http://${HTTP_EGRESS_REPLAY_HOST}:${port}${HTTP_EGRESS_PATH}`,
      headers: {},
    };
  }
  return {
    url: HTTP_EGRESS_CONNECT_URL,
    headers: { Host: HTTP_EGRESS_REPLAY_HOST },
  };
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function httpPost(url, payload, headers) {
  const proxy = process.env.HTTP_PROXY || process.env.http_proxy;
  const body = JSON.stringify(payload);
  const deadline = Date.now() + 60_000;
  const fetchOpts = {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...headers },
    body,
  };
  const doFetch = proxy
    ? (() => {
        const { fetch, ProxyAgent } = require('undici');
        fetchOpts.dispatcher = new ProxyAgent(proxy);
        return fetch;
      })()
    : fetch;
  while (true) {
    const resp = await doFetch(url, fetchOpts);
    if (resp.status !== 599 || Date.now() >= deadline) {
      return resp;
    }
    await sleep(2000);
  }
}

async function handleMessage(ch, msg, mongoColl) {
  const orderId = parseOrderId(msg.content);
  console.log(`consumed routing_key=${msg.fields.routingKey} order_id=${orderId}`);

  await mongoColl.insertOne({ order_id: orderId, status: 'processed' });
  console.log('mongo insert ok');

  if (isCandidate()) {
    await mongoColl.insertOne({ order_id: orderId, audit: 'candidate_n1_loop' });
    console.log('mongo candidate n+1 insert ok');
  }

  try {
    const { url, headers } = httpEgressTarget();
    const via = usesEgressProxy() ? 'replay' : 'record';
    const resp = await httpPost(url, { status: 'complete', order_id: orderId }, headers);
    console.log(`http egress via=${via} status=${resp.status}`);
    if (resp.status !== 200) {
      ch.nack(msg, false, true);
      return;
    }
  } catch (err) {
    console.error(`http egress failed: ${err}`);
    ch.nack(msg, false, true);
    return;
  }

  const shipped = { order_id: orderId, status: 'shipped' };
  await ch.publish(EGRESS_EXCHANGE, EGRESS_ROUTING_KEY, Buffer.from(JSON.stringify(shipped)), {
    contentType: 'application/json',
    persistent: true,
  });
  console.log(`rmq egress published exchange=${EGRESS_EXCHANGE} routing_key=${EGRESS_ROUTING_KEY}`);

  if (isCandidate()) {
    const dup = { ...shipped, extra: 'duplicate' };
    await ch.publish(EGRESS_EXCHANGE, EGRESS_ROUTING_KEY, Buffer.from(JSON.stringify(dup)), {
      contentType: 'application/json',
      persistent: true,
    });
    console.log('rmq candidate n+1 publish ok');
  }

  ch.ack(msg);
}

async function main() {
  const amqpUrl = normalizeAmqpUrl(envOr('AMQP_URL', ''));
  if (!amqpUrl) {
    throw new Error('AMQP_URL is required');
  }

  const mongoUrl = envOr('MONGO_URL', 'mongodb://127.0.0.1:27017');
  const mongoDb = envOr('MONGO_DB', 'test');
  const exchange = envOr('AMQP_EXCHANGE', 'orders');
  const queue = envOr('AMQP_QUEUE', 'orders');
  const bindingKey = envOr('AMQP_BINDING_KEY', 'order.created');

  const mongoClient = new MongoClient(mongoUrl);
  await mongoClient.connect();
  const mongoColl = mongoClient.db(mongoDb).collection('orders');

  const conn = await amqp.connect(amqpUrl);
  const ch = await conn.createChannel();
  await ch.assertExchange(exchange, 'topic', { durable: true });
  await ch.assertQueue(queue, { durable: true });
  await ch.bindQueue(queue, exchange, bindingKey);
  await ch.assertExchange(EGRESS_EXCHANGE, 'topic', { durable: true });
  await ch.prefetch(1);

  console.log(
    `nodejs-hybrid-worker amqp=${amqpUrl} mongo=${mongoUrl} exchange=${exchange} queue=${queue} egress=${EGRESS_EXCHANGE}`,
  );

  await ch.consume(queue, (msg) => {
    if (!msg) {
      return;
    }
    handleMessage(ch, msg, mongoColl).catch((err) => {
      console.error(`consume handler failed: ${err}`);
      ch.nack(msg, false, true);
    });
  });

  process.on('SIGTERM', async () => {
    await ch.close();
    await conn.close();
    await mongoClient.close();
    process.exit(0);
  });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
