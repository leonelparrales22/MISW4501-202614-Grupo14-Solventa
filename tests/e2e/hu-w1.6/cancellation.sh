#!/usr/bin/env bash
# Runs the end-to-end scenarios of the cancellation of a policy at the request
# of the insured, with its prorated refund (HU-W1.6-01), with Docker, over the
# environment of tests/e2e/hu-w1.4 (the real audit, origination and claims
# services with their PostgreSQL, Redis, the stubs, the payment gateway and the
# e-mail stubs, Moto as Cognito). The policies are bought through the real
# journey (consent in audit, POST /v1/offers, acceptance and the card 4111), so
# the first month is paid by the charge of the issuance.
#
#   C1   CA-1        the quote of a policy bought today: the month paid, 1 day used of the period, the refund
#                    (paid x N / D truncated to the peso), the card 4111 and the end of the day; nothing changes
#   C2   CA-2        the confirmation with another amount: 409 refund_changed with the quote, nothing changes;
#                    with the amount seen: 201, the policy cancelled with REF-<year>-<number>, the refund sent to
#                    the gateway once (completed), the wallet and the detail say so; it cannot be cancelled twice
#   C3   rules       the cancelled policy cannot change (HU-W1.4-01: 409 policy_not_in_force) and the advisor
#                    does not count it as cancelled for non-payment
#   C4   D-5 b       the gateway stopped: the cancellation does not wait for it and the refund stays in_process;
#                    started again (with its memory empty): completed, with one real refund
#   C5   roles       without a token 401; the advisor and the operations analyst 403; another policy id 404
#
# The script exits with a non-zero code when any check fails. The environment
# is always removed at the end (down -v). Requirements: Docker (compose v2),
# curl, jq and the AWS CLI (only against the local Moto). The secrets
# (PostgreSQL, Moto users, the document link) are random, generated here and
# never printed.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl jq aws; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done

# The environment of HU-W1.4, with a project and ports of its own.
COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -p hu-w1-6-e2e -f ../hu-w1.4/docker-compose.yml"
export AUDIT_PORT="${AUDIT_PORT:-8790}" ORIGINATION_PORT="${ORIGINATION_PORT:-8791}" CLAIMS_PORT="${CLAIMS_PORT:-8792}"
export TOXIPROXY_PORT="${TOXIPROXY_PORT:-8794}" MOTO_PORT="${MOTO_PORT:-5390}"
export EMAIL_PORT="${EMAIL_PORT:-9391}" GATEWAY_PORT="${GATEWAY_PORT:-9394}"
AUDIT="http://localhost:$AUDIT_PORT"
ORIGINATION="http://localhost:$ORIGINATION_PORT"
GATEWAY="http://localhost:$GATEWAY_PORT"
RUN_ID="c$(date +%s)"
INSURED_EMAIL="asegurado.local@example.com"

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

# call <method> <url> [json body] [bearer token]: sets STATUS and BODY. The
# token goes through a private header file, never through the command line.
call() {
  : >"$WORK/header"
  printf 'User-Agent: hu-w1.6-e2e\n' >>"$WORK/header"
  [[ -n "${4:-}" ]] && printf 'Authorization: Bearer %s\n' "$4" >>"$WORK/header"
  if [[ -n "${3:-}" ]]; then
    printf 'Content-Type: application/json\n' >>"$WORK/header"
    STATUS="$(printf '%s' "$3" | curl -s --max-time 20 -o "$WORK/body" -w '%{http_code}' -X "$1" -H @"$WORK/header" --data-binary @- "$2" || true)"
  else
    STATUS="$(curl -s --max-time 20 -o "$WORK/body" -w '%{http_code}' -X "$1" -H @"$WORK/header" "$2" || true)"
  fi
  BODY="$(tr -d '\r' <"$WORK/body")"
}
field() { jq -r "$1" <<<"$BODY" 2>/dev/null || echo "<not json>"; }

