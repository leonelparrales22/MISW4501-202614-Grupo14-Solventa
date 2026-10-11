// HA-L01 CA-5 (staging, through the API Gateway): the rate limiting of the
// gateway protects the journey when one partner's load grows, without
// affecting the other consumers.
//
// Two partners at the same time on POST /v1/risk-profiles/resolve:
//   - partner A at RATE_A (200 req/s) over its usage plan (100 req/s, burst 200):
//     part of its requests must get 429;
//   - partner B at RATE_B (50 req/s), inside its plan: 0 % of 429 and p95 <= 400 ms.
//
// Variables: BASE_URL (stage URL), API_KEY_A, API_KEY_B, RATE_A (200),
// RATE_B (50), DURATION (2m).
import http from "k6/http";
import { check } from "k6";
import { Rate } from "k6/metrics";

const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";
const API_KEY_A = __ENV.API_KEY_A || "";
const API_KEY_B = __ENV.API_KEY_B || "";
const RATE_A = parseInt(__ENV.RATE_A || "200", 10);
const RATE_B = parseInt(__ENV.RATE_B || "50", 10);
const DURATION = __ENV.DURATION || "2m";

const throttled = new Rate("throttled");

// 429 is the expected answer of the gateway for partner A over its limit.
const ACCEPT_THROTTLING = http.expectedStatuses(200, 429);

export const options = {
  scenarios: {
    partner_a_over_limit: {
      executor: "constant-arrival-rate",
      rate: RATE_A,
      timeUnit: "1s",
      duration: DURATION,
      preAllocatedVUs: 100,
      maxVUs: 300,
      exec: "partnerA",
      tags: { partner: "a" },
    },
    partner_b_nominal: {
      executor: "constant-arrival-rate",
      rate: RATE_B,
      timeUnit: "1s",
      duration: DURATION,
      preAllocatedVUs: 30,
      maxVUs: 100,
      exec: "partnerB",
      tags: { partner: "b" },
    },
  },
  thresholds: {
    "throttled{partner:a}": ["rate>0"], // the limit is enforced on A
    "throttled{partner:b}": ["rate==0"], // B is never throttled
    "http_req_duration{partner:b}": ["p(95)<400"], // B keeps the journey p95 (CA-1)
    "http_req_failed{partner:b}": ["rate<0.01"],
  },
  summaryTrendStats: ["med", "p(95)", "p(99)", "max"],
  tags: { story: "HA-L01", criterion: "CA-5" },
};

function quote(apiKey, partner, params) {
  const res = http.post(
    `${BASE_URL}/v1/risk-profiles/resolve`,
    JSON.stringify({ customer_id: `rl-${partner}-${__VU}-${__ITER}`, consent_active: true }),
    Object.assign({ headers: { "Content-Type": "application/json", "x-api-key": apiKey } }, params),
  );
  throttled.add(res.status === 429);
  return res;
}

export function partnerA() {
  const res = quote(API_KEY_A, "a", { responseCallback: ACCEPT_THROTTLING });
  check(res, { "partner A gets 200 or 429": (r) => r.status === 200 || r.status === 429 });
}

export function partnerB() {
  const res = quote(API_KEY_B, "b", {});
  check(res, { "partner B gets 200": (r) => r.status === 200 });
}

export function handleSummary(data) {
  const m = data.metrics;
  const metric = (name) => m[name];
  const pct = (x) => (x ? `${(x.values.rate * 100).toFixed(1)} %` : "n/a");
  const p95 = (x) => (x ? `${x.values["p(95)"].toFixed(0)} ms` : "n/a");
  return {
    stdout: [
      "",
      `HA-L01 CA-5 rate limiting (A at ${RATE_A} req/s, B at ${RATE_B} req/s, ${DURATION})`,
      `  requests: ${m.http_reqs ? m.http_reqs.values.count : 0}`,
      `  throttled (429): A ${pct(metric("throttled{partner:a}"))} | B ${pct(metric("throttled{partner:b}"))}`,
      `  partner B p95: ${p95(metric("http_req_duration{partner:b}"))}`,
      "",
    ].join("\n"),
  };
}
