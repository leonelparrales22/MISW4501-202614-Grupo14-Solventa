// HA-D02 CA-5: every acceptance of IDS ended in exactly one real charge at the
// payment gateway (its own counters, not origination's database) and in one
// contractual notification per policy (e-mail stub). EXPECTED_TOTAL, when set,
// is the number of real charges the gateway must have made in the whole run:
// none lost, none duplicated. Prints "VERIFY ..." with the counts.
//
// With COUNTERFACTUAL=1 (a gateway that ignores the key) duplicates are
// reported instead of failing.
//
// Env: IDS, EXPECTED_TOTAL, COUNTERFACTUAL.
import http from "k6/http";
import { check } from "k6";
import { EMAIL_URL, GATEWAY_URL, INSURED, ids, json } from "./lib.js";

export const options = {
  vus: 1,
  iterations: 1,
  thresholds: { checks: ["rate==1"] },
};

export default function () {
  const all = ids();
  const debug = json(http.get(`${GATEWAY_URL}/debug/charges`, { tags: { name: "gateway_debug" } }));
  const requests = debug.requests || {};
  const real = debug.real_charges || {};
  const messages = json(http.get(`${EMAIL_URL}/messages?to=${encodeURIComponent(INSURED)}`, { tags: { name: "messages" } }))
    .messages || [];

  let duplicated = 0;
  let lost = 0;
  let retriedAtTheGateway = 0;
  for (const id of all) {
    const key = `cobro-${id}`;
    const charged = real[key] || 0;
    if (charged === 0) lost++;
    if (charged > 1) duplicated++;
    if ((requests[key] || 0) > 1) retriedAtTheGateway++;
    const mails = messages.filter((m) => m.body.includes(`pol-${id}`)).length;
    if (__ENV.COUNTERFACTUAL !== "1") {
      check(charged, { "exactly one real charge per acceptance": (n) => n === 1 });
      check(mails, { "one contractual notification per policy": (n) => n === 1 });
    }
  }
  console.log(
    `VERIFY acceptances=${all.length} lost=${lost} duplicated=${duplicated} ` +
      `keys_with_repeated_requests=${retriedAtTheGateway} gateway_requests=${debug.total_requests} ` +
      `gateway_real_charges=${debug.total_real_charges} supports_idempotency=${debug.supports_idempotency}`,
  );
  if (__ENV.EXPECTED_TOTAL && __ENV.COUNTERFACTUAL !== "1") {
    check(debug, {
      "the gateway made exactly the expected real charges": (d) => d.total_real_charges === Number(__ENV.EXPECTED_TOTAL),
    });
  }
}
