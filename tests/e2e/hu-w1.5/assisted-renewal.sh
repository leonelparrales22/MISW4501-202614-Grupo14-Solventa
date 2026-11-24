#!/usr/bin/env bash
# Runs the end-to-end scenarios of the assisted renewal (HU-W1.5-03) with
# Docker, against the real audit and origination services (PostgreSQL of each,
# Redis, the Open Finance / Open Data / KYC stubs behind Toxiproxy, the payment
# gateway and e-mail stubs, Moto as Cognito). The policies are bought through
# the real journey: consent in audit, POST /v1/offers, the insured opens and
# accepts the offer, hands over the card 4111 and the charge is confirmed.
#
#   R1  CA-1    the advisor requotes with the engine v1 (discount, sources ok)
#   R2  CA-2    no acceptance, an unknown method, a future date or no reference: 422, nothing stored
#   R3  CA-3    formalizing charges and issues the renewed policy (renewal_of, continuous coverage, PDF,
#               e-mail of the certificate, monitor channel=renewal)
#   R4  CA-4    after a restart: the advisor (name of Moto), the date, the acceptance and the requote applied
#   R5  CA-5    Open Finance disabled in Toxiproxy: provisional, cache, profile_date, last_profile_in_use;
#               enabled again: normal
#   R5b CA-5    no cached profile (deleted in Redis) and Open Finance down: base_actuarial, personalization_unavailable
#   R6  roles   401 without a token; 403 for the insured and operations
#   R7  one outcome per period: two advisors at once 201 and 409; the insured gets the same renewal, one charge
#   R8  minimization: answers without score, token, code or the holder's e-mail; the logs of the
#               assisted renewal without document, e-mail, reference, code or token
#   R9  D-9     the code goes to the holder's e-mail (read from the stub), a wrong one 422, the right one 201,
#               a fourth code 429
#   R10 D-8     operations revokes the consent in audit: 422 consent_required and no source queried;
#               audit stopped: 503 consent_unverifiable and no source queried
#   R11 D-10    a policy whose charge has no payment method (INSERT in the local PostgreSQL, the real journey
#               never produces it): blocked_reason payment_method_missing and 409 on formalizing
#   R12 01·D-3 (a+) the gateway restarts and forgets its tokens: the charge of the renewal is declined; the view
#               shows failed, payment_rejected, last_failure_at and retry_until; another card renews it
#   R13 D-13 (c) a policy born in the assisted channel (INSERT of its offer, charge and policy in the local
#               PostgreSQL: the journey of HU-W1.1-03 needs a customer account): consent_mode
#               advisor_affirmation; without consent_affirmed 422 and neither audit nor the sources are asked;
#               with it 201 advisor_affirmed; formalizing renews it
#
# "No source queried" is checked on Toxiproxy (every call to a stub opens a
# connection through its proxy and the proxy logs it) and "audit is not asked"
# on the request log of audit. The script exits with a non-zero code when any
# check fails. The environment is always removed at the end (down -v).
# Requirements: Docker (compose v2), curl, jq and the AWS CLI (only against the
# local Moto, as tests/security/ha-s01/local does). The secrets (PostgreSQL,
# Moto users, the document link) are random, generated here and never printed.
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
export AUDIT_PORT="${AUDIT_PORT:-8590}" ORIGINATION_PORT="${ORIGINATION_PORT:-8591}"
export TOXIPROXY_PORT="${TOXIPROXY_PORT:-8594}" MOTO_PORT="${MOTO_PORT:-5190}"
export EMAIL_PORT="${EMAIL_PORT:-9191}" GATEWAY_PORT="${GATEWAY_PORT:-9194}"
AUDIT="http://localhost:$AUDIT_PORT"
ORIGINATION="http://localhost:$ORIGINATION_PORT"
GATEWAY="http://localhost:$GATEWAY_PORT"
MAIL="http://localhost:$EMAIL_PORT"
RUN_ID="r$(date +%s)"

secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
DOCUMENT_LINK_SECRET="$(secret 48)"
export PG_PASSWORD MOTO_USER_PASSWORD DOCUMENT_LINK_SECRET

umask 077
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
check_true() { # <description> <0 when true>
  if [[ "$2" == 0 ]]; then
    echo "  ok    $1"
  else
    echo "  FAIL  $1" >&2
    FAILS=$((FAILS + 1))
  fi
}

