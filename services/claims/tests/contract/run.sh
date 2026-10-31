#!/usr/bin/env bash
# Contract test of the claims routes (OBJ 05): starts the real service in
# Docker (PostgreSQL of its own, the policy core stub, Moto as Cognito and as
# the S3 of the supports) and
# runs Schemathesis against ../../api/openapi.yaml with every check, on the
# routes /v1/claims*:
#   1. as an insured: the filing of HU-M2.2-01 and every route of HU-W2.1-01
#      (filing options, declaration and confirmation of a support, web filing,
#      list, detail and receipt). The generated policies are not the insured's and the generated
#      ids do not exist, so the 422 and 404 answers are reached.
#   2. as an advisor: every route answers 403.
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

COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -f docker-compose.yml"
export CLAIMS_PORT="${CLAIMS_PORT:-8296}" MOTO_PORT="${MOTO_CLAIMS_PORT:-5122}"
secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
export PG_PASSWORD MOTO_USER_PASSWORD

cleanup() { $COMPOSE --profile tools down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== Starting PostgreSQL, the policy core stub and Moto"
$COMPOSE up -d --build --wait postgres-claims policy-core moto

echo "== Creating the bucket of the supports in Moto (S3)"
curl -sf -X PUT "http://localhost:$MOTO_PORT/claims-evidence" >/dev/null

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

echo "== Starting claims"
$COMPOSE up -d --build claims
for i in $(seq 1 90); do
  curl -sf "http://localhost:$CLAIMS_PORT/health" >/dev/null 2>&1 && break
  if [[ "$i" == 90 ]]; then
    echo "claims did not become healthy" >&2
    $COMPOSE logs claims | tail -40 >&2
    exit 1
  fi
  sleep 1
done

ROUTES='^/v1/claims'
# The mobile filing (HU-M2.1-01) asks for 4 to 6 pieces of evidence with their
# own keys, so Hypothesis discards most of what it generates for it (the
# filter_too_much health check); the answers it does reach are still checked.

seed=1
for user in asegurado.local@example.com asesor.local@example.com; do
  echo "== Schemathesis: the claims routes as $user"
  $COMPOSE --profile tools run --rm schemathesis run /api/openapi.yaml --base-url=http://claims:8080 \
    -H "Authorization: Bearer $(token "$user")" --include-path-regex="$ROUTES" \
    --checks=all --hypothesis-max-examples=50 --hypothesis-seed="$seed" \
    --hypothesis-suppress-health-check=filter_too_much
  seed=$((seed + 1))
done

echo "== claims contract passed."
