// Shared helpers of the HA-D02 scenario scripts.
import http from "k6/http";

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
