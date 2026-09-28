// k6 load test for POST /transactions. Run it through docker-compose.loadtest.yml
// (see the README's Performance section) so it reaches every API replica.
//
// Settings: VUS (concurrent virtual users, default 50), DURATION (default 30s),
// BASE_URL (default http://api:8080).
import http from 'k6/http';
import { check } from 'k6';

export const options = {
  vus: Number(__ENV.VUS || 50),
  duration: __ENV.DURATION || '30s',
  summaryTrendStats: ['med', 'p(95)', 'p(99)', 'max'],
  thresholds: {
    http_req_failed: ['rate<0.01'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://api:8080';
const HEADERS = { 'Content-Type': 'application/json' };

export default function () {
  // A new account per iteration: each starts with the mock's 10,000.00 balance,
  // so every DEBIT is approved and the test measures the full execution path.
  const body = JSON.stringify({
    accountId: `load-${__VU}-${__ITER}`,
    type: 'DEBIT',
    amount: 10.5,
    currency: 'MXN',
  });
  const response = http.post(`${BASE_URL}/transactions`, body, { headers: HEADERS });
  check(response, { 'status is 201': (r) => r.status === 201 });
}
