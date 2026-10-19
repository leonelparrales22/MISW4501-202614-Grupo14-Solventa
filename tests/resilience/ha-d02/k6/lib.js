// Shared helpers of the HA-D02 scenario scripts.
import http from "k6/http";
import { sleep } from "k6";

export const BASE_URL = __ENV.BASE_URL || "http://origination:8080";
export const MOTO_URL = __ENV.MOTO_URL || "http://moto:5000";
export const GATEWAY_URL = __ENV.GATEWAY_URL || "http://payment-gateway:9040";
export const EMAIL_URL = __ENV.EMAIL_URL || "http://email:9030";

// Users seeded by tests/security/ha-s01/local/seed.sh (one per role).
export const INSURED = "asegurado.local@example.com";
export const OPERATIONS = "operaciones.local@example.com";

// login returns the ID token of a seeded user from Moto (Cognito API).
export function login(username) {
  const res = http.post(
    `${MOTO_URL}/`,
    JSON.stringify({
      AuthFlow: "USER_PASSWORD_AUTH",
      ClientId: __ENV.AUTH_CLIENT_ID,
      AuthParameters: { USERNAME: username, PASSWORD: __ENV.MOTO_USER_PASSWORD },
    }),
    {
      headers: {
        "Content-Type": "application/x-amz-json-1.1",
        "X-Amz-Target": "AWSCognitoIdentityProviderService.InitiateAuth",
      },
      tags: { name: "moto_login" },
    },
  );
  if (res.status !== 200) {
    throw new Error(`login of ${username} failed with status ${res.status}`);
  }
  return res.json().AuthenticationResult.IdToken;
}

export function auth(token) {
  return { headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" } };
}

export function json(res) {
  try {
    return res.json();
  } catch (_) {
    return {};
  }
}

// ids reads the comma-separated acceptance ids of the IDS variable.
export function ids() {
  return (__ENV.IDS || "").split(",").filter((s) => s !== "");
}

// PSE is the payment method the scenario hands over: PSE does not tokenise,
// so no card ever travels (docs/contratos/HU-W1.3-02-api.md §2 and §3).
export const PSE = { method: "pse", bank_code: "1007" };

// payWithPSE waits until the charge of the acceptance exists (404 while the
// underwriting runs, HU-W1.3-02) and hands over PSE, which makes its first
// attempt due at once. Returns the answer of the payment method, or null if
// the charge never appeared within timeoutSeconds.
export function payWithPSE(token, id, timeoutSeconds = 30) {
  const deadline = Date.now() + timeoutSeconds * 1000;
  while (Date.now() < deadline) {
    const charge = http.get(`${BASE_URL}/v1/acceptances/${id}/charge`, Object.assign(auth(token), { tags: { name: "charge" } }));
    if (charge.status === 200) {
      return http.post(`${BASE_URL}/v1/acceptances/${id}/charge/payment-method`, JSON.stringify(PSE),
        Object.assign(auth(token), { tags: { name: "payment_method" } }));
    }
    sleep(0.1);
  }
  return null;
}
