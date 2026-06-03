/**
 * Phase 4b.2 — parallel k6 stress test for Igris ingress + Beru health under load.
 *
 * Prerequisites: Kind E2E stack + port-forwards (see tests/k6/README.md).
 */
import http from 'k6/http';
import { check } from 'k6';
import { Rate } from 'k6/metrics';

const beruHealthSuccess = new Rate('beru_health_success');

const targetUrl = (__ENV.TARGET_URL || 'http://127.0.0.1:8888').replace(/\/$/, '');
const orphanTargetUrl = (__ENV.ORPHAN_TARGET_URL || 'http://127.0.0.1:8889').replace(
  /\/$/,
  '',
);
const beruHealthUrl = __ENV.BERU_HEALTH_URL || 'http://127.0.0.1:8080/healthz';
const testPath = __ENV.TEST_PATH || '/post';
const duration = __ENV.DURATION || '2m';
const LARGE_PAYLOAD_MB = parseInt(__ENV.LARGE_PAYLOAD_MB || '5', 10);

function uuidv4() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    const v = c === 'x' ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}

function normalBody() {
  return JSON.stringify({ user: 'alice', price: 10 });
}

function noisyBody() {
  return JSON.stringify({
    user: 'alice',
    price: 10,
    timestamp: new Date().toISOString(),
    uuid: uuidv4(),
  });
}

function buildLargePayloadJson(mb) {
  const targetBytes = mb * 1024 * 1024;
  const overhead = '{"padding":""}'.length;
  const padding = 'x'.repeat(Math.max(0, targetBytes - overhead));
  return JSON.stringify({ padding });
}

// Init context only — single allocation shared by all VUs (avoids OOM).
const largePayloadBody = buildLargePayloadJson(LARGE_PAYLOAD_MB);

export const options = {
  scenarios: {
    steady_state: {
      executor: 'constant-vus',
      vus: 10,
      duration,
      exec: 'steadyState',
    },
    noise_generator: {
      executor: 'constant-vus',
      vus: 5,
      duration,
      exec: 'noiseGenerator',
    },
    large_payload: {
      executor: 'constant-vus',
      vus: 1,
      duration,
      exec: 'largePayload',
    },
    orphaned_traces: {
      executor: 'constant-vus',
      vus: 5,
      duration,
      exec: 'orphanedTraces',
    },
    beru_health: {
      executor: 'constant-arrival-rate',
      rate: 1,
      timeUnit: '5s',
      duration,
      preAllocatedVUs: 1,
      maxVUs: 1,
      exec: 'beruHealthCheck',
    },
  },
  thresholds: {
    beru_health_success: ['rate==1.0'],
  },
};

export default function () {}

export function steadyState() {
  const url = `${targetUrl}${testPath}`;
  const res = http.post(url, normalBody(), {
    headers: {
      'Content-Type': 'application/json',
      'x-shadow-trace-id': `k6-steady-${__VU}-${__ITER}`,
    },
    tags: { scenario: 'steady_state' },
  });
  check(res, {
    'steady_state status 2xx or 202': (r) => r.status === 202 || (r.status >= 200 && r.status < 300),
  });
}

export function noiseGenerator() {
  const url = `${targetUrl}${testPath}`;
  const res = http.post(url, noisyBody(), {
    headers: {
      'Content-Type': 'application/json',
      'x-shadow-trace-id': `k6-noise-${__VU}-${__ITER}`,
    },
    tags: { scenario: 'noise_generator' },
  });
  check(res, {
    'noise_generator accepted': (r) => r.status === 202 || (r.status >= 200 && r.status < 300),
  });
}

export function largePayload() {
  const url = `${targetUrl}${testPath}`;
  const res = http.post(url, largePayloadBody, {
    headers: {
      'Content-Type': 'application/json',
      'x-shadow-trace-id': `k6-large-${__VU}-${__ITER}`,
    },
    tags: { scenario: 'large_payload' },
    responseCallback: http.expectedStatuses(200, 202, 413),
  });
  check(res, {
    'large_payload rejected or accepted': (r) =>
      r.status === 413 || r.status === 202 || (r.status >= 200 && r.status < 300),
  });
}

export function orphanedTraces() {
  const url = `${orphanTargetUrl}${testPath}`;
  const traceId = `k6-orphan-${__VU}-${__ITER}`;
  const res = http.post(url, normalBody(), {
    headers: {
      'Content-Type': 'application/json',
      'x-shadow-trace-id': traceId,
      'X-Stress-Broken-Ancillary': 'chunked, identity',
      'Transfer-Encoding': 'chunked, identity',
    },
    tags: { scenario: 'orphaned_traces' },
  });
  check(res, {
    'orphaned_traces request completed': (r) => r.status > 0,
  });
}

export function beruHealthCheck() {
  const res = http.get(beruHealthUrl, {
    tags: { scenario: 'beru_health' },
  });
  const ok = res.status >= 200 && res.status < 300;
  beruHealthSuccess.add(ok);
  if (!ok) {
    console.error(
      `[CRITICAL] Beru health failed: status=${res.status} url=${beruHealthUrl} error=${res.error}`,
    );
  }
}
