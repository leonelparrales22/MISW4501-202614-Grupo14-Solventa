// HA-D01 acceptance check: during an Open Finance outage 100% of the offer
// requests are answered, either normal or provisional, and none is lost.
//
// EXPECT_MODE=normal      every answer is a complete profile, from Open Finance
//                         or reused from the cache.
// EXPECT_MODE=provisional Open Finance is down: customers warmed in the
//                         baseline phase reuse their complete cached profile
//                         without any query (HU-W1.2-02, so their answer stays
//                         normal); new customers get the base actuarial profile.
import http from "k6/http";
import { check } from "k6";

const BASE_URL = __ENV.BASE_URL || "http://localhost:8180";
const EXPECT_MODE = __ENV.EXPECT_MODE || "normal";
const WARM_CUSTOMERS = 10;

export const options = {
  vus: Number(__ENV.VUS || 5),
  duration: __ENV.DURATION || "10s",
  thresholds: {
    http_req_failed: ["rate==0"], // no request fails
    checks: ["rate==1"], // every answer is the expected one
  },
};

export default function () {
  const isNew = __ITER % 2 === 0;
  const customerId = isNew ? `new-${__VU}-${__ITER}-${Date.now()}` : `warm-${__ITER % WARM_CUSTOMERS}`;

  const res = http.post(
    `${BASE_URL}/v1/risk-profiles/resolve`,
    JSON.stringify({ customer_id: customerId, consent_active: true }),
    { headers: { "Content-Type": "application/json" } },
  );

  let body = {};
  try {
    body = res.json();
  } catch (_) {
    // the checks below fail when the body is not JSON
  }

  const checks = { "status is 200": () => res.status === 200 };
  if (EXPECT_MODE === "normal") {
    checks["mode is normal"] = () => body.mode === "normal" && (body.source === "open_finance" || body.source === "cache");
  } else if (isNew) {
    checks["new customer gets the base profile"] = () =>
      body.mode === "provisional" &&
      body.unavailable_source === "open_finance" &&
      body.source === "base_actuarial" &&
      body.notice === "personalization_unavailable" &&
      body.profile_date === null;
  } else {
    checks["warm customer reuses the cached profile"] = () =>
      body.mode === "normal" && body.source === "cache" && body.profile_date !== null;
  }
  check(res, checks);
}
