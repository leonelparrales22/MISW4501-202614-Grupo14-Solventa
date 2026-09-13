import http from "k6/http";
import { check } from "k6";

export const options = {
  vus: Number(__ENV.VUS || 10),
  duration: __ENV.DURATION || "30s",
  thresholds: {
    http_req_failed: ["rate==0"],
    http_req_duration: ["p(95)<700"],
  },
};

const target = __ENV.TARGET || "http://localhost:8080";
const expected = __ENV.EXPECT_PROVISIONAL || "false";

export default function () {
  const response = http.post(
    `${target}/offers`,
    JSON.stringify({ customer_id: `customer-${__VU % 5}` }),
    { headers: { "Content-Type": "application/json" } },
  );
  check(response, {
    "responds 200": (r) => r.status === 200,
    "matches expected response mode": (r) =>
      expected === "any" || JSON.parse(r.body).provisional === (expected === "true"),
  });
}
