'use strict';

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
  const amqpUrl = normalizeAmqpUrl(process.env.AMQP_URL);
  if (!amqpUrl) {
    throw new Error('AMQP_URL is required');
  }

  const exchange = envOr('AMQP_EXCHANGE', 'orders');
  const queue = envOr('AMQP_QUEUE', 'worker-queue');
  const bindingKey = envOr('AMQP_BINDING_KEY', 'order.created');
  const egressExchange = (process.env.RMQ_EGRESS_EXCHANGE || '').trim();
  const egressRoutingKey = envOr('RMQ_EGRESS_ROUTING_KEY', 'order.egress');

  const conn = await amqp.connect(amqpUrl);
  const ch = await conn.createChannel();

  await ch.assertExchange(exchange, 'topic', { durable: true });
  await ch.assertQueue(queue, { durable: true });
  await ch.bindQueue(queue, exchange, bindingKey);

  if (egressExchange) {
    await ch.assertExchange(egressExchange, 'topic', { durable: true });
  }

  console.log(
    `nodejs-test-worker amqp=${amqpUrl} exchange=${exchange} queue=${queue} egress_exchange=${egressExchange || '<none>'}`,
  );

  await ch.consume(queue, async (msg) => {
    if (!msg) {
      return;
    }

    console.log(`consumed routing_key=${msg.fields.routingKey}`);

    try {
      if (egressExchange) {
        const body = JSON.stringify({ event: 'egress', source: 'nodejs-test-worker' });
        await ch.publish(egressExchange, egressRoutingKey, Buffer.from(body), {
          contentType: 'application/json',
          persistent: true,
        });
      }
      ch.ack(msg);
    } catch (err) {
      console.error(`consume handler failed: ${err}`);
      ch.nack(msg, false, true);
    }
  });

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
