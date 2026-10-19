// HA-D02: waits until every acceptance of IDS reaches the EXPECT state of its
// premium charge, then checks it together with the contractual issuance of
// HU-W1.3-03 (CA-4: a coverage exists only with a confirmed payment).
//
// EXPECT=pending    the gateway is down or answers ambiguously: the charge is
//                   pending with at least one failed attempt, the same key,
//                   a 24-hour window, and the policy has no issuance (404).
// EXPECT=confirmed  charged, the activation published, the issuance completed.
// EXPECT=failed     the retry window expired: an issuance exception listed for
//                   operations (and forbidden to the insured), no issuance.
//                   With RESUME=1 operations then resume every one with a note.
//
// With GATEWAY_CHARGED_MIN=N (ambiguous phase) it also waits until at least N
// of the pending acceptances were really charged by the gateway while
// origination still waits: the answer was lost although the money moved, the
// case that could duplicate. Every one of them must have exactly one real
// charge, however many requests carried its key (except in the counterfactual
// run). With the circuit breaker open between probes, few attempts reach the
// gateway during an outage, so N is small.
//
// Prints "SAMPLE <EXPECT> <json>" with the charge of the first acceptance and
// "AMBIGUOUS ..." with the gateway counts of the pending acceptances.
//
// Env: IDS, EXPECT, TIMEOUT_SECONDS (default 180), WINDOW_SECONDS (default
// 86400), RESUME, GATEWAY_CHARGED_MIN, COUNTERFACTUAL.
import http from "k6/http";
import { check, sleep } from "k6";
import { BASE_URL, GATEWAY_URL, INSURED, OPERATIONS, auth, ids, json, login } from "./lib.js";

export const options = {
  vus: 1,
  iterations: 1,
  thresholds: { checks: ["rate==1"] },
};

const EXPECT = __ENV.EXPECT || "confirmed";
const TIMEOUT_SECONDS = Number(__ENV.TIMEOUT_SECONDS || 180);
const WINDOW_SECONDS = Number(__ENV.WINDOW_SECONDS || 86400);
const GATEWAY_CHARGED_MIN = Number(__ENV.GATEWAY_CHARGED_MIN || 0);

// gatewayCounts returns the requests and the real charges per key at the gateway.
function gatewayCounts() {
  const d = json(http.get(`${GATEWAY_URL}/debug/charges`, { tags: { name: "gateway_debug" } }));
  return { requests: d.requests || {}, real: d.real_charges || {} };
}

// chargedWhilePending returns the ids the gateway charged at least once.
function chargedAtTheGateway(all, counts) {
  return all.filter((id) => (counts.real[`cobro-${id}`] || 0) >= 1);
}

export function setup() {
  return { insured: login(INSURED), operations: login(OPERATIONS) };
}

function charge(token, id) {
  const res = http.get(`${BASE_URL}/v1/acceptances/${id}/charge`, Object.assign(auth(token), { tags: { name: "charge" } }));
  return res.status === 200 ? json(res) : null;
}

function issuance(token, id) {
  return http.get(`${BASE_URL}/v1/policies/pol-${id}/issuance-status`, Object.assign(auth(token), { tags: { name: "issuance" } }));
}

function reached(token, id) {
  const c = charge(token, id);
  if (!c) {
    return false;
  }
  switch (EXPECT) {
    case "pending":
      return (c.status === "pending" || c.status === "in_progress") && c.attempts >= 1;
    case "failed":
      return c.status === "failed";
    default: {
      if (c.status !== "confirmed" || c.activation_published_at === null) {
        return false;
      }
      const st = issuance(token, id);
      return st.status === 200 && json(st).status === "completed";
    }
  }
}

