// HA-L01 readiness gate of the staging window: before any load, checks that
// the whole journey answers (API Gateway -> VPC link -> internal ALB ->
// origination -> Toxiproxy -> stubs). 30 sequential quotes with partner A's key:
//   - every answer is 200 and carries the sources array (checks == 100 %);
//   - at least half of them have some source "ok" (source_reachable >= 50 %).
// With the normal latency profile about 99 % of the answers have some source
// "ok" and about 76 % with the degraded one; when Toxiproxy did not create its
// proxies it is 0 %, so the gate fails and sequence.sh sends no load.
//
// Variables: BASE_URL (stage URL), API_KEY_A (partner A key, sent as
// x-api-key when set), READY_REQUESTS (30).
import http from "k6/http";
import { check } from "k6";
import { Rate } from "k6/metrics";

const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";
const API_KEY = __ENV.API_KEY_A || "";
const REQUESTS = parseInt(__ENV.READY_REQUESTS || "30", 10);
const RUN_ID = Date.now();
const HEADERS = API_KEY
  ? { "Content-Type": "application/json", "x-api-key": API_KEY }
  : { "Content-Type": "application/json" };

const sourceReachable = new Rate("source_reachable");

export const options = {
  scenarios: {
    ready: {
      executor: "shared-iterations",
      vus: 1,
      iterations: REQUESTS,
      maxDuration: "2m",
    },
  },
  thresholds: {
    checks: ["rate==1"], // every answer is 200 with the sources array
    source_reachable: ["rate>=0.5"], // the sources are reachable through Toxiproxy
  },
  tags: { story: "HA-L01", step: "ready" },
};

export default function () {
  const res = http.post(
    `${BASE_URL}/v1/risk-profiles/resolve`,
    JSON.stringify({ customer_id: `ready-${RUN_ID}-${__ITER}`, consent_active: true }),
    { headers: HEADERS, timeout: "10s" },
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
    "sources is an array": () => Array.isArray(body.sources),
  });
  sourceReachable.add(sources.some((s) => s.status === "ok"));
}

export function handleSummary(data) {
  const m = data.metrics;
  const pct = (x) => (x ? `${(x.values.rate * 100).toFixed(1)} %` : "n/a");
  return {
    stdout: [
      "",
      `HA-L01 readiness gate: requests ${m.http_reqs ? m.http_reqs.values.count : 0}` +
        ` | checks ok ${pct(m.checks)} (needs 100 %)` +
        ` | some source ok ${pct(m.source_reachable)} (needs >= 50 %)`,
      "",
    ].join("\n"),
  };
}