# call <method> <url> [json body] [bearer token]: sets STATUS and BODY. The
# token goes through a private header file, never through the command line.
call() {
  : >"$WORK/header"
  printf 'User-Agent: hu-w1.5-e2e\n' >>"$WORK/header"
  [[ -n "${4:-}" ]] && printf 'Authorization: Bearer %s\n' "$4" >>"$WORK/header"
  if [[ -n "${3:-}" ]]; then
    printf 'Content-Type: application/json\n' >>"$WORK/header"
    STATUS="$(printf '%s' "$3" | curl -s --max-time 20 -o "$WORK/body" -w '%{http_code}' -X "$1" -H @"$WORK/header" --data-binary @- "$2" || true)"
  else
    STATUS="$(curl -s --max-time 20 -o "$WORK/body" -w '%{http_code}' -X "$1" -H @"$WORK/header" "$2" || true)"
  fi
  BODY="$(tr -d '\r' <"$WORK/body")"
  printf '%s\n' "$BODY" >>"$WORK/answers"
}
field() { jq -r "$1" <<<"$BODY" 2>/dev/null || echo "<not json>"; }

wait_healthy() { # <url>
  local i
  for i in $(seq 1 120); do
    curl -sf --max-time 2 "$1/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "$1 did not become healthy" >&2
  $COMPOSE logs audit origination | tail -40 >&2
  return 1
}

# source_connections: connections the stubs received through Toxiproxy so far.
source_connections() { $COMPOSE logs --no-log-prefix toxiproxy 2>/dev/null | grep -c 'Accepted client' || true; }
# audit_verifications: consent verifications audit answered so far.
audit_verifications() { $COMPOSE logs --no-log-prefix audit 2>/dev/null | grep -c 'consent-verifications' || true; }
toxi() { curl -sf -X POST "http://localhost:$TOXIPROXY_PORT/proxies/$1" -H 'Content-Type: application/json' -d "{\"enabled\":$2}" >/dev/null; }
psql_orig() { $COMPOSE exec -T postgres-origination psql -U origination -d origination -v ON_ERROR_STOP=1 -tA -c "$1" 2>&1 | tr -d '\r'; }

echo "== [$(stamp)] Starting PostgreSQL, Redis, the stubs behind Toxiproxy, the gateway, the e-mail and Moto"
$COMPOSE up -d --build --wait postgres-audit postgres-origination email payment-gateway redis openfinance opendata kyc toxiproxy moto

echo "== [$(stamp)] Seeding the local Cognito (Moto)"
SEED_OUT=""
# The published port of Moto can lag behind its health check on Docker Desktop.
for attempt in $(seq 1 15); do
  if SEED_OUT="$(env -u MSYS_NO_PATHCONV MOTO_URL="http://localhost:$MOTO_PORT" bash ../../security/ha-s01/local/seed.sh 2>&1 | tr -d '\r')"; then
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

# A second advisor without the claim name, only in the local Moto, for R7.
(
  unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN
  export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-2 AWS_DEFAULT_REGION=us-east-2 AWS_PAGER=""
  idp() { MSYS_NO_PATHCONV=1 aws cognito-idp --endpoint-url "http://localhost:$MOTO_PORT" --region us-east-2 --output text "$@" | tr -d '\r'; }
  pool="${AUTH_ISSUER##*/}"
  idp admin-create-user --user-pool-id "$pool" --username asesor2.local@example.com \
    --user-attributes Name=email,Value=asesor2.local@example.com Name=email_verified,Value=true --message-action SUPPRESS >/dev/null
  idp admin-set-user-password --user-pool-id "$pool" --username asesor2.local@example.com --password "$MOTO_USER_PASSWORD" --permanent >/dev/null
  idp admin-add-user-to-group --user-pool-id "$pool" --username asesor2.local@example.com --group-name advisor >/dev/null
)

token() { # <username>
  curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
    -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$WEB_CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"$1\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken'
}
OPERATIONS_TOKEN="$(token operaciones.local@example.com)"
INSURED_TOKEN="$(token asegurado.local@example.com)"
ADVISOR_TOKEN="$(token asesor.local@example.com)"
ADVISOR2_TOKEN="$(token asesor2.local@example.com)"

