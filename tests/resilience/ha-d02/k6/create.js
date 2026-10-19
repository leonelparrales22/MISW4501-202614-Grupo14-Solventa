// HA-D02 CA-1: the insured accepts COUNT demo offers; every acceptance is
// registered (202) and readable at once, whatever the payment gateway is
// doing. Then, as screen E4 does (HU-W1.3-02), the insured hands over PSE as
// soon as the charge exists: without a payment method the charge would wait
// and never be due. Prints "ACCEPTANCES <PHASE> <id,id,...>" for run.sh.
//
// Env: RUN_ID, PHASE, COUNT.
import http from "k6/http";
import { check } from "k6";
import { BASE_URL, INSURED, auth, json, login, payWithPSE } from "./lib.js";

export const options = {
  vus: 1,
  iterations: 1,
  thresholds: { checks: ["rate==1"] },
};

export function setup() {
  return { token: login(INSURED) };
}

export default function (data) {
  const phase = __ENV.PHASE;
  const count = Number(__ENV.COUNT || 1);
  const created = [];
  for (let i = 0; i < count; i++) {
    const offer = `demo-active-${__ENV.RUN_ID}-${phase}-${i}`;
    const res = http.post(`${BASE_URL}/v1/offers/${offer}/acceptance`, JSON.stringify({ channel: "web" }),
      Object.assign(auth(data.token), { tags: { name: "accept" } }));
    const body = json(res);
    check(res, {
      "acceptance registered (202)": (r) => r.status === 202,
      "status registered": () => body.status === "registered",
    });
    if (!body.acceptance_id) {
      continue;
    }
    const read = http.get(`${BASE_URL}/v1/acceptances/${body.acceptance_id}`,
      Object.assign(auth(data.token), { tags: { name: "acceptance" } }));
    check(read, { "acceptance readable at once": (r) => r.status === 200 });
    created.push(body.acceptance_id);
  }
  // The payment method is handed over once every acceptance is registered, so
  // the acceptances of a phase are not slowed down by the underwriting.
  for (const id of created) {
    const paid = payWithPSE(data.token, id);
    const charge = paid ? json(paid) : {};
    check(paid, {
      "PSE handed over (202)": (r) => r !== null && r.status === 202,
      "payment method is pse": () => (charge.payment_method || {}).method === "pse",
      "the key is cobro-<acceptance>": () => charge.idempotency_key === `cobro-${id}`,
    });
  }
  console.log(`ACCEPTANCES ${phase} ${created.join(",")}`);
}
