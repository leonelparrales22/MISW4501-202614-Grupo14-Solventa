#!/usr/bin/env bash
# Runs the HU-W1.2 end-to-end scenarios with Docker, against the real audit and
# origination services (PostgreSQL of audit, Redis, the Open Finance / Open
# Data / KYC stubs behind Toxiproxy, Moto as Cognito):
#
#   HU-W1.2-01 (explicit consent)
#     1. CA-1  the terms shown (GET /current) are the terms recorded
#     2. CA-3  without a consent, origination answers 422 and no source is queried
#     3. CA-2  the consent is recorded, queryable with an operations token and verified
#     4. CA-3  with the consent, the profile is resolved from the sources
#     5. CA-3  with audit stopped, origination answers 503 and no source is queried
#     6. CA-4  UPDATE, DELETE and TRUNCATE on consent_record fail with psql
#     7.       security: the evidence needs a token and the right role
#
# "No source is queried" is checked on Toxiproxy: every call to a stub opens a
# connection through its proxy, and the proxy logs each one ("Accepted client"),
# so zero new lines means zero requests reached the stubs. (The reverse is a
# lower bound: origination reuses connections, so one line may carry several
# requests; that is why origination is restarted before the audit-down check.)
#
# The script exits with a non-zero code when any check fails. The environment
# is always removed at the end (down -v). Requirements: Docker (compose v2),
# curl, jq and the AWS CLI (only to seed the local Moto, as
# tests/security/ha-s01/local does). The secrets (PostgreSQL, Moto users) are
# random, generated here and never printed.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl jq aws; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done

# MSYS_NO_PATHCONV keeps Git Bash from rewriting container paths.
COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -f docker-compose.yml"
export AUDIT_PORT="${AUDIT_PORT:-8290}" ORIGINATION_PORT="${ORIGINATION_PORT:-8291}"
export TOXIPROXY_PORT="${TOXIPROXY_PORT:-8494}" MOTO_PORT="${MOTO_PORT:-5120}"
AUDIT="http://localhost:$AUDIT_PORT"
ORIGINATION="http://localhost:$ORIGINATION_PORT"
RUN_ID="r$(date +%s)"

secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
export PG_PASSWORD MOTO_USER_PASSWORD

WORK="$(mktemp -d)"
cleanup() {
  $COMPOSE down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

stamp() { date -u +%H:%M:%S; }
FAILS=0
check() { # <description> <expected> <actual>
  if [[ "$2" == "$3" ]]; then
    echo "  ok    $1"
  else
    echo "  FAIL  $1 (expected '$2', got '$3')" >&2
    FAILS=$((FAILS + 1))
  fi
}

# call <method> <url> [json body] [bearer token]: sets STATUS and BODY.
call() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -X "$1" "$2" -H 'User-Agent: hu-w1.2-e2e')
  [[ -n "${3:-}" ]] && args+=(-H 'Content-Type: application/json' -d "$3")
  [[ -n "${4:-}" ]] && args+=(-H "Authorization: Bearer $4")
  STATUS="$(curl "${args[@]}")"
  BODY="$(cat "$WORK/body")"
}
field() { jq -r "$1" <<<"$BODY"; }

# source_connections: connections the stubs received through Toxiproxy so far.
source_connections() { $COMPOSE logs --no-log-prefix toxiproxy 2>/dev/null | grep -c 'Accepted client' || true; }

wait_healthy() { # <url>
  local i
  for i in $(seq 1 90); do
    curl -sf "$1/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "$1 did not become healthy" >&2
  $COMPOSE logs audit origination | tail -40 >&2
  return 1
}

grant_body() { # <customer> <application> <terms_version>
  cat <<JSON
{"customer_id":"$1","document_id":"1020304050","full_name":"Ana María Pérez","terms_version":"$3",
 "accepted_sources":["open_finance","open_data","kyc"],"accepted_data_policy":true,"channel":"embedded",
 "partner_id":"partner-e2e","application_id":"$2"}
JSON
}
resolve_body() { echo "{\"customer_id\":\"$1\",\"consent_active\":true}"; }

echo "== [$(stamp)] Starting PostgreSQL, Redis, the stubs behind Toxiproxy and Moto"
$COMPOSE up -d --build --wait postgres-audit redis openfinance opendata kyc toxiproxy moto

echo "== [$(stamp)] Seeding the local Cognito (Moto)"
SEED_OUT=""
for attempt in 1 2 3 4 5; do
  if SEED_OUT="$(env -u MSYS_NO_PATHCONV MOTO_URL="http://localhost:$MOTO_PORT" bash ../../security/ha-s01/local/seed.sh 2>&1 | tr -d '\r')"; then
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
OPERATIONS_TOKEN="$(token operaciones.local@example.com)"
INSURED_TOKEN="$(token asegurado.local@example.com)"

echo "== [$(stamp)] Starting audit and origination (AUDIT_URL points to the real audit)"
$COMPOSE up -d --build audit origination
wait_healthy "$AUDIT"
wait_healthy "$ORIGINATION"
$COMPOSE logs audit origination | grep -E "consent|database migration" || true

CUSTOMER="cust-$RUN_ID"
APPLICATION="app-$RUN_ID"

echo "== [$(stamp)] 1. CA-1: the terms shown are the terms recorded"
call GET "$AUDIT/v1/audit/consent-terms/current"
check "GET /current answers 200" 200 "$STATUS"
SHOWN="$(field .version)"
SHOWN_SOURCES="$(field '.sources | map(.code) | join(",")')"
check "the terms list the three sources" "open_finance,open_data,kyc" "$SHOWN_SOURCES"