echo "== [$(stamp)] Starting audit and origination"
$COMPOSE up -d --build audit origination
wait_healthy "$AUDIT"
wait_healthy "$ORIGINATION"

call GET "$AUDIT/v1/audit/consent-terms/current"
TERMS_VERSION="$(field .version)"
TERMS_SOURCES="$(field '[.sources[].code] | map("\"" + . + "\"") | join(",")')"

# seed <tag> <holder name> <holder e-mail> <document> <term months>: buys a policy
# through the real journey; sets POLICY, CUSTOMER, CONSENT, ACCEPTANCE, OFFER.
seed() {
  CUSTOMER="cust-$1-$RUN_ID"
  local app="app-$1-$RUN_ID" i
  call POST "$AUDIT/v1/audit/consents" "{\"customer_id\":\"$CUSTOMER\",\"document_id\":\"$4\",\"full_name\":\"$2\",
    \"terms_version\":\"$TERMS_VERSION\",\"accepted_sources\":[$TERMS_SOURCES],\"accepted_data_policy\":true,
    \"channel\":\"embedded\",\"partner_id\":\"partner-e2e\",\"application_id\":\"$app\"}"
  [[ "$STATUS" == 201 ]] || { echo "consent of $1: HTTP $STATUS $BODY" >&2; exit 1; }
  CONSENT="$(field .consent_id)"
  call POST "$ORIGINATION/v1/offers" "{\"customer_id\":\"$CUSTOMER\",\"consent_id\":\"$CONSENT\",\"partner_id\":\"partner-e2e\",
    \"application_id\":\"$app\",\"document_id\":\"$4\",\"full_name\":\"$2\",\"email\":\"$3\",\"birth_date\":\"1985-03-14\",
    \"insured_amount\":68000000,\"term_months\":$5}"
  [[ "$STATUS" == 201 ]] || { echo "offer of $1: HTTP $STATUS $BODY" >&2; exit 1; }
  OFFER="$(field .offer_id)"
  call GET "$ORIGINATION/v1/offers/$OFFER" "" "$INSURED_TOKEN"
  call POST "$ORIGINATION/v1/offers/$OFFER/acceptance" '{"channel":"web"}' "$INSURED_TOKEN"
  [[ "$STATUS" == 202 ]] || { echo "acceptance of $1: HTTP $STATUS $BODY" >&2; exit 1; }
  ACCEPTANCE="$(field .acceptance_id)"
  for i in $(seq 1 60); do
    call GET "$ORIGINATION/v1/acceptances/$ACCEPTANCE/charge" "" "$INSURED_TOKEN"
    [[ "$STATUS" == 200 ]] && break
    sleep 1
  done
  [[ "$STATUS" == 200 ]] || { echo "charge of $1 not created: HTTP $STATUS $BODY" >&2; exit 1; }
  pay "$ACCEPTANCE" 4111111111111111
  for i in $(seq 1 60); do
    call GET "$ORIGINATION/v1/acceptances/$ACCEPTANCE/charge" "" "$INSURED_TOKEN"
    [[ "$(field .status)" == confirmed && "$(field .policy_id)" != null ]] && break
    sleep 1
  done
  POLICY="$(field .policy_id)"
  [[ "$POLICY" != null && -n "$POLICY" ]] || { echo "policy of $1 not issued: $BODY" >&2; exit 1; }
  for i in $(seq 1 30); do
    call GET "$ORIGINATION/v1/policies/$POLICY/assisted-renewal" "" "$ADVISOR_TOKEN"
    [[ "$STATUS" == 200 ]] && break
    sleep 1
  done
  echo "  seeded $1: $POLICY ($2)"
}
# pay <acceptance or charge id> <card number>: tokenises the card at the gateway
# (as the browser does) and hands it over as the insured.
pay() {
  call POST "$GATEWAY/tokens" "{\"number\":\"$2\",\"holder\":\"Prueba E2E\"}"
  local tok brand last4
  tok="$(field .token)" brand="$(field .brand)" last4="$(field .last4)"
  call POST "$ORIGINATION/v1/acceptances/$1/charge/payment-method" \
    "{\"method\":\"card\",\"payment_token\":\"$tok\",\"brand\":\"$brand\",\"last4\":\"$last4\",\"installments\":1}" "$INSURED_TOKEN"
}

