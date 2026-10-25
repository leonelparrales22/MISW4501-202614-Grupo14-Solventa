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
#   HU-W1.2-03 (personalized offer, PostgreSQL of origination, e-mail stub)
#     8.  CA-1/CA-2  the offer is complete, valid 24 h, claimed on first access, persisted and immutable
#     9.  CA-3       Open Finance down: base_actuarial_profile; partial cached profile: cached_profile + date
#     10. CA-4       the source returns: the provisional offer is replaced, ONE e-mail, the old one cannot be
#                    accepted (422 offer_expired), the new one can (202); an accepted provisional is untouched
#     11. CA-5       an expired offer (seeded in PostgreSQL) is refused and renewed, idempotently
#     12.            origination restarts: the offers and their owners persist
#     13.            security: no score/rules/document, consent_mismatch, no consent = no source
#
#   HU-W4.1-01 (audit record of the underwriting decisions)
#     14. CA-1..4    every offer priced is recorded in audit (rules in order, sources, engine version,
#                    accepted/deferred), read by operations only, unknown id = generic 404, append-only
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
export EMAIL_PORT="${EMAIL_PORT:-9131}"
export AUDIT_PORT="${AUDIT_PORT:-8290}" ORIGINATION_PORT="${ORIGINATION_PORT:-8291}"
export TOXIPROXY_PORT="${TOXIPROXY_PORT:-8494}" MOTO_PORT="${MOTO_PORT:-5120}"
AUDIT="http://localhost:$AUDIT_PORT"
ORIGINATION="http://localhost:$ORIGINATION_PORT"
RUN_ID="r$(date +%s)"

secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
DOCUMENT_LINK_SECRET="$(secret 48)"
export PG_PASSWORD MOTO_USER_PASSWORD DOCUMENT_LINK_SECRET

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
$COMPOSE up -d --build --wait postgres-audit postgres-origination email redis openfinance opendata kyc toxiproxy moto

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
ADVISOR_TOKEN="$(token asesor.local@example.com)"
PARTNER_TOKEN="$(token socio.local@example.com)"

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

# --- HU-W1.2-03 ---------------------------------------------------------------

# toxi <proxy> <true|false>: enables or disables a source behind Toxiproxy.
toxi() { curl -sf -X POST "http://localhost:$TOXIPROXY_PORT/proxies/$1" -H 'Content-Type: application/json' -d "{\"enabled\":$2}" >/dev/null; }
sources_up() { toxi openfinance true; toxi opendata true; toxi kyc true; }
psql_orig() { $COMPOSE exec -T postgres-origination psql -U origination -d origination -v ON_ERROR_STOP=1 -tA -c "$1" 2>&1 || true; }
# mails_with_key <key>: messages the e-mail stub received under the idempotency key.
mails_with_key() { curl -sf "http://localhost:$EMAIL_PORT/messages" | jq --arg k "$1" '[.messages[] | select(.idempotency_key == $k)] | length'; }

# consent <customer>: records the consent in audit and sets CONSENT_ID.
consent() {
  call POST "$AUDIT/v1/audit/consents" "$(grant_body "$1" "app-$1" "$SHOWN")"
  CONSENT_ID="$(field .consent_id)"
}
offer_body() { # <customer> <consent id> <email>
  cat <<JSON
{"customer_id":"$1","consent_id":"$2","partner_id":"partner-e2e","application_id":"app-$1",
 "document_id":"1020304050","full_name":"Ana María Pérez","email":"$3","birth_date":"1985-03-14",
 "insured_amount":250000000,"term_months":120}
JSON
}
# new_offer <customer> <email>: grants the consent and generates the offer (OFFER_ID, STATUS, BODY).
new_offer() {
  consent "$1"
  call POST "$ORIGINATION/v1/offers" "$(offer_body "$1" "$CONSENT_ID" "$2")"
  OFFER_ID="$(field .offer_id)"
}
# wait_offer_status <offer id> <status> [seconds]: polls GET until the offer has the status.
wait_offer_status() {
  local i
  for i in $(seq 1 "${3:-60}"); do
    call GET "$ORIGINATION/v1/offers/$1" "" "$INSURED_TOKEN"
    [[ "$(field .status)" == "$2" ]] && return 0
    sleep 1
  done
  return 1
}

