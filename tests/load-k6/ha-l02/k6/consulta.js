// HA-L02: the mobile journey of EC-L02 against the claims service. Each
// iteration is what the app does to show a claim: the list of "Mis
// siniestros" (M9), the detail and the timeline (M10).
//
// EXPECT=fresh  the database answers: every read is 200 with stale=false.
// EXPECT=stale  the database is slow or down: every read is 200 with
//               stale=true and the as_of of the last read.
//
// The threshold of the scenario is the journey: p95 of each read <= 2 s
// (criterion 1), no failed request and every check true.
import http from "k6/http";
import { check } from "k6";

const BASE = __ENV.BASE_URL;
const TOKEN = __ENV.TOKEN;
const CLAIM = __ENV.CLAIM_ID;
const EXPECT = __ENV.EXPECT || "fresh";

export const options = {
  scenarios: {
    journey: {
      executor: "constant-arrival-rate",
      rate: Number(__ENV.RATE || 20),
      timeUnit: "1s",
      duration: __ENV.DURATION || "30s",
      preAllocatedVUs: 40,
      maxVUs: 120,
    },
  },
  thresholds: {
    http_req_failed: ["rate==0"],
    checks: ["rate==1"],
    "http_req_duration{read:list}": ["p(95)<2000"],
    "http_req_duration{read:detail}": ["p(95)<2000"],
    "http_req_duration{read:history}": ["p(95)<2000"],
  },
  summaryTrendStats: ["avg", "med", "p(95)", "p(99)", "max"],
};

const params = (read) => ({ headers: { Authorization: `Bearer ${TOKEN}` }, tags: { read } });

function expectFreshness(res, name) {
  const body = res.json();
  const stale = EXPECT === "stale";
  check(res, {
    [`${name} 200`]: (r) => r.status === 200,
    [`${name} stale=${stale}`]: () => body.stale === stale,
    [`${name} X-Data-Source`]: (r) => r.headers["X-Data-Source"] === (stale ? "cache" : "database"),
    [`${name} as_of`]: () => typeof body.as_of === "string" && !Number.isNaN(Date.parse(body.as_of)),
  });
  return body;
}

export default function () {
  const list = http.get(`${BASE}/v1/claims`, params("list"));
  const page = expectFreshness(list, "list");
  check(page, { "list has the claim": (p) => (p.items || []).some((c) => c.claim_id === CLAIM) });

  expectFreshness(http.get(`${BASE}/v1/claims/${CLAIM}`, params("detail")), "detail");

  const history = expectFreshness(http.get(`${BASE}/v1/claims/${CLAIM}/history`, params("history")), "history");
  check(history, { "history has its changes": (h) => (h.changes || []).length >= 1 });
}
