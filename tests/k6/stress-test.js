/**
 * Phase 4b.2 — parallel k6 stress test for Igris ingress + Beru health under load.
 *
 * Payload limits: Igris default 512KiB (IGRIS_MAX_BODY_SIZE). limit_payload uses 450KB
 * (under Envoy ~1MiB buffered response with echo). large_payload uses 1MB (Igris 413).
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
// Igris default IGRIS_MAX_BODY_SIZE (512 * 1024). Stay below Envoy ext_proc ~1MiB response buffer.
const IGRIS_MAX_BODY_BYTES = parseInt(__ENV.IGRIS_MAX_BODY_BYTES || String(512 * 1024), 10);
const LIMIT_PAYLOAD_KB = parseInt(__ENV.LIMIT_PAYLOAD_KB || '450', 10);
const LARGE_PAYLOAD_MB = parseInt(__ENV.LARGE_PAYLOAD_MB || '1', 10);

function buildPaddedPayloadJson(targetBytes) {
  const prefix = '{"padding":"';
  const suffix = '"}';
  const paddingLen = targetBytes - prefix.length - suffix.length;
  if (paddingLen < 0) {
    throw new Error(`targetBytes ${targetBytes} too small for JSON wrapper`);
  }
  const padding = 'x'.repeat(paddingLen);
  const body = prefix + padding + suffix;
  if (body.length !== targetBytes) {
    throw new Error(`payload size mismatch: got ${body.length}, want ${targetBytes}`);
  }
  return body;
}

function buildPaddedPayloadKb(kb) {
  return buildPaddedPayloadJson(kb * 1024);
}

function buildPaddedPayloadMb(mb) {
  return buildPaddedPayloadJson(mb * 1024 * 1024);
}

// Init context only — single allocation shared by all VUs (avoids OOM).
const limitPayloadBody = buildPaddedPayloadKb(LIMIT_PAYLOAD_KB);
const largePayloadBody = buildPaddedPayloadMb(LARGE_PAYLOAD_MB);

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

const allScenarios = {
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
  limit_payload: {
    executor: 'constant-arrival-rate',
    rate: 1,
    timeUnit: '10s',
    duration,
    preAllocatedVUs: 1,
    maxVUs: 1,
    exec: 'limitPayload',
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
};

function pickScenarios(all, filterCsv) {
  const names = (filterCsv || '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
  if (names.length === 0) {
    return all;
  }
  const picked = {};
  for (const name of names) {
    if (!all[name]) {
      throw new Error(
        `unknown scenario "${name}" in SCENARIOS; valid: ${Object.keys(all).join(', ')}`,
      );
    }
    picked[name] = all[name];
  }
  return picked;
}

export const options = {
  scenarios: pickScenarios(allScenarios, __ENV.SCENARIOS),
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
    responseCallback: http.expectedStatuses(413),
  });
  check(res, {
    'large_payload rejected by Igris with 413': (r) => r.status === 413,
  });
  if (res.status !== 413) {
    console.error(
      `[CRITICAL] large_payload expected 413 from Igris, got ${res.status} (body ${largePayloadBody.length} bytes, limit ${IGRIS_MAX_BODY_BYTES})`,
    );
  }
}

export function limitPayload() {
  const url = `${targetUrl}${testPath}`;
  const res = http.post(url, limitPayloadBody, {
    headers: {
      'Content-Type': 'application/json',
      'x-shadow-trace-id': `k6-limit-${__VU}-${__ITER}`,
    },
    tags: { scenario: 'limit_payload' },
    timeout: '120s',
  });
  check(res, {
    'limit_payload accepted by Igris (202)': (r) => r.status === 202,
    'limit_payload not rejected with 413': (r) => r.status !== 413,
  });
  if (res.status === 413) {
    console.error(
      `[CRITICAL] limit_payload got 413 at ${limitPayloadBody.length} bytes (limit ${IGRIS_MAX_BODY_BYTES}, configured ${LIMIT_PAYLOAD_KB}KB)`,
    );
  }
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