wait_healthy() { # <url> <service>
  local i
  for i in $(seq 1 120); do
    curl -sf --max-time 2 "$1/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "$1 did not become healthy" >&2
  $COMPOSE logs "$2" | tail -40 >&2
  return 1
}

echo "== [$(stamp)] Starting PostgreSQL, Redis, the stubs, the gateway, the e-mail and Moto"
$COMPOSE up -d --build --wait postgres-audit postgres-origination postgres-claims email payment-gateway redis openfinance opendata kyc \
  toxiproxy moto

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

token() { # <username>
  curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
    -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$WEB_CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"$1\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken'
}
INSURED_TOKEN="$(token "$INSURED_EMAIL")"
ADVISOR_TOKEN="$(token asesor.local@example.com)"
OPERATIONS_TOKEN="$(token operaciones.local@example.com)"

echo "== [$(stamp)] Starting audit, claims and origination"
$COMPOSE up -d --build audit claims origination
wait_healthy "$AUDIT" audit
wait_healthy "$ORIGINATION" origination

call GET "$AUDIT/v1/audit/consent-terms/current"
TERMS_VERSION="$(field .version)"
TERMS_SOURCES="$(field '[.sources[].code] | map("\"" + . + "\"") | join(",")')"

# seed <tag> <holder name> <document>: buys a policy of 12 months of the
# insured through the real journey, paid with the card 4111; sets POLICY.
seed() {
  local customer="cust-$1-$RUN_ID" app="app-$1-$RUN_ID" consent offer acceptance tok brand last4 i
  call POST "$AUDIT/v1/audit/consents" "{\"customer_id\":\"$customer\",\"document_id\":\"$3\",\"full_name\":\"$2\",
    \"terms_version\":\"$TERMS_VERSION\",\"accepted_sources\":[$TERMS_SOURCES],\"accepted_data_policy\":true,
    \"channel\":\"embedded\",\"partner_id\":\"partner-e2e\",\"application_id\":\"$app\"}"
  [[ "$STATUS" == 201 ]] || { echo "consent of $1: HTTP $STATUS $BODY" >&2; exit 1; }
  consent="$(field .consent_id)"
  call POST "$ORIGINATION/v1/offers" "{\"customer_id\":\"$customer\",\"consent_id\":\"$consent\",\"partner_id\":\"partner-e2e\",
    \"application_id\":\"$app\",\"document_id\":\"$3\",\"full_name\":\"$2\",\"email\":\"$INSURED_EMAIL\",\"birth_date\":\"1985-03-14\",
    \"insured_amount\":68000000,\"term_months\":12}"
  [[ "$STATUS" == 201 ]] || { echo "offer of $1: HTTP $STATUS $BODY" >&2; exit 1; }
  offer="$(field .offer_id)"
  call GET "$ORIGINATION/v1/offers/$offer" "" "$INSURED_TOKEN"
  call POST "$ORIGINATION/v1/offers/$offer/acceptance" '{"channel":"web"}' "$INSURED_TOKEN"
  [[ "$STATUS" == 202 ]] || { echo "acceptance of $1: HTTP $STATUS $BODY" >&2; exit 1; }
  acceptance="$(field .acceptance_id)"
  for i in $(seq 1 60); do
    call GET "$ORIGINATION/v1/acceptances/$acceptance/charge" "" "$INSURED_TOKEN"
    [[ "$STATUS" == 200 ]] && break
    sleep 1
  done
  [[ "$STATUS" == 200 ]] || { echo "charge of $1 not created: HTTP $STATUS $BODY" >&2; exit 1; }
  call POST "$GATEWAY/tokens" '{"number":"4111111111111111","holder":"Prueba E2E"}'
  tok="$(field .token)" brand="$(field .brand)" last4="$(field .last4)"
  call POST "$ORIGINATION/v1/acceptances/$acceptance/charge/payment-method" \
    "{\"method\":\"card\",\"payment_token\":\"$tok\",\"brand\":\"$brand\",\"last4\":\"$last4\",\"installments\":1}" "$INSURED_TOKEN"
  for i in $(seq 1 60); do
    call GET "$ORIGINATION/v1/acceptances/$acceptance/charge" "" "$INSURED_TOKEN"
    [[ "$(field .status)" == confirmed && "$(field .policy_id)" != null ]] && break
    sleep 1
  done
  POLICY="$(field .policy_id)"
  [[ "$POLICY" != null && -n "$POLICY" ]] || { echo "policy of $1 not issued: $BODY" >&2; exit 1; }
  for i in $(seq 1 30); do
    call GET "$ORIGINATION/v1/policies/$POLICY" "" "$INSURED_TOKEN"
    [[ "$STATUS" == 200 ]] && break
    sleep 1
  done
  echo "  seeded $1: $POLICY ($2)"
}