echo "== [$(stamp)] 8. HU-W1.2-03 CA-1/CA-2: complete offer, 24 h, claimed on first access, persisted, immutable"
C1="cust-offer-$RUN_ID"
new_offer "$C1" "ana-$RUN_ID-1@example.com"
check "POST /v1/offers answers 201" 201 "$STATUS"
OFFER1="$OFFER_ID"
check "the offer is normal" normal "$(field .mode)"
check "the offer is active" active "$(field .status)"
check "the engine version is v1" v1 "$(field .rater_version)"
check "two coverages" 2 "$(field '.terms.coverages | length')"
check "the conditions are listed" validity_24h "$(field '.conditions[0]')"
CREATED_AT="$(field .created_at)"
EXPIRES_AT="$(field .expires_at)"
check "valid for 24 hours" 86400 "$(jq -n --arg a "$CREATED_AT" --arg b "$EXPIRES_AT" '(($b | sub("\\.[0-9]+Z$"; "Z") | fromdate) - ($a | sub("\\.[0-9]+Z$"; "Z") | fromdate))')"
check "the response has no score, rules or document" 0 "$(grep -ciE 'score|"rules"|1020304050' <<<"$BODY" || true)"
call GET "$ORIGINATION/v1/offers/$OFFER1"
check "reading without a token: 401" 401 "$STATUS"
call GET "$ORIGINATION/v1/offers/$OFFER1" "" "$INSURED_TOKEN"
check "the insured opens the offer (claim): 200" 200 "$STATUS"
check "the offer can be accepted" true "$(field .can_accept)"
call GET "$ORIGINATION/v1/offers/$OFFER1" "" "$PARTNER_TOKEN"
check "another role (partner) is refused: 403" 403 "$STATUS"
call GET "$ORIGINATION/v1/offers/$OFFER1" "" "$ADVISOR_TOKEN"
check "an advisor reads it without claiming: 200" 200 "$STATUS"
OWNER="$(psql_orig "SELECT owner_id FROM offer_owner WHERE offer_id = '$OFFER1'")"
check "the owner was persisted" 1 "$([[ -n "$OWNER" ]] && echo 1 || echo 0)"
check "the row keeps the profile copy" standard "$(psql_orig "SELECT profile->>'category' FROM offer WHERE offer_id = '$OFFER1'")"
check "the row keeps the rules" 5 "$(psql_orig "SELECT jsonb_array_length(rules) FROM offer WHERE offer_id = '$OFFER1'")"
check "the row keeps the engine version" v1 "$(psql_orig "SELECT rater_version FROM offer WHERE offer_id = '$OFFER1'")"
for stmt in "UPDATE offer SET premium_minor = 1" "DELETE FROM offer" "TRUNCATE offer, offer_owner"; do
  if grep -q "append-only" <<<"$(psql_orig "$stmt")"; then
    check "$stmt is rejected" rejected rejected
  else
    check "$stmt is rejected" rejected accepted
  fi
done

echo "== [$(stamp)] 9. HU-W1.2-03 CA-3: provisional offers say why"
toxi openfinance false
C2="cust-base-$RUN_ID"
new_offer "$C2" "ana-$RUN_ID-2@example.com"
check "Open Finance down: the offer is still produced (201)" 201 "$STATUS"
check "provisional" provisional "$(field .mode)"
check "priced with the base actuarial profile" base_actuarial_profile "$(field .provisional_reason)"
check "no profile date" null "$(field .profile_date)"
check "the unavailable source is named" open_finance "$(field .unavailable_source)"
check "the requote condition is listed" provisional_requote_when_source_restored "$(field '.conditions | last')"
BASE_OFFER="$OFFER_ID"
sources_up
toxi opendata false
toxi kyc false
C3="cust-cache-$RUN_ID"
new_offer "$C3" "ana-$RUN_ID-3@example.com"
check "with only Open Finance the offer is normal" normal "$(field .mode)"
toxi openfinance false
call POST "$ORIGINATION/v1/offers" "$(offer_body "$C3" "$CONSENT_ID" "ana-$RUN_ID-3@example.com")"
check "partial cached profile: provisional offer (201)" 201 "$STATUS"
check "priced with the cached profile" cached_profile "$(field .provisional_reason)"
check "and its date is reported" true "$([[ "$(field .profile_date)" != null ]] && echo true || echo false)"
CACHED_OFFER="$(field .offer_id)"
toxi opendata true
toxi kyc true