export default function (data) {
  const all = ids();
  const started = Date.now();
  let waiting = all.slice();
  while (waiting.length > 0 && Date.now() - started < TIMEOUT_SECONDS * 1000) {
    waiting = waiting.filter((id) => !reached(data.insured, id));
    if (waiting.length > 0) {
      sleep(1);
    }
  }
  console.log(`CONVERGED ${EXPECT} ${all.length - waiting.length}/${all.length} in ${((Date.now() - started) / 1000).toFixed(1)} s`);
  check(waiting, { [`all ${all.length} charges reached ${EXPECT}`]: (w) => w.length === 0 });
  if (all.length > 0) {
    console.log(`SAMPLE ${EXPECT} ${JSON.stringify(charge(data.insured, all[0]))}`);
  }
  if (EXPECT === "pending" && GATEWAY_CHARGED_MIN > 0) {
    ambiguous(all, started);
  }

  for (const id of all) {
    const c = charge(data.insured, id) || {};
    const st = issuance(data.insured, id);
    check(c, { "the key is cobro-<acceptance>": (b) => b.idempotency_key === `cobro-${id}` });
    if (EXPECT === "pending") {
      const windowSeconds = (Date.parse(c.retry_window.ends_at) - Date.parse(c.retry_window.started_at)) / 1000;
      check(c, {
        "payment pending with a next attempt": (b) => b.next_attempt_at !== null && b.policy_id === null,
        "retry window as configured": () => windowSeconds === WINDOW_SECONDS,
      });
      check(st, { "no issuance without a confirmed payment (404)": (r) => r.status === 404 });
    } else if (EXPECT === "failed") {
      check(c, { "failed because the window expired": (b) => b.failure_reason === "retry_window_expired" });
      check(st, { "no issuance for a failed charge (404)": (r) => r.status === 404 });
    } else {
      check(c, {
        "confirmed with its policy": (b) => b.status === "confirmed" && b.policy_id === `pol-${id}`,
        "coverage activated": (b) => b.coverage_activated_at !== null,
      });
      check(st, { "contractual issuance completed": (r) => r.status === 200 && json(r).status === "completed" });
    }
  }

  if (EXPECT === "failed") {
    exceptions(data, all);
  }
}

// ambiguous waits for GATEWAY_CHARGED_MIN acceptances charged at the gateway
// while still pending, and checks that each one was charged exactly once.
function ambiguous(all, started) {
  let counts = gatewayCounts();
  while (chargedAtTheGateway(all, counts).length < GATEWAY_CHARGED_MIN && Date.now() - started < TIMEOUT_SECONDS * 1000) {
    sleep(2);
    counts = gatewayCounts();
  }
  const charged = chargedAtTheGateway(all, counts);
  const requests = charged.reduce((n, id) => n + (counts.requests[`cobro-${id}`] || 0), 0);
  const realTotal = charged.reduce((n, id) => n + (counts.real[`cobro-${id}`] || 0), 0);
  console.log(
    `AMBIGUOUS charged_at_the_gateway_while_pending=${charged.length}/${all.length} ` +
      `requests_with_their_keys=${requests} real_charges_of_them=${realTotal}`,
  );
  check(charged, {
    [`at least ${GATEWAY_CHARGED_MIN} charged at the gateway while origination still waits`]: (c) =>
      c.length >= GATEWAY_CHARGED_MIN,
  });
  if (__ENV.COUNTERFACTUAL === "1") {
    return;
  }
  for (const id of charged) {
    check(counts.real[`cobro-${id}`], { "one real charge however many requests carried the key": (n) => n === 1 });
  }
}

function exceptions(data, all) {
  const res = http.get(`${BASE_URL}/v1/issuance-exceptions?limit=200`, Object.assign(auth(data.operations), { tags: { name: "exceptions" } }));
  const items = json(res).items || [];
  const listed = new Map(items.map((e) => [e.acceptance_id, e]));
  if (items.length > 0) {
    console.log(`SAMPLE exception ${JSON.stringify(items[0])}`);
  }
  check(res, { "exceptions listed for operations (200)": (r) => r.status === 200 });
  for (const id of all) {
    check(listed.get(id), {
      "the exception is visible in the monitoring": (e) =>
        e !== undefined && e.step === "premium_charge" && e.reason === "retry_window_expired",
    });
  }
  const forbidden = http.get(`${BASE_URL}/v1/issuance-exceptions`, Object.assign(auth(data.insured), { tags: { name: "exceptions" } }));
  check(forbidden, { "the insured cannot list exceptions (403)": (r) => r.status === 403 });

  if (__ENV.RESUME !== "1") {
    return;
  }
  for (const id of all) {
    const r = http.post(`${BASE_URL}/v1/acceptances/${id}/charge/resume`,
      JSON.stringify({ note: "Pasarela restablecida: se reanuda desde la aceptacion registrada (HA-D02)" }),
      Object.assign(auth(data.operations), { tags: { name: "resume" } }));
    const body = json(r);
    check(r, {
      "resumed (202)": (x) => x.status === 202,
      "resumed with the same key": () => body.idempotency_key === `cobro-${id}`,
      "pending again": () => body.status === "pending" && body.resume_count === 1,
    });
  }
}
