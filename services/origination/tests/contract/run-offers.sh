#!/usr/bin/env bash
# Contract test of the offer routes of origination (HU-W1.2-03, OBJ 05): starts
# the real service in Docker (PostgreSQL of its own, audit for the consent, Moto
# as Cognito) and runs Schemathesis against ../../api/openapi.yaml with every
# check, on the routes /v1/offers:
#   1. the partner-channel POST /v1/offers, without a token.
#   2. the human routes (GET /v1/offers/{id}, POST /v1/offers/{id}/renewal) as
#      an insured, and the read as an advisor, with a Moto token.
# The generated customers have no consent, so the 422 answers are reached; the
# generated ids do not exist, so the 404 answers are reached. The script exits
# with a non-zero code if any check fails. The environment is always removed at
# the end (down -v). Requirements: Docker (compose v2), curl, jq and the AWS CLI
# (only to seed Moto, as tests/security/ha-s01/local does). Secrets are random,
# generated here and never printed.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl jq aws; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done

COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -f docker-compose.offers.yml"
export ORIGINATION_OFFERS_PORT="${ORIGINATION_OFFERS_PORT:-8295}" MOTO_PORT="${MOTO_OFFERS_PORT:-5121}"
secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
export PG_PASSWORD MOTO_USER_PASSWORD

cleanup() { $COMPOSE --profile tools down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== Starting PostgreSQL, Redis, the Open Finance stub and Moto"
$COMPOSE up -d --build --wait postgres-origination postgres-audit redis openfinance moto

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
token() { # <username>
  curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
    -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$WEB_CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"$1\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken'
}

echo "== Starting audit and origination"
$COMPOSE up -d --build audit origination
for i in $(seq 1 90); do
  curl -sf "http://localhost:$ORIGINATION_OFFERS_PORT/health" >/dev/null 2>&1 && break
  if [[ "$i" == 90 ]]; then
    echo "origination did not become healthy" >&2
    $COMPOSE logs origination audit | tail -40 >&2
    exit 1
  fi
  sleep 1
done

ROUTES='^/v1/offers(/\{offer_id\}(/renewal)?)?$'

echo "== Schemathesis: the partner-channel POST /v1/offers"
$COMPOSE --profile tools run --rm schemathesis run /api/openapi.yaml --base-url=http://origination:8080 \
  --include-path-regex='^/v1/offers$' --checks=all --hypothesis-max-examples=100 --hypothesis-seed=1

for user in asegurado.local@example.com asesor.local@example.com; do
  echo "== Schemathesis: the human routes as $user"
  $COMPOSE --profile tools run --rm schemathesis run /api/openapi.yaml --base-url=http://origination:8080 \
    -H "Authorization: Bearer $(token "$user")" --include-path-regex="$ROUTES" \
    --checks=all --hypothesis-max-examples=50 --hypothesis-seed=2
done

echo "== origination offers contract passed."