echo "== [$(stamp)] 2. CA-3: without a consent no source is queried"
BEFORE="$(source_connections)"
call POST "$ORIGINATION/v1/risk-profiles/resolve" "$(resolve_body "$CUSTOMER")"
check "origination answers 422 even if the caller affirms the consent" 422 "$STATUS"
check "the error is consent_required" consent_required "$(field .error)"
check "no request reached the stubs" "$BEFORE" "$(source_connections)"

echo "== [$(stamp)] 3. CA-2: the consent is recorded, queryable and verified"
call POST "$AUDIT/v1/audit/consents" "$(grant_body "$CUSTOMER" "$APPLICATION" v0)"
check "an outdated terms version is a 409" 409 "$STATUS"
check "the error is terms_outdated" terms_outdated "$(field .error)"
call POST "$AUDIT/v1/audit/consents" "$(grant_body "$CUSTOMER" "$APPLICATION" "$SHOWN")"
check "the consent is created (201)" 201 "$STATUS"
CONSENT_ID="$(field .consent_id)"
check "the terms recorded are the terms shown" "$SHOWN" "$(field .terms_version)"
call POST "$AUDIT/v1/audit/consents" "$(grant_body "$CUSTOMER" "$APPLICATION" "$SHOWN")"
check "granting again is idempotent (200)" 200 "$STATUS"
check "and returns the same consent" "$CONSENT_ID" "$(field .consent_id)"
call GET "$AUDIT/v1/audit/consents/$CONSENT_ID" "" "$OPERATIONS_TOKEN"
check "operations reads the evidence (200)" 200 "$STATUS"
check "the evidence names the customer" "$CUSTOMER" "$(field .customer_id)"
check "the evidence names the holder" "Ana María Pérez" "$(field .holder.full_name)"
check "the evidence names the channel" embedded "$(field .channel)"
check "the evidence has the user agent" hu-w1.2-e2e "$(field .user_agent)"
call GET "$AUDIT/v1/audit/consent-verifications?customer_id=$CUSTOMER"
check "audit verifies the consent as active" true "$(field .active)"
check "the verification names the consent" "$CONSENT_ID" "$(field .consent_id)"

echo "== [$(stamp)] 4. CA-3: with the consent the profile is resolved from the sources"
BEFORE="$(source_connections)"
call POST "$ORIGINATION/v1/risk-profiles/resolve" "$(resolve_body "$CUSTOMER")"
check "origination answers 200" 200 "$STATUS"
check "the profile is normal" normal "$(field .mode)"
check "it comes from the external sources" external_sources "$(field .source)"
if [[ "$(source_connections)" -gt "$BEFORE" ]]; then
  check "the stubs were queried" yes yes
else
  check "the stubs were queried" yes no
fi

echo "== [$(stamp)] 5. CA-3: with audit stopped nothing is queried (fail-closed)"
OTHER="cust-down-$RUN_ID"
call POST "$AUDIT/v1/audit/consents" "$(grant_body "$OTHER" "app-down-$RUN_ID" "$SHOWN")"
check "another customer consents" 201 "$STATUS"
$COMPOSE stop audit >/dev/null
# A fresh origination has no open connection to the stubs, so any source call
# it made would show up in Toxiproxy as a new connection.
$COMPOSE restart origination >/dev/null
wait_healthy "$ORIGINATION"
BEFORE="$(source_connections)"
call POST "$ORIGINATION/v1/risk-profiles/resolve" "$(resolve_body "$OTHER")"
check "origination answers 503 although the consent exists" 503 "$STATUS"
check "the error is request_aborted" request_aborted "$(field .error)"
check "no request reached the stubs" "$BEFORE" "$(source_connections)"
$COMPOSE start audit >/dev/null
wait_healthy "$AUDIT"
RECOVERED=no
for _ in $(seq 1 20); do
  call POST "$ORIGINATION/v1/risk-profiles/resolve" "$(resolve_body "$OTHER")"
  if [[ "$STATUS" == 200 ]]; then
    RECOVERED=yes
    break
  fi
  sleep 1
done
check "when audit is back the profile is resolved again" yes "$RECOVERED"

echo "== [$(stamp)] 6. CA-4: the record cannot be altered"
psql_audit() { $COMPOSE exec -T postgres-audit psql -U audit -d audit -v ON_ERROR_STOP=1 -c "$1" 2>&1 || true; }
for stmt in "UPDATE consent_record SET holder_full_name = 'Someone Else'" "DELETE FROM consent_record" "TRUNCATE consent_record"; do
  OUT="$(psql_audit "$stmt")"
  if grep -q "append-only" <<<"$OUT"; then
    check "$stmt is rejected" rejected rejected
  else
    check "$stmt is rejected" rejected "accepted: $OUT"
  fi
done
call GET "$AUDIT/v1/audit/consents/$CONSENT_ID" "" "$OPERATIONS_TOKEN"
check "the record is intact" "Ana María Pérez" "$(field .holder.full_name)"

echo "== [$(stamp)] 7. Security of the evidence lookup"
call GET "$AUDIT/v1/audit/consents/$CONSENT_ID"
check "without a token: 401" 401 "$STATUS"
call GET "$AUDIT/v1/audit/consents/$CONSENT_ID" "" "$INSURED_TOKEN"
check "as an insured: 403" 403 "$STATUS"
call GET "$AUDIT/v1/audit/consents/cns-00000000000000000000000000000000" "" "$OPERATIONS_TOKEN"
check "an unknown id: 404" 404 "$STATUS"

if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-W1.2 end-to-end scenarios FAILED: $FAILS check(s) (run $RUN_ID)." >&2
  exit 1
fi
echo "== [$(stamp)] HU-W1.2 end-to-end scenarios passed (run $RUN_ID)."
