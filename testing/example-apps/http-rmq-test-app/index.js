'use strict';

const express = require('express');
const amqp = require('amqplib');

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

async function main() {
  const listenAddr = envOr('LISTEN_ADDR', ':8080');
  const port = listenAddr.startsWith(':') ? listenAddr.slice(1) : listenAddr;
  const amqpUrl = normalizeAmqpUrl(envOr('AMQP_URL', ''));
  if (!amqpUrl) {
    throw new Error('AMQP_URL is required');
  }
  const egressExchange = envOr('RMQ_EGRESS_EXCHANGE', 'egress-events');
  const egressRoutingKey = envOr('RMQ_EGRESS_ROUTING_KEY', 'order.egress');

  const conn = await amqp.connect(amqpUrl);
  const ch = await conn.createChannel();
  await ch.assertExchange(egressExchange, 'topic', { durable: true });

  const app = express();
  app.use(express.json());

  app.get('/healthz', (_req, res) => {
    res.status(200).send('ok');
  });

  app.post('/publish', async (req, res) => {
    try {
      const body = req.body && typeof req.body === 'object' ? req.body : {};
      const payload = Buffer.from(JSON.stringify({ ...body, source: 'http-rmq-test-app' }));
      await ch.publish(egressExchange, egressRoutingKey, payload, {
        contentType: 'application/json',
        persistent: true,
      });
      console.log(`rmq egress published exchange=${egressExchange} routing_key=${egressRoutingKey}`);
      res.status(200).json({ status: 'ok' });
    } catch (err) {
      console.error(`rmq egress publish failed: ${err}`);
      res.status(500).json({ error: String(err) });
    }
  });

  console.log(
    `http-rmq-test-app listening on :${port} amqp=${amqpUrl} egress=${egressExchange}/${egressRoutingKey}`,
  );
  app.listen(Number(port));

  process.on('SIGTERM', async () => {
    await ch.close();
    await conn.close();
    process.exit(0);
  });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
