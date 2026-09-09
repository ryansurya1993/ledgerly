// Ledgerly load test -- see docker-compose.loadtest.yml and the root
// README's "Prove it scales yourself" section for how this is run.
//
// Runs as a container on the same Compose network as the stack itself
// (see docker-compose.loadtest.yml), targeting wallet-service by its
// *service name*, not a host-published port. Docker's own embedded DNS
// round-robins a scaled service's name across however many replicas
// are currently running -- that's what actually spreads this load
// across N wallet-service replicas (and, transitively, whichever
// ledger-service replica each of those happens to call), with no load
// balancer needed for this specific purpose. See docker-compose.scale.yml
// for why a real load balancer is still needed for the browser-facing
// frontend, which this sidesteps by not being a browser.
//
// Each iteration creates its own fresh wallet rather than sharing one
// across VUs: this is a throughput/capacity test (many concurrent
// accounts, realistic mixed traffic), a different and complementary
// kind of proof from the frontend's stress-test panel, which already
// covers many concurrent writers racing on the *same* account in
// tightly-controlled, assertion-heavy detail.
import http from 'k6/http';
import { check, sleep } from 'k6';

const BASE_URL = __ENV.WALLET_BASE_URL || 'http://wallet-service:8081';
const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

export const options = {
  scenarios: {
    ledgerly_traffic: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '10s', target: 20 }, // ramp up
        { duration: '30s', target: 20 }, // sustained load
        { duration: '10s', target: 0 },  // ramp down
      ],
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<1000'],
  },
};

// uniqueSuffix leans on k6's own per-VU/per-iteration counters (no
// external uuid library needed, so this has no dependency on the k6
// container being able to reach the internet at run time).
function uniqueSuffix() {
  return `${__VU}-${__ITER}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

export default function () {
  const createRes = http.post(
    `${BASE_URL}/wallets`,
    JSON.stringify({ name: `loadtest-${uniqueSuffix()}` }),
    JSON_HEADERS,
  );
  const created = check(createRes, { 'create wallet: 201': (r) => r.status === 201 });
  if (!created) {
    sleep(0.2);
    return;
  }
  const walletId = createRes.json('id');
  const topUpAmount = 5000;

  const topupRes = http.post(
    `${BASE_URL}/wallets/${walletId}/topup`,
    JSON.stringify({ idempotency_key: `loadtest-topup-${uniqueSuffix()}`, amount: topUpAmount }),
    JSON_HEADERS,
  );
  check(topupRes, { 'topup: 201': (r) => r.status === 201 });

  const balanceRes = http.get(`${BASE_URL}/wallets/${walletId}/balance`);
  check(balanceRes, {
    'balance: 200': (r) => r.status === 200,
    'balance matches the single top-up': (r) => r.json('balance') === topUpAmount,
  });

  sleep(0.2);
}
