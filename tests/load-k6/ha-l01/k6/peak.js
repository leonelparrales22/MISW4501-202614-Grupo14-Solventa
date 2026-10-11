// HA-L01 peak load: the quoting journey (risk profile resolution with the
// three external sources consulted in parallel) under the ramp of EXP-L01,
// 8 -> 83 req/s (500 -> 5 000 quotes/min) in RAMP_UP, then held for SUSTAIN.
//
// Locally it is a smoke test: latency is only informative and the run fails
// when a request is lost or an answer is malformed. With STRICT_LATENCY=1
// (staging, CA-1) it also enforces p95 <= 400 ms and p99 <= 800 ms.
//
// Variables: BASE_URL (the service locally, the API Gateway stage in staging),
// API_KEY (partner key, sent as x-api-key when set), RPS_MAX (83), RAMP_UP
// (1m), SUSTAIN (3m), CACHE_HIT (0.5, share of requests for a repeated
// customer), PROFILE and OPEN_FINANCE_REQUIRED (labels for the summary),
// STRICT_LATENCY.
import http from "k6/http";
import { check } from "k6";
import { Rate } from "k6/metrics";

const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";
const API_KEY = __ENV.API_KEY || "";
const RPS_MAX = parseInt(__ENV.RPS_MAX || "83", 10);
const RAMP_UP = __ENV.RAMP_UP || "1m";
const SUSTAIN = __ENV.SUSTAIN || "3m";
const CACHE_HIT = parseFloat(__ENV.CACHE_HIT || "0.5");
const PROFILE = __ENV.PROFILE || "unlabeled";
const OPEN_FINANCE_REQUIRED = __ENV.OPEN_FINANCE_REQUIRED || "?";
const STRICT_LATENCY = __ENV.STRICT_LATENCY === "1";
const WARM_POOL = 200;
const HEADERS = API_KEY
  ? { "Content-Type": "application/json", "x-api-key": API_KEY }
  : { "Content-Type": "application/json" };

const provisional = new Rate("provisional");
const partial = new Rate("partial");
const missing = {
  open_finance: new Rate("missing_open_finance"),
  open_data: new Rate("missing_open_data"),
  kyc: new Rate("missing_kyc"),
};

const thresholds = STRICT_LATENCY
  ? { http_req_duration: ["p(95)<400", "p(99)<800"], http_req_failed: ["rate<0.01"], checks: ["rate>0.99"] }
  : { http_req_failed: ["rate==0"], checks: ["rate==1"] }; // every request answered, every answer valid

export const options = {
  scenarios: {
    peak: {
      executor: "ramping-arrival-rate",
      startRate: 8,
      timeUnit: "1s",
      preAllocatedVUs: 100,
      maxVUs: 400,
      stages: [
        { duration: RAMP_UP, target: RPS_MAX },
        { duration: SUSTAIN, target: RPS_MAX },
      ],
    },
  },
  thresholds,
  summaryTrendStats: ["avg", "med", "p(90)", "p(95)", "p(99)", "max"],
  tags: { story: "HA-L01", profile: PROFILE, open_finance_required: OPEN_FINANCE_REQUIRED },
};

function customerId() {
  if (Math.random() < CACHE_HIT) {
    return `warm-${Math.floor(Math.random() * WARM_POOL)}`;
  }
  return `cold-${__VU}-${__ITER}-${Date.now()}`;
}

export default function () {
  const res = http.post(
    `${BASE_URL}/v1/risk-profiles/resolve`,
    JSON.stringify({ customer_id: customerId(), consent_active: true }),
    { headers: HEADERS },
  );

  let body = {};
  try {
    body = res.json();
  } catch (_) {
    // the checks below fail when the body is not JSON
  }
  const sources = Array.isArray(body.sources) ? body.sources : [];

  check(res, {
    "status is 200": (r) => r.status === 200,
    "mode is normal or provisional": () => body.mode === "normal" || body.mode === "provisional",
    "sources is an array": () => Array.isArray(body.sources),
    "normal answers come from the sources": () =>
      body.mode !== "normal" || body.source === "external_sources" || body.source === "open_finance",
    "provisional answers name the missing source": () =>
      body.mode !== "provisional" || body.unavailable_source === "open_finance",
  });

  provisional.add(body.mode === "provisional");
  partial.add(body.partial === true);
  for (const name of Object.keys(missing)) {
    const state = sources.find((s) => s.name === name);
    if (state) {
      missing[name].add(state.status !== "ok");
    }
  }
}

function pct(metric) {
  return metric ? `${(metric.values.rate * 100).toFixed(1)} %` : "n/a";
}

function ms(metric, stat) {
  return metric ? `${metric.values[stat].toFixed(0)} ms` : "n/a";
}

export function handleSummary(data) {
  const m = data.metrics;
  const lines = [
    "",
    `HA-L01 peak (profile=${PROFILE}, OPEN_FINANCE_REQUIRED=${OPEN_FINANCE_REQUIRED}, strict latency=${STRICT_LATENCY})`,
    `  requests: ${m.http_reqs ? m.http_reqs.values.count : 0}   failed: ${pct(m.http_req_failed)}   checks ok: ${pct(m.checks)}`,
    `  provisional: ${pct(m.provisional)}   partial: ${pct(m.partial)}`,
    `  missing per source: open_finance ${pct(m.missing_open_finance)} | open_data ${pct(m.missing_open_data)} | kyc ${pct(m.missing_kyc)}`,
    `  latency${STRICT_LATENCY ? "" : " (informative, local)"}: med ${ms(m.http_req_duration, "med")} | p95 ${ms(m.http_req_duration, "p(95)")} | p99 ${ms(m.http_req_duration, "p(99)")} | max ${ms(m.http_req_duration, "max")}`,
    "",
  ];
  return { stdout: lines.join("\n") };
}
