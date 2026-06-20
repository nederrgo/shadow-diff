'use strict';

// ponytail: mongodb@4.17 — OTel operator bundles instrumentation-mongodb@0.35, broken with driver 6.x

const express = require('express');
const { MongoClient } = require('mongodb');

function envOr(name, fallback) {
  const value = (process.env[name] || '').trim();
  return value || fallback;
}

async function main() {
  const listenAddr = envOr('LISTEN_ADDR', ':8080');
  const port = listenAddr.startsWith(':') ? listenAddr.slice(1) : listenAddr;
  const mongoURL = envOr('MONGO_URL', 'mongodb://127.0.0.1:27017');
  const collectionName = envOr('MONGO_COLLECTION', 'test.items');

  const client = new MongoClient(mongoURL);
  await client.connect();
  const collection = client.db().collection(collectionName);

  const app = express();
  app.use(express.json());

  app.get('/healthz', (_req, res) => {
    res.status(200).send('ok');
  });

  app.post('/write', async (req, res) => {
    try {
      const doc = req.body && typeof req.body === 'object' ? { ...req.body } : {};
      doc.ts = new Date().toISOString();
      await collection.insertOne(doc);
      console.log('mongo insert ok');
      res.status(200).json({ status: 'ok' });
    } catch (err) {
      console.error(`mongo insert failed: ${err}`);
      res.status(500).json({ error: String(err) });
    }
  });

  console.log(`mongo-test-app listening on :${port} mongo=${mongoURL} collection=${collectionName}`);
  app.listen(Number(port));

  process.on('SIGTERM', async () => {
    await client.close();
    process.exit(0);
  });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