quote() { call GET "$ORIGINATION/v1/policies/$1/cancellation/quote" "" "$INSURED_TOKEN"; }
confirm() { # <policy> <refund amount>
  call POST "$ORIGINATION/v1/policies/$1/cancellation" \
    "{\"reason\":\"property_sold\",\"comments\":\"Vendi el apartamento\",\"refund_amount\":$2,\"accept_terms\":true}" "$INSURED_TOKEN"
}
refund_status() { # <policy>: the refund of the cancellation, as the detail says it
  call GET "$ORIGINATION/v1/policies/$1" "" "$INSURED_TOKEN"
  field '.cancellation.refund'
}
real_refunds() { # <policy>: the real refunds the gateway made with its key
  curl -s --max-time 10 "$GATEWAY/debug/charges" | tr -d '\r' | jq -r ".real_refunds[\"reembolso-$1\"] // 0"
}
wait_refund() { # <policy> <status> <seconds>
  local i
  for i in $(seq 1 "$3"); do
    [[ "$(refund_status "$1")" == "$2" ]] && return 0
    sleep 1
  done
  return 0
}

echo "== [$(stamp)] Buying the policies through the real journey"
seed c1 "Diana Correa Lozano" 1083012345
C1="$POLICY"
seed c2 "Hernan Vidal Rojas" 1083123456
C2="$POLICY"

echo "== [$(stamp)] C1. CA-1: the refund is shown before confirming"
quote "$C1"
check "the quote answers 200" 200 "$STATUS"
PAID="$(field .paid_period.paid_amount)" D="$(field .period_days)" N="$(field .unused_days)" AMOUNT="$(field .refund_amount)"
check "month 1, paid by the issuance" "1 issuance" "$(field '"\(.paid_period.month) \(.paid_period.kind)"')"
check "bought today: one day used" "$((D - 1))" "$N"
EXPECTED="$(jq -n "($PAID * 100 * $N / $D | floor) / 100 | floor")"
check "the refund is paid x N / D truncated to the peso" "$EXPECTED" "$AMOUNT"
check "the percentage, with one decimal" "$(jq -n "($N * 1000 / $D | round) / 10")" "$(field .prorate_percent)"
check "back to the card of the issuance" "card visa 1111" "$(field '"\(.refund_method.method) \(.refund_method.brand) \(.refund_method.last4)"')"
check "effective at the end of the day in Colombia" "04:59:59Z" "$(field '.effective_at[11:]')"
check "up to date" up_to_date "$(field .payment_status)"
call GET "$ORIGINATION/v1/policies/$C1" "" "$INSURED_TOKEN"
check "quoting changes nothing" "active null" "$(field '"\(.status) \(.cancellation)"')"