ar() { echo "$ORIGINATION/v1/policies/$1/assisted-renewal"; }
requote() { call POST "$(ar "$1")/requotes" "${2:-{\}}" "${3:-$ADVISOR_TOKEN}"; }
NOW_RFC3339() { date -u +%Y-%m-%dT%H:%M:%SZ; }
formalize() { # <policy> <requote> <method> <reference> [code] [token]
  call POST "$(ar "$1")/formalization" \
    "{\"requote_id\":\"$2\",\"acceptance\":{\"method\":\"$3\",\"accepted_at\":\"$(NOW_RFC3339)\",\"reference\":\"$4\"},\"confirmation_code\":\"${5:-}\"}" \
    "${6:-$ADVISOR_TOKEN}"
}
wait_view() { # <policy> <jq condition>
  local i
  for i in $(seq 1 60); do
    call GET "$(ar "$1")" "" "$ADVISOR_TOKEN"
    [[ "$STATUS" == 200 && "$(field "$2")" == true ]] && return 0
    sleep 1
  done
  return 1
}

echo "== [$(stamp)] Buying the policies through the real journey"
seed p1 "Carolina Perez Ruiz" "carolina.e2e@example.com" 1020345678 12
P1="$POLICY"
seed p2 "Mateo Rios Lara" "mateo.e2e@example.com" 1030456789 12
P2="$POLICY" C2="$CUSTOMER"
seed p3 "Valentina Soto Gil" "valentina.e2e@example.com" 1040567890 12
P3="$POLICY"
seed p4 "Andres Mora Paz" "andres.e2e@example.com" 1050678901 12
P4="$POLICY" ACC4="$ACCEPTANCE" OFFER4="$OFFER"
seed p5 "Lucia Vega Rey" "lucia.e2e@example.com" 1060789012 12
P5="$POLICY" CONSENT5="$CONSENT"
seed p6 "Sofia Duarte Leon" "sofia.e2e@example.com" 1070890123 12
P6="$POLICY"

echo "== [$(stamp)] R1. CA-1: the advisor requotes with the engine"
call GET "$(ar "$P1")" "" "$ADVISOR_TOKEN"
check "GET assisted-renewal answers 200" 200 "$STATUS"
check "it can be requoted" true "$(field .can_requote)"
check "the consent of an embedded policy is verified with audit" audit "$(field .consent_mode)"
requote "$P1"
check "the requote answers 201" 201 "$STATUS"
R1="$(field .requote_id)"
check "the engine is v1" v1 "$(field .rating_version)"
check "the good-experience discount is 8 %" 0.08 "$(field .discount_rate)"
check "it was requoted" true "$(field .requoted)"
check "the mode is normal" normal "$(field .mode)"
check "every source answered" true "$(field '[.sources[].status] | length > 0 and all(. == "ok")')"
check "the renewed coverage starts when the current one ends" "$(field .current.coverage_end)" "$(field .renewal.coverage_start)"
RENEWED_PREMIUM="$(field .renewal.premium)"

echo "== [$(stamp)] R2. CA-2: nothing is formalized without a valid acceptance"
call POST "$(ar "$P1")/formalization" "{\"requote_id\":\"$R1\"}" "$ADVISOR_TOKEN"
check "without acceptance: 422 validation_failed" "422 validation_failed" "$STATUS $(field .error)"
formalize "$P1" "$R1" fax GRAB-1
check "an unknown method: 422" 422 "$STATUS"
call POST "$(ar "$P1")/formalization" \
  "{\"requote_id\":\"$R1\",\"acceptance\":{\"method\":\"recorded_call\",\"accepted_at\":\"2099-01-01T00:00:00Z\",\"reference\":\"GRAB-1\"}}" "$ADVISOR_TOKEN"
check "a date in the future: 422 on acceptance.accepted_at" "acceptance.accepted_at" "$(field '.fields[0].field')"
formalize "$P1" "$R1" remote_signature ""
check "a signature without reference: 422 on acceptance.reference" "acceptance.reference" "$(field '.fields[0].field')"
check "no renewal was stored" 0 "$(psql_orig "SELECT count(*) FROM renewal WHERE policy_id = '$P1'")"