echo "== [$(stamp)] 10. HU-W1.2-03 CA-4: the source returns; replace, notify once, respect the accepted"
# Open Finance is still down: these two provisional offers are created in the same window.
C4="cust-requote-$RUN_ID"
new_offer "$C4" "ana-$RUN_ID-4@example.com"
check "provisional offer to be replaced" provisional "$(field .mode)"
OLD="$OFFER_ID"
C5="cust-accepted-$RUN_ID"
new_offer "$C5" "ana-$RUN_ID-5@example.com"
KEPT="$OFFER_ID"
call GET "$ORIGINATION/v1/offers/$OLD" "" "$INSURED_TOKEN"
check "the insured claims the offer to replace" 200 "$STATUS"
call GET "$ORIGINATION/v1/offers/$KEPT" "" "$INSURED_TOKEN"
call POST "$ORIGINATION/v1/offers/$KEPT/acceptance" '{"channel":"web"}' "$INSURED_TOKEN"
check "the provisional offer is accepted (202)" 202 "$STATUS"
sleep 3
call GET "$ORIGINATION/v1/offers/$OLD" "" "$INSURED_TOKEN"
check "while the source is down the offer stays active" active "$(field .status)"
sources_up
if wait_offer_status "$OLD" superseded 90; then
  check "the provisional offer was replaced" superseded "$(field .status)"
else
  check "the provisional offer was replaced" superseded "$(field .status)"
fi
NEW="$(field .superseded_by)"
check "it points to the new offer" true "$([[ "$NEW" == off-* ]] && echo true || echo false)"
check "the old one can no longer be accepted" false "$(field .can_accept)"
call POST "$ORIGINATION/v1/offers/$OLD/acceptance" '{"channel":"web"}' "$INSURED_TOKEN"
check "accepting the replaced offer: 422" 422 "$STATUS"
check "the error is offer_expired" offer_expired "$(field .error)"
call GET "$ORIGINATION/v1/offers/$NEW" "" "$INSURED_TOKEN"
check "the owner reads the new offer without claiming again (200)" 200 "$STATUS"
check "the new offer is normal" normal "$(field .mode)"
call POST "$ORIGINATION/v1/offers/$NEW/acceptance" '{"channel":"web"}' "$INSURED_TOKEN"
check "the owner accepts the new offer (202)" 202 "$STATUS"
for _ in $(seq 1 15); do [[ "$(mails_with_key "offer-requoted:$OLD")" == 1 ]] && break; sleep 1; done
check "the customer got the notice" 1 "$(mails_with_key "offer-requoted:$OLD")"
sleep 4
check "and only one" 1 "$(mails_with_key "offer-requoted:$OLD")"
call GET "$ORIGINATION/v1/offers/$KEPT" "" "$INSURED_TOKEN"
check "an accepted provisional offer is not replaced" null "$(field .superseded_by)"
check "and gets no notice" 0 "$(mails_with_key "offer-requoted:$KEPT")"
call GET "$ORIGINATION/v1/offers/$BASE_OFFER" "" "$ADVISOR_TOKEN"
check "an unaccepted provisional offer of another customer is replaced too" superseded "$(field .status)"
check "a replaced offer cannot be renewed" 409 "$(call POST "$ORIGINATION/v1/offers/$OLD/renewal" "" "$INSURED_TOKEN"; echo "$STATUS")"

echo "== [$(stamp)] 11. HU-W1.2-03 CA-5: an expired offer is refused and renewed"
# A seeded offer: a copy of the first one with its validity in the past.
SEEDED="off-seed$RUN_ID"
psql_orig "INSERT INTO offer (offer_id, customer_id, channel, quote_id, consent_id, partner_id, application_id, applicant, birth_date,
    age_at_quote, terms, premium_minor, currency, conditions, mode, profile_source, provisional_reason, profile_date, unavailable_source,
    profile, rules, rater_version, created_at, expires_at)
  SELECT '$SEEDED', customer_id, channel, quote_id, consent_id, partner_id, application_id, applicant, birth_date,
    age_at_quote, terms, premium_minor, currency, conditions, mode, profile_source, provisional_reason, profile_date, unavailable_source,
    profile, rules, rater_version, now() - interval '48 hours', now() - interval '24 hours'
  FROM offer WHERE offer_id = '$OFFER1'" >/dev/null
