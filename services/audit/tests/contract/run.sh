#!/usr/bin/env bash
# Contract test of the audit service (HU-W1.2-01, OBJ 05): starts audit over
# PostgreSQL and Moto in Docker and runs Schemathesis against
# ../../api/openapi.yaml with every check, once as an operations user (who can
# read the evidence), once as an insured user over their own consents
# (HU-M1.3-01: the link of the account and a consent are seeded first, as
# test data only) and, for the routes that need no token, with no credentials
# at all. The script exits with a non-zero code if any check
# fails. The environment is always removed at the end (down -v).
#
# Requirements: Docker (compose v2), curl, jq and the AWS CLI (only to seed the
# local Moto, as tests/security/ha-s01/local does). The secrets are random,
# generated here and never printed.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl jq aws; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done

COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -f docker-compose.yml"
export AUDIT_PORT="${AUDIT_PORT:-8292}" MOTO_PORT="${MOTO_PORT:-5121}"

secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
export PG_PASSWORD MOTO_USER_PASSWORD

cleanup() { $COMPOSE --profile tools down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== Starting PostgreSQL and Moto"
$COMPOSE up -d --build --wait postgres moto

echo "== Seeding the local Cognito (Moto)"
SEED_OUT=""
for attempt in 1 2 3 4 5; do
  if SEED_OUT="$(env -u MSYS_NO_PATHCONV MOTO_URL="http://localhost:$MOTO_PORT" bash ../../../../tests/security/ha-s01/local/seed.sh 2>&1 | tr -d '\r')"; then
    break
  fi
  if [[ "$attempt" == 5 ]]; then
    echo "$SEED_OUT" >&2
    exit 1
  fi
  sleep 2
done
AUTH_ISSUER="$(sed -n 's/^  export AUTH_ISSUER=//p' <<<"$SEED_OUT")"
AUTH_CLIENT_IDS="$(sed -n 's/^  export AUTH_CLIENT_IDS=//p' <<<"$SEED_OUT")"
WEB_CLIENT_ID="${AUTH_CLIENT_IDS%%,*}"
AUTH_JWKS_URL="http://moto:5000/${AUTH_ISSUER##*/}/.well-known/jwks.json"
export AUTH_ISSUER AUTH_CLIENT_IDS AUTH_JWKS_URL
if [[ -z "$AUTH_ISSUER" || -z "$WEB_CLIENT_ID" ]]; then
  echo "Could not read the pool and the client from seed.sh" >&2
  exit 1
fi

echo "== Starting audit"
$COMPOSE up -d --build --wait audit
for _ in $(seq 1 60); do
  curl -sf "http://localhost:$AUDIT_PORT/health" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "http://localhost:$AUDIT_PORT/health" >/dev/null || {
  $COMPOSE logs audit | tail -40 >&2
  echo "audit did not become healthy" >&2
  exit 1
}

TOKEN="$(curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
  -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
  -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$WEB_CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"operaciones.local@example.com\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
  tr -d '\r' | jq -r '.AuthenticationResult.IdToken')"

echo "== Schemathesis as an operations user (all routes)"
$COMPOSE --profile tools run --rm schemathesis run /api/openapi.yaml --base-url=http://audit:8080 \
  -H "Authorization: Bearer $TOKEN" --checks=all --hypothesis-max-examples=100 --hypothesis-seed=1

echo "== Seeding the holder of the insured user (link by SQL, consent by the partner route)"
INSURED_TOKEN="$(curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1'   -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth'   -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$WEB_CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"asegurado.local@example.com\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
  tr -d '' | jq -r '.AuthenticationResult.IdToken')"
# The subject is the "sub" claim of the token (base64url payload).
PAYLOAD="$(cut -d. -f2 <<<"$INSURED_TOKEN" | tr '_-' '/+')"
while (( ${#PAYLOAD} % 4 )); do PAYLOAD="$PAYLOAD="; done
INSURED_SUB="$(base64 -d <<<"$PAYLOAD" | jq -r .sub)"
$COMPOSE exec -T postgres psql -U audit -d audit -v ON_ERROR_STOP=1 -q -c   "INSERT INTO holder_link (subject, document_id, customer_id, linked_at, event_id) VALUES ('$INSURED_SUB', '1020304050', 'cus-contract', now(), 'customer-linked-cus-contract')"
curl -sf -o /dev/null -X POST "http://localhost:$AUDIT_PORT/v1/audit/consents" -H 'Content-Type: application/json'   -d '{"customer_id":"cus-contract","document_id":"1020304050","full_name":"Asegurado Contrato","terms_version":"v1","accepted_sources":["open_finance","open_data","kyc"],"accepted_data_policy":true,"channel":"embedded","partner_id":"aliado-banco-1","application_id":"app-contract"}'

echo "== Schemathesis as an insured user (their own consents, HU-M1.3-01)"
$COMPOSE --profile tools run --rm schemathesis run /api/openapi.yaml --base-url=http://audit:8080   -H "Authorization: Bearer $INSURED_TOKEN" --include-path-regex='^/v1/consents'   --checks=all --hypothesis-max-examples=100 --hypothesis-seed=3

echo "== Schemathesis with no credentials (the routes that need none)"
$COMPOSE --profile tools run --rm schemathesis run /api/openapi.yaml --base-url=http://audit:8080 \
  --include-path-regex='^/(health|v1/audit/(consent-terms|consent-verifications|consents$))' \
  --checks=all --hypothesis-max-examples=100 --hypothesis-seed=2

echo "== audit contract passed."