echo "== [$(stamp)] R3. CA-3: formalizing charges and issues the renewed policy"
formalize "$P1" "$R1" recorded_call GRAB-2026-88931
check "the formalization answers 201" 201 "$STATUS"
check "the renewal is the advisor's" advisor "$(field .renewal.channel)"
CHARGE1="$(field .renewal.charge_id)"
wait_view "$P1" '.renewal.status == "renewed"' || true
check "the renewal is renewed" renewed "$(field .renewal.status)"
NEW1="$(field .renewal.new_policy_id)"
check "the charge is confirmed with the new policy" "confirmed $NEW1" "$(field .charge.status) $(field .charge.policy_id)"
check "the new policy renews P1" "$P1" "$(psql_orig "SELECT renewal_of FROM policy WHERE policy_id = '$NEW1'")"
check "the coverage is continuous" t "$(psql_orig "SELECT n.coverage_start = o.coverage_end FROM policy n JOIN policy o ON o.policy_id = n.renewal_of WHERE n.policy_id = '$NEW1'")"
check "the requoted premium was charged" t "$(psql_orig "SELECT amount_minor = round($RENEWED_PREMIUM * 100) FROM charge_attempt WHERE acceptance_id = '$CHARGE1'")"
ISSUED=no
for _ in $(seq 1 60); do
  call GET "$ORIGINATION/v1/policies/$NEW1/issuance-status" "" "$ADVISOR_TOKEN"
  [[ "$(field .status)" == completed ]] && { ISSUED=yes; break; }
  sleep 1