call GET "$ORIGINATION/v1/offers/$SEEDED" "" "$INSURED_TOKEN"
check "the expired offer is readable (200)" 200 "$STATUS"
check "its status is expired" expired "$(field .status)"
check "it can be renewed" true "$(field .can_renew)"
check "it cannot be accepted" false "$(field .can_accept)"
call POST "$ORIGINATION/v1/offers/$SEEDED/acceptance" '{"channel":"web"}' "$INSURED_TOKEN"
check "accepting an expired offer: 422" 422 "$STATUS"
check "the error is offer_expired" offer_expired "$(field .error)"
call POST "$ORIGINATION/v1/offers/$SEEDED/renewal" "" "$PARTNER_TOKEN"
check "only an insured renews: 403" 403 "$STATUS"
call POST "$ORIGINATION/v1/offers/$SEEDED/renewal" "" "$INSURED_TOKEN"
check "the renewal is created (201)" 201 "$STATUS"
RENEWAL="$(field .offer_id)"
check "the renewal is active" active "$(field .status)"
call POST "$ORIGINATION/v1/offers/$SEEDED/renewal" "" "$INSURED_TOKEN"
check "renewing again is idempotent (200)" 200 "$STATUS"
check "and returns the same renewal" "$RENEWAL" "$(field .offer_id)"
call POST "$ORIGINATION/v1/offers/$OFFER1/renewal" "" "$INSURED_TOKEN"
check "a valid offer is not renewed: 409" 409 "$STATUS"
check "the error is offer_still_valid" offer_still_valid "$(field .error)"
call POST "$ORIGINATION/v1/offers/$RENEWAL/acceptance" '{"channel":"web"}' "$INSURED_TOKEN"
check "the renewal can be accepted (202)" 202 "$STATUS"

echo "== [$(stamp)] 12. HU-W1.2-03: origination restarts; offers and owners persist"
OWNER_BEFORE="$(psql_orig "SELECT owner_id FROM offer_owner WHERE offer_id = '$OFFER1'")"
$COMPOSE restart origination >/dev/null
wait_healthy "$ORIGINATION"
call GET "$ORIGINATION/v1/offers/$OFFER1" "" "$INSURED_TOKEN"
check "the offer is still there (200)" 200 "$STATUS"
check "with the same price" "$OFFER1" "$(field .offer_id)"
check "and the same owner" "$OWNER_BEFORE" "$(psql_orig "SELECT owner_id FROM offer_owner WHERE offer_id = '$OFFER1'")"
call GET "$ORIGINATION/v1/offers/$OLD" "" "$INSURED_TOKEN"
check "the replacement is still recorded" superseded "$(field .status)"

echo "== [$(stamp)] 13. HU-W1.2-03 security: consent and exposure"
call GET "$AUDIT/v1/audit/consent-verifications?customer_id=$C1"
OTHERS_CONSENT="$(field .consent_id)"
C6="cust-nocons-$RUN_ID"
BEFORE="$(source_connections)"
call POST "$ORIGINATION/v1/offers" "$(offer_body "$C6" "$OTHERS_CONSENT" "ana-$RUN_ID-6@example.com")"
check "an offer without consent: 422" 422 "$STATUS"
check "consent_required" consent_required "$(field .error)"
check "no request reached the stubs" "$BEFORE" "$(source_connections)"
call POST "$ORIGINATION/v1/offers" "$(offer_body "$C4" "$OTHERS_CONSENT" "ana-$RUN_ID-4@example.com")"
check "another customer's consent id: 422" 422 "$STATUS"
check "consent_mismatch" consent_mismatch "$(field .error)"
call POST "$ORIGINATION/v1/offers" '{"customer_id":"x"}'
check "invalid fields: 400" 400 "$STATUS"
check "validation_failed" validation_failed "$(field .error)"
$COMPOSE stop audit >/dev/null
$COMPOSE restart origination >/dev/null
wait_healthy "$ORIGINATION"
BEFORE="$(source_connections)"
call POST "$ORIGINATION/v1/offers" "$(offer_body "$C1" "$CONSENT_ID" "ana-$RUN_ID-1@example.com")"
check "audit stopped: 503" 503 "$STATUS"
check "consent_unverifiable" consent_unverifiable "$(field .error)"
check "no request reached the stubs" "$BEFORE" "$(source_connections)"
$COMPOSE start audit >/dev/null
wait_healthy "$AUDIT"

