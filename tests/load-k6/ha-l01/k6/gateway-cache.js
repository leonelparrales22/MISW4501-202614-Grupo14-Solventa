// HA-L01 CA-4 (staging, through the API Gateway): a cacheable answer is served
// from the gateway cache without recalculating, and partners never share
// cache entries.
//
// For each customer: partner A reads it twice and partner B once, with
// GET /v1/risk-profiles/{customer_id}?consent_active=true.
//   - A's second answer must be byte-identical to the first one: a recalculation
//     would carry a new profile_date and new per-source latencies.
//   - B's answer must differ from A's cached one: the cache key includes the
//     partner API key, so B gets its own calculation.
//
// The byte comparison only proves isolation when A's answer is unique to its
// calculation. The base actuarial profile (profile_date null) is a fixed
// answer that two independent calculations can repeat byte for byte (same
// source states and budget latencies), so those customers are counted as not
// comparable instead of as a leak (seen once in 800 in the 2026-10-11 staging
// window). The authoritative proof is the stage metrics for the window:
// CacheHitCount == CUSTOMERS and CacheMissCount == 2 x CUSTOMERS (A miss, A
// hit, B miss); any read of A's entry by B shows up as an extra hit.
//
// Variables: BASE_URL (stage URL), API_KEY_A, API_KEY_B, CUSTOMERS (800).
import http from "k6/http";
import { check } from "k6";
import { Counter, Rate } from "k6/metrics";

const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";
const API_KEY_A = __ENV.API_KEY_A || "";
const API_KEY_B = __ENV.API_KEY_B || "";
const CUSTOMERS = parseInt(__ENV.CUSTOMERS || "800", 10);
const RUN_ID = Date.now();

const servedFromCache = new Rate("served_from_cache");
const isolatedPerPartner = new Rate("isolated_per_partner");
const notComparable = new Counter("isolation_not_comparable");

// True when the answer is the fixed base actuarial profile, which another
// calculation can reproduce byte for byte.
function isFixedAnswer(body) {
  try {
    return JSON.parse(body).profile_date === null;
  } catch (e) {
    return false;
  }
}

export const options = {
  scenarios: {
    cache: {
      executor: "shared-iterations",
      vus: 10,
      iterations: CUSTOMERS,
      maxDuration: "5m",
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    served_from_cache: ["rate>0.95"], // CA-4: the repeated read comes from the cache
    isolated_per_partner: ["rate==1"], // CA-4: never another partner's entry
  },
  tags: { story: "HA-L01", criterion: "CA-4" },
};

function read(customerId, apiKey, partner) {
  return http.get(`${BASE_URL}/v1/risk-profiles/${encodeURIComponent(customerId)}?consent_active=true`, {
    headers: { "x-api-key": apiKey },
    tags: { partner },
  });
}

export default function () {
  const customerId = `cache-${RUN_ID}-${__VU}-${__ITER}`;

  const first = read(customerId, API_KEY_A, "a");
  const second = read(customerId, API_KEY_A, "a");
  const other = read(customerId, API_KEY_B, "b");

  const ok = check(first, { "partner A gets 200": (r) => r.status === 200 }) &&
    check(other, { "partner B gets 200": (r) => r.status === 200 });
  if (!ok) {
    return;
  }
  servedFromCache.add(second.status === 200 && second.body === first.body);
  if (isFixedAnswer(first.body)) {
    notComparable.add(1);
    return;
  }
  isolatedPerPartner.add(other.body !== first.body);
}

export function handleSummary(data) {
  const m = data.metrics;
  const pct = (x) => (x ? `${(x.values.rate * 100).toFixed(1)} %` : "n/a");
  return {
    stdout: [
      "",
      "HA-L01 CA-4 gateway cache",
      `  requests: ${m.http_reqs ? m.http_reqs.values.count : 0}   failed: ${pct(m.http_req_failed)}`,
      `  repeated read served from cache: ${pct(m.served_from_cache)}   partners isolated: ${pct(m.isolated_per_partner)}`,
      `  not comparable (fixed base profile): ${m.isolation_not_comparable ? m.isolation_not_comparable.values.count : 0}`,
      `  authoritative check: stage CacheHitCount == ${CUSTOMERS} and CacheMissCount == ${2 * CUSTOMERS} for this run`,
      "",
    ].join("\n"),
  };
}