done
check "the certificate of the renewed policy was issued" yes "$ISSUED"
call GET "$ORIGINATION/v1/policies/$NEW1/contract-document" "" "$ADVISOR_TOKEN"
check "the PDF of the certificate (200)" 200 "$STATUS"
call GET "$MAIL/messages?to=carolina.e2e@example.com"
check "the holder got the e-mail of the certificate" true "$(field "[.messages[] | select(.body | contains(\"$NEW1\"))] | length > 0")"
call GET "$ORIGINATION/v1/issuances?channel=renewal" "" "$OPERATIONS_TOKEN"
check "the monitor lists it with the channel renewal" true "$(field "[.items[] | select(.acceptance_id == \"$CHARGE1\")] | length == 1")"

echo "== [$(stamp)] R4. CA-4: the record survives a restart"
$COMPOSE restart origination >/dev/null
wait_healthy "$ORIGINATION"
call GET "$(ar "$P1")" "" "$ADVISOR_TOKEN"
check "the advisor's name (Moto)" "Laura Gómez" "$(field .formalization.advisor_name)"
check "the method and the reference" "recorded_call GRAB-2026-88931" "$(field .formalization.acceptance.method) $(field .formalization.acceptance.reference)"
check "the requote applied" "$R1" "$(field .formalization.requote_id)"
check "the date" true "$(field '.formalization.formalized_at != null')"
check "the conditions applied" "$RENEWED_PREMIUM" "$(field .requote.renewal.premium)"

echo "== [$(stamp)] R5. CA-5: Open Finance down, the last cached profile"
requote "$P2"
check "a requote with the sources up is normal" normal "$(field .mode)"
toxi openfinance false
requote "$P2"
check "with Open Finance down the requote answers 201" 201 "$STATUS"
check "it is provisional from the cache" "provisional cache" "$(field .mode) $(field .source)"
check "the advisor is told" last_profile_in_use "$(field .notice)"
check "with the date of the profile" true "$(field '.profile_date != null')"
check "Open Finance is the source missing" open_finance "$(field .unavailable_source)"

echo "== [$(stamp)] R5b. CA-5: no cached profile, the base actuarial profile"
$COMPOSE exec -T redis redis-cli DEL "risk_profile:$C2" >/dev/null
requote "$P2"
check "without a cached profile: base_actuarial" "base_actuarial personalization_unavailable" "$(field .source) $(field .notice)"
toxi openfinance true
requote "$P2"
check "the sources are back: normal" normal "$(field .mode)"

echo "== [$(stamp)] R6. Roles"
call GET "$(ar "$P2")"
check "without a token: 401" 401 "$STATUS"
call GET "$(ar "$P2")" "" "$INSURED_TOKEN"
check "the insured: 403" 403 "$STATUS"
requote "$P2" '{}' "$OPERATIONS_TOKEN"
check "operations: 403" 403 "$STATUS"

echo "== [$(stamp)] R7. One outcome per period"
requote "$P3"
R3="$(field .requote_id)"
NOW="$(NOW_RFC3339)"
BODY7="{\"requote_id\":\"$R3\",\"acceptance\":{\"method\":\"recorded_call\",\"accepted_at\":\"$NOW\",\"reference\":\"GRAB-7\"}}"
printf 'Content-Type: application/json\nAuthorization: Bearer %s\n' "$ADVISOR_TOKEN" >"$WORK/h1"
printf 'Content-Type: application/json\nAuthorization: Bearer %s\n' "$ADVISOR2_TOKEN" >"$WORK/h2"
curl -s --max-time 20 -o "$WORK/a1" -w '%{http_code}' -X POST "$(ar "$P3")/formalization" -H @"$WORK/h1" -d "$BODY7" >"$WORK/c1" &
curl -s --max-time 20 -o "$WORK/a2" -w '%{http_code}' -X POST "$(ar "$P3")/formalization" -H @"$WORK/h2" -d "$BODY7" >"$WORK/c2" &
wait
rm -f "$WORK/h1" "$WORK/h2"
check "two advisors at once: one 201 and one 409" "201 409" "$(printf '%s\n%s\n' "$(cat "$WORK/c1")" "$(cat "$WORK/c2")" | sort | paste -sd' ')"
call POST "$ORIGINATION/v1/policies/$P3/renewal" '{}' "$INSURED_TOKEN"
check "the insured finds the same renewal (200)" 200 "$STATUS"
check "one renewal for the period" 1 "$(psql_orig "SELECT count(*) FROM renewal WHERE policy_id = '$P3'")"
check "one charge for it" 1 "$(psql_orig "SELECT count(*) FROM charge_attempt WHERE renewal_of = '$P3'")"

echo "== [$(stamp)] R9. D-9: the confirmation code of the e-mail"
requote "$P6"
R6="$(field .requote_id)"
call POST "$(ar "$P6")/consent" "{\"requote_id\":\"$R6\"}" "$ADVISOR_TOKEN"
check "the code is sent (200)" 200 "$STATUS"
check "to the masked address" "so***@example.com" "$(field .sent_to)"
last_code() {
  curl -s --max-time 10 "$MAIL/messages?to=sofia.e2e@example.com" | tr -d '\r' |
    jq -r '[.messages[] | select(.subject | contains("renovación"))] | last | .body' | grep -oE '\b[0-9]{6}\b' | head -n 1
}
CODE="$(last_code)"
check_true "the e-mail carries a code of 6 digits" "$([[ "$CODE" =~ ^[0-9]{6}$ ]]; echo $?)"
WRONG="000000"
[[ "$CODE" == "$WRONG" ]] && WRONG="111111"
formalize "$P6" "$R6" confirmation_code "" "$WRONG"
check "a wrong code: 422 invalid_confirmation_code" "422 invalid_confirmation_code" "$STATUS $(field .error)"
call POST "$(ar "$P6")/consent" "{\"requote_id\":\"$R6\"}" "$ADVISOR_TOKEN"
call POST "$(ar "$P6")/consent" "{\"requote_id\":\"$R6\"}" "$ADVISOR_TOKEN"
check "a third code (200)" 200 "$STATUS"
call POST "$(ar "$P6")/consent" "{\"requote_id\":\"$R6\"}" "$ADVISOR_TOKEN"
check "a fourth code: 429 too_many_codes" "429 too_many_codes" "$STATUS $(field .error)"
CODE="$(last_code)"
formalize "$P6" "$R6" confirmation_code "" "$CODE"
check "the right code formalizes (201)" 201 "$STATUS"
check "recorded with the method of the code" confirmation_code "$(field .formalization.acceptance.method)"
CODES_SEEN="$CODE"

echo "== [$(stamp)] R10. D-8: the consent comes first"
# The compliance portal sends an Idempotency-Key (a UUID) with the revocation.
uuid() { printf '%04x%04x-%04x-4%03x-a%03x-%04x%04x%04x' $RANDOM $RANDOM $RANDOM $((RANDOM % 4096)) $((RANDOM % 4096)) $RANDOM $RANDOM $RANDOM; }
printf 'Idempotency-Key: %s\nContent-Type: application/json\nAuthorization: Bearer %s\n' "$(uuid)" "$OPERATIONS_TOKEN" >"$WORK/revoke-header"
REVOKE_STATUS="$(curl -s --max-time 20 -o "$WORK/body" -w '%{http_code}' -X POST "$AUDIT/v1/audit/consents/$CONSENT5/revocations" \
  -H @"$WORK/revoke-header" -d '{"reason":"customer_request","reference":"TCK-E2E-0001","role_confirmed":true}' || true)"
rm -f "$WORK/revoke-header"
check "operations revokes the consent in audit (201)" 201 "$REVOKE_STATUS"
BEFORE="$(source_connections)"
requote "$P5"
check "the requote answers 422 consent_required" "422 consent_required" "$STATUS $(field .error)"
check "no request reached the sources" "$BEFORE" "$(source_connections)"
$COMPOSE stop audit >/dev/null
$COMPOSE restart origination >/dev/null
wait_healthy "$ORIGINATION"
BEFORE="$(source_connections)"
requote "$P4"
check "with audit stopped: 503 consent_unverifiable" "503 consent_unverifiable" "$STATUS $(field .error)"
check "no request reached the sources" "$BEFORE" "$(source_connections)"
$COMPOSE start audit >/dev/null
wait_healthy "$AUDIT"

echo "== [$(stamp)] R11. D-10: a policy without a registered payment method"
P11="pol-r11-$RUN_ID" ACC11="acc-r11-$RUN_ID"
psql_orig "INSERT INTO charge_attempt SELECT (jsonb_populate_record(NULL::charge_attempt, to_jsonb(c) || jsonb_build_object(
  'acceptance_id', '$ACC11', 'idempotency_key', 'cobro-$ACC11', 'policy_id', '$P11', 'activation_event_id', NULL,
  'payment_method_kind', NULL, 'payment_token', NULL, 'payment_brand', NULL, 'payment_last4', NULL, 'payment_bank_code', NULL,
  'payment_payer_type', NULL, 'payment_method_captured_at', NULL))).* FROM charge_attempt c WHERE acceptance_id = '$ACC4'" >/dev/null