echo "== [$(stamp)] 14. HU-W4.1-01: every offer priced is in the audit record, read-only"
call GET "$AUDIT/v1/audit/decisions/$OFFER1" "" "$OPERATIONS_TOKEN"
check "operations reads the audit record of a normal offer (200)" 200 "$STATUS"
check "the record is the offer" "$OFFER1" "$(field .offer_id)"
check "a normal offer is an accepted risk" accepted "$(field .decision)"
check "the engine version is recorded" v1 "$(field .rater_version)"
check "the request time is recorded in UTC" true "$([[ "$(field .requested_at)" == *Z ]] && echo true || echo false)"
check "the five rules are recorded in execution order" "1,2,3,4,5" "$(field '[.rules[].order] | join(",")')"
check "starting with the base rate" base_monthly_rate "$(field '.rules[0].code')"
check "the sources consulted are recorded" "kyc,open_data,open_finance" "$(field '[.sources[].source] | sort | join(",")')"
check "the premium is the one of the offer (minor units)" "$(psql_orig "SELECT premium_minor FROM offer WHERE offer_id = '$OFFER1'")" "$(field '(.premium * 100) | round')"
call GET "$AUDIT/v1/audit/decisions/$BASE_OFFER" "" "$OPERATIONS_TOKEN"
check "a provisional offer is deferred" deferred "$(field .decision)"
check "with its reason" base_actuarial_profile "$(field .decision_reason)"
call GET "$AUDIT/v1/audit/decisions/$CACHED_OFFER" "" "$OPERATIONS_TOKEN"
check "the cached-profile offer is deferred too" cached_profile "$(field .decision_reason)"
call GET "$AUDIT/v1/audit/decisions/$NEW" "" "$OPERATIONS_TOKEN"
check "the offer that replaced a provisional one is recorded (accepted)" accepted "$(field .decision)"
call GET "$AUDIT/v1/audit/decisions/$RENEWAL" "" "$OPERATIONS_TOKEN"
check "the renewal is recorded" 200 "$STATUS"
call GET "$AUDIT/v1/audit/decisions/off-does-not-exist" "" "$OPERATIONS_TOKEN"
check "an unknown identifier: 404" 404 "$STATUS"
check "with a generic error that exposes nothing internal" decision_not_found "$(field .error)"
call GET "$AUDIT/v1/audit/decisions/$OFFER1"
check "without token: 401" 401 "$STATUS"
call GET "$AUDIT/v1/audit/decisions/$OFFER1" "" "$INSURED_TOKEN"
check "as insured: 403" 403 "$STATUS"
call GET "$AUDIT/v1/audit/decisions/$OFFER1" "" "$ADVISOR_TOKEN"
check "as advisor: 403 (only operations)" 403 "$STATUS"
call PUT "$AUDIT/v1/audit/decisions/$OFFER1" '{"premium":1}' "$OPERATIONS_TOKEN"
check "the record cannot be changed through the API (405)" 405 "$STATUS"
for stmt in "UPDATE underwriting_decision SET premium = 1" "DELETE FROM underwriting_decision" "TRUNCATE underwriting_decision"; do
  OUT="$(psql_audit "$stmt")"
  check "'$stmt' fails in the database" true "$([[ "$OUT" == *"append-only"* ]] && echo true || echo false)"
done
check "one record per offer (recording again changes nothing)" 1 "$(psql_audit "SELECT count(*) FROM underwriting_decision WHERE offer_id = '$OFFER1'" | grep -Eo '^ *[0-9]+$' | tr -d ' ')"

if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-W1.2 end-to-end scenarios FAILED: $FAILS check(s) (run $RUN_ID)." >&2
  exit 1
fi
echo "== [$(stamp)] HU-W1.2 end-to-end scenarios passed (run $RUN_ID)."
