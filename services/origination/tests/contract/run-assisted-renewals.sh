#!/usr/bin/env bash
# Contract test of the assisted renewal routes of origination (HU-W1.5-03,
# OBJ 05): starts the real service in Docker (PostgreSQL of its own, the
# payment gateway and e-mail stubs so the renewal is mounted, audit for the
# consent, Moto as Cognito) and runs Schemathesis against ../../api/openapi.yaml with every
# check, on the routes /v1/policies/{policy_id}/assisted-renewal*:
#   1. as an advisor, with a Moto token: the generated policy ids do not exist,
#      so the 404 answers are reached, and the malformed bodies reach 400/422;
#   2. as an insured: every route answers 403.
# The script exits with a non-zero code if any check fails. The environment is
# always removed at the end (down -v). Requirements: Docker (compose v2), curl,
# jq and the AWS CLI (only to seed Moto, as tests/security/ha-s01/local does).
# Secrets are random, generated here and never printed.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl jq aws; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done

COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -f docker-compose.assisted-renewals.yml"
export ORIGINATION_ASSISTED_PORT="${ORIGINATION_ASSISTED_PORT:-8296}" MOTO_PORT="${MOTO_ASSISTED_PORT:-5122}"
secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
DOCUMENT_LINK_SECRET="$(secret 48)"
export PG_PASSWORD MOTO_USER_PASSWORD DOCUMENT_LINK_SECRET

cleanup() { $COMPOSE --profile tools down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== Starting PostgreSQL, Redis, the stubs and Moto"
$COMPOSE up -d --build --wait postgres-origination postgres-audit redis openfinance payment-gateway email moto

echo "== Seeding the local Cognito (Moto)"
SEED_OUT=""
# The published port of Moto can lag behind its health check on Docker Desktop.
for attempt in $(seq 1 15); do
  if SEED_OUT="$(env -u MSYS_NO_PATHCONV MOTO_URL="http://localhost:$MOTO_PORT" bash ../../../../tests/security/ha-s01/local/seed.sh 2>&1 | tr -d '\r')"; then
    break
  fi
  if [[ "$attempt" == 15 ]]; then
    echo "$SEED_OUT" >&2
    exit 1
  fi
  sleep 3
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
  curl -sf --max-time 2 "http://localhost:$ORIGINATION_ASSISTED_PORT/health" >/dev/null 2>&1 && break
  if [[ "$i" == 90 ]]; then
    echo "origination did not become healthy" >&2
    $COMPOSE logs origination audit | tail -40 >&2
    exit 1
  fi
  sleep 1
done

ROUTES='^/v1/policies/\{policy_id\}/assisted-renewal(/(requotes|consent|formalization))?$'

echo "== Schemathesis: the assisted renewal as the advisor"
$COMPOSE --profile tools run --rm schemathesis run /api/openapi.yaml --base-url=http://origination:8080 \
  -H "Authorization: Bearer $(token asesor.local@example.com)" --include-path-regex="$ROUTES" \
  --checks=all --hypothesis-max-examples=50 --hypothesis-seed=3

echo "== Every route refuses the insured (403)"
INSURED="$(token asegurado.local@example.com)"
for call in "GET /v1/policies/pol-x/assisted-renewal" "POST /v1/policies/pol-x/assisted-renewal/requotes" \
  "POST /v1/policies/pol-x/assisted-renewal/consent" "POST /v1/policies/pol-x/assisted-renewal/formalization"; do
  read -r method path <<<"$call"
  status="$(curl -s --max-time 10 -o /dev/null -w '%{http_code}' -X "$method" "http://localhost:$ORIGINATION_ASSISTED_PORT$path" \
    -H "Authorization: Bearer $INSURED" -H 'Content-Type: application/json' -d '{}')"
  if [[ "$status" != "403" ]]; then
    echo "$method $path as the insured answered $status, not 403" >&2
    exit 1
  fi
  echo "  ok    $method $path → 403"
done

echo "== origination assisted renewal contract passed."