psql_orig "INSERT INTO policy SELECT (jsonb_populate_record(NULL::policy, to_jsonb(p) || jsonb_build_object(
  'policy_id', '$P11', 'acceptance_id', '$ACC11'))).* FROM policy p WHERE policy_id = '$P4'" >/dev/null
call GET "$(ar "$P11")" "" "$ADVISOR_TOKEN"
check "the view says payment_method_missing" payment_method_missing "$(field .blocked_reason)"
requote "$P11"
check "it can be requoted (201)" 201 "$STATUS"
formalize "$P11" "$(field .requote_id)" recorded_call GRAB-11
check "formalizing answers 409 payment_method_missing" "409 payment_method_missing" "$STATUS $(field .error)"
check "no renewal was stored" 0 "$(psql_orig "SELECT count(*) FROM renewal WHERE policy_id = '$P11'")"

echo "== [$(stamp)] R13. D-13 (c): a policy born in the assisted channel"
Q13="q-r13-$RUN_ID" P13="pol-r13-$RUN_ID" ACC13="acc-r13-$RUN_ID"
OFF13="off-q-$Q13"
psql_orig "INSERT INTO offer SELECT (jsonb_populate_record(NULL::offer, to_jsonb(o) || jsonb_build_object(
  'offer_id', '$OFF13', 'customer_id', 'cus-$Q13', 'channel', 'assisted', 'quote_id', '$Q13', 'consent_id', 'frm-$Q13',
  'partner_id', 'solventa-advisor', 'application_id', '$Q13', 'supersedes', NULL, 'renews', NULL))).* FROM offer o WHERE offer_id = '$OFFER4'" >/dev/null
psql_orig "INSERT INTO charge_attempt SELECT (jsonb_populate_record(NULL::charge_attempt, to_jsonb(c) || jsonb_build_object(
  'acceptance_id', '$ACC13', 'idempotency_key', 'cobro-$ACC13', 'offer_id', '$OFF13', 'policy_id', '$P13',
  'activation_event_id', NULL))).* FROM charge_attempt c WHERE acceptance_id = '$ACC4'" >/dev/null