echo "== [$(stamp)] C2. CA-2: the policy is cancelled and the refund registered"
confirm "$C1" 1
check "another amount: 409 refund_changed" "409 refund_changed" "$STATUS $(field .error)"
check "with the quote to confirm" "$AMOUNT" "$(field .quote.refund_amount)"
confirm "$C1" "$AMOUNT"
check "the amount seen: 201 cancelled" "201 cancelled" "$STATUS $(field .status)"
check "a cancellation at the request of the insured" insured_request "$(field .cancellation.reason)"
REF="$(field .cancellation.refund_detail.reference)"
check "with its reference" true "$([[ "$REF" =~ ^REF-[0-9]{4}-[0-9]{5,}$ ]] && echo true || echo false)"
check "amount, method and date (CA-2)" "$AMOUNT card true" \
  "$(field '"\(.cancellation.refund_detail.amount) \(.cancellation.refund_detail.method.method) \(.cancellation.refund_detail.registered_at != null)"')"
check "the reason" "property_sold Vendi el apartamento" "$(field '"\(.cancellation.insured_reason.code) \(.cancellation.insured_reason.comments)"')"
wait_refund "$C1" completed 60
check "the refund is sent to the gateway in the background" completed "$(refund_status "$C1")"
check "with one real refund at the gateway" 1 "$(real_refunds "$C1")"
call GET "$ORIGINATION/v1/policies" "" "$INSURED_TOKEN"
check "the wallet says Cancelada, without renewal" "cancelled false" \
  "$(field ".items[] | select(.policy_id == \"$C1\") | \"\(.status) \(.can_renew)\"")"
quote "$C1"
check "it cannot be cancelled twice" "409 policy_already_cancelled" "$STATUS $(field .error)"

echo "== [$(stamp)] C3. What a cancellation stops"
call POST "$ORIGINATION/v1/policies/$C1/change-requests" \
  '{"change_type":"insured_amount","channel":"web","insured_amount":120000000}' "$INSURED_TOKEN"
check "the cancelled policy cannot change (HU-W1.4-01)" "409 policy_not_in_force" "$STATUS $(field .error)"
call GET "$ORIGINATION/v1/policies/expiring?status=cancelled_non_payment" "" "$ADVISOR_TOKEN"
check "the advisor does not count it as non-payment" "200 0" "$STATUS $(field .cancelled_non_payment_last_30_days)"

echo "== [$(stamp)] C4. D-5 b: the refund waits for the gateway"
$COMPOSE stop payment-gateway >/dev/null 2>&1
quote "$C2"
AMOUNT2="$(field .refund_amount)"
confirm "$C2" "$AMOUNT2"
check "the cancellation does not wait for the gateway" "201 in_process" "$STATUS $(field .cancellation.refund)"
sleep 5
check "the refund stays En proceso while the gateway is down" in_process "$(refund_status "$C2")"
$COMPOSE start payment-gateway >/dev/null 2>&1
wait_refund "$C2" completed 120
check "back up: Reembolsado" completed "$(refund_status "$C2")"
check "with one real refund (the key, never random)" 1 "$(real_refunds "$C2")"

echo "== [$(stamp)] C5. Roles"
call GET "$ORIGINATION/v1/policies/$C2/cancellation/quote"
check "without a token: 401" 401 "$STATUS"
call GET "$ORIGINATION/v1/policies/$C2/cancellation/quote" "" "$ADVISOR_TOKEN"
check "the advisor: 403" 403 "$STATUS"
call POST "$ORIGINATION/v1/policies/$C2/cancellation" '{"reason":"other","refund_amount":0,"accept_terms":true}' "$OPERATIONS_TOKEN"
check "the operations analyst: 403" 403 "$STATUS"
call GET "$ORIGINATION/v1/policies/VH-2026-999999/cancellation/quote" "" "$INSURED_TOKEN"
check "a policy that is not hers: 404" "404 policy_not_found" "$STATUS $(field .error)"

echo
if [[ "$FAILS" -gt 0 ]]; then
  echo "== $FAILS check(s) failed" >&2
  exit 1
fi
echo "== TODO OK: the cancellation with a prorated refund (HU-W1.6-01) passed end to end."
