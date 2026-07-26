'use strict';

// ponytail: mongodb@4.17 — OTel operator bundles instrumentation-mongodb@0.35, broken with driver 6.x

const http = require('http');
const https = require('https');
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
  return envOr('AMQP_URL', '').includes('candidate') ||
    envOr('MONGO_URL', '').includes('candidate');
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

function isShadowWorker() {
  return envOr('AMQP_URL', '').includes('shadow') ||
    envOr('MONGO_URL', '').includes('shadow');
}

function httpEgressTarget() {
  return {
    url: HTTP_EGRESS_CONNECT_URL,
    headers: { Host: HTTP_EGRESS_REPLAY_HOST },
  };
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function httpRequestNative(url, payload, headers) {
  // undici/fetch forbids setting Host (Fetch spec); use http.request() so the
  // custom Host header actually reaches the wire (it becomes the egress mock key).
  return new Promise((resolve, reject) => {
    const parsed = new URL(url);
    const lib = parsed.protocol === 'https:' ? https : http;
    const body = JSON.stringify(payload);
    const req = lib.request(
      {
        hostname: parsed.hostname,
        port: parsed.port || (parsed.protocol === 'https:' ? 443 : 80),
        path: parsed.pathname + (parsed.search || ''),
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...headers },
      },
      (res) => {
        res.resume();
        resolve({ status: res.statusCode });
      },
    );
    req.on('error', reject);
    req.write(body);
    req.end();
  });
}

async function httpPost(url, payload, headers) {
  const deadline = Date.now() + 60_000;
  while (true) {
    const resp = await httpRequestNative(url, payload, headers);
    if (resp.status !== 599 || Date.now() >= deadline) return resp;
    await sleep(2000);
  }
}

function extractTraceparent(msg) {
  try {
    const h = msg.properties && msg.properties.headers;
    if (h && typeof h.traceparent === 'string') return h.traceparent.trim();
  } catch {
    // ignore
  }
  return null;
}

async function handleMessage(ch, msg, mongoColl) {
  const orderId = parseOrderId(msg.content);
  const traceparent = extractTraceparent(msg);
  console.log(`consumed routing_key=${msg.fields.routingKey} order_id=${orderId}`);

  const mongoOpts = traceparent ? { comment: traceparent } : {};
  await mongoColl.insertOne({ order_id: orderId, status: 'processed' }, mongoOpts);
  console.log('mongo insert ok');

  if (isCandidate()) {
    await mongoColl.insertOne({ order_id: orderId, audit: 'candidate_n1_loop' }, mongoOpts);
    console.log('mongo candidate n+1 insert ok');
  }

  try {
    const { url, headers } = httpEgressTarget();
    if (traceparent) headers['traceparent'] = traceparent;
    const via = isShadowWorker() ? 'replay' : 'record';
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
  const rmqHeaders = traceparent ? { traceparent } : {};
  await ch.publish(EGRESS_EXCHANGE, EGRESS_ROUTING_KEY, Buffer.from(JSON.stringify(shipped)), {
    contentType: 'application/json',
    persistent: true,
    headers: rmqHeaders,
  });
  console.log(`rmq egress published exchange=${EGRESS_EXCHANGE} routing_key=${EGRESS_ROUTING_KEY}`);

  if (isCandidate()) {
    const dup = { ...shipped, extra: 'duplicate' };
    await ch.publish(EGRESS_EXCHANGE, EGRESS_ROUTING_KEY, Buffer.from(JSON.stringify(dup)), {
      contentType: 'application/json',
      persistent: true,
      headers: rmqHeaders,
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

  const conn = await amqp.connect(amqpUrl, { heartbeat: 30 });
  conn.on('error', (err) => { console.error('AMQP connection error:', err); process.exit(1); });
  conn.on('close', () => { console.error('AMQP connection closed, exiting for restart'); process.exit(1); });
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