psql_orig "INSERT INTO policy SELECT (jsonb_populate_record(NULL::policy, to_jsonb(p) || jsonb_build_object(
  'policy_id', '$P13', 'acceptance_id', '$ACC13'))).* FROM policy p WHERE policy_id = '$P4'" >/dev/null
call GET "$(ar "$P13")" "" "$ADVISOR_TOKEN"
check "the screen shows the checkbox (advisor_affirmation)" advisor_affirmation "$(field .consent_mode)"
BEFORE="$(source_connections)" BEFORE_AUDIT="$(audit_verifications)"
requote "$P13"
check "without the checkbox: 422 validation_failed on consent_affirmed" "422 consent_affirmed" "$STATUS $(field '.fields[0].field')"
check "no request reached the sources" "$BEFORE" "$(source_connections)"
check "audit was not asked" "$BEFORE_AUDIT" "$(audit_verifications)"
requote "$P13" '{"consent_affirmed":true}'
check "with the checkbox: 201 advisor_affirmed" "201 advisor_affirmed" "$STATUS $(field .consent_basis)"
check "the affirmation is recorded with who" asesor "$(psql_orig "SELECT CASE WHEN consent_affirmed_by <> '' THEN 'asesor' END FROM renewal_requote WHERE requote_id = '$(field .requote_id)'")"
formalize "$P13" "$(field .requote_id)" recorded_call GRAB-13
check "formalizing answers 201" 201 "$STATUS"
wait_view "$P13" '.renewal.status == "renewed"' || true
check "the policy is renewed" renewed "$(field .renewal.status)"

echo "== [$(stamp)] R12. 01·D-3 (a+): the payment fails and the insured retries"
$COMPOSE restart payment-gateway >/dev/null
sleep 2
requote "$P4"
R4="$(field .requote_id)"
formalize "$P4" "$R4" recorded_call GRAB-12
check "formalizing answers 201" 201 "$STATUS"
CHARGE4="$(field .renewal.charge_id)"
wait_view "$P4" '.charge.status == "failed"' || true
check "the charge failed: payment_rejected" "failed payment_rejected" "$(field .charge.status) $(field .charge.failure_reason)"
check "with the date of the failure and retry_until" true "$(field '.charge.last_failure_at != null and .renewal.retry_until != null')"
check "the renewal is still accepted" accepted "$(field .renewal.status)"
pay "$CHARGE4" 4111111111111111
check "the insured hands over another card (202)" 202 "$STATUS"
wait_view "$P4" '.renewal.status == "renewed"' || true
check "the policy is renewed" renewed "$(field .renewal.status)"

echo "== [$(stamp)] R8. Minimization"
ANSWERS="$(cat "$WORK/answers")"
check_true "no answer carries a score" "$(! grep -q '"score"' <<<"$ANSWERS"; echo $?)"
check_true "no answer carries the confirmation code" "$(! grep -qF "\"$CODES_SEEN\"" <<<"$(grep -v '"messages"' <<<"$ANSWERS")"; echo $?)"
ASSISTED_ANSWERS="$(grep -F '"consent_mode"' <<<"$ANSWERS" || true)"
check_true "the assisted renewal answers carry no holder e-mail nor token" \
  "$(! grep -qE 'e2e@example.com|tok_' <<<"$ASSISTED_ANSWERS"; echo $?)"
LOGS="$($COMPOSE logs --no-log-prefix origination 2>/dev/null | grep -F 'assisted renewal' || true)"
check_true "the assisted renewal logs exist" "$([[ -n "$LOGS" ]]; echo $?)"
check_true "they carry no document, e-mail, reference, code nor token" \
  "$(! grep -qE '1020345678|1070890123|e2e@example.com|GRAB-|tok_|'"$CODES_SEEN" <<<"$LOGS"; echo $?)"

echo
if [[ "$FAILS" -gt 0 ]]; then
  echo "== $FAILS check(s) failed" >&2
  exit 1
fi
echo "== TODO OK: the assisted renewal (HU-W1.5-03) passed end to end."
