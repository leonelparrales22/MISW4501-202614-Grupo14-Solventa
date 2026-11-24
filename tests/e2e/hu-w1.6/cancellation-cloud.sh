#!/usr/bin/env bash
# API checks of the cancellation of a policy at the request of the insured,
# with its prorated refund (HU-W1.6-01), for the cloud window (staging),
# through the portal of CloudFront, as the browser calls it, and only with the
# permanent users of bootstrap/identity. It never creates users and never
# prints a password or a token. It buys its own policies through the real
# journey (consent through the partner route, POST /v1/offers, the insured
# opens and accepts the offer, the card 4111 tokenised at /pasarela/tokens),
# so the seeded C1..C3 of tests/e2e/hu-w1.5/assisted-renewal-cloud.sh --seed
# stay for the demonstration; everything lives in the ephemeral platform and
# goes away with down.sh.
#
# Usage:
#   cancellation-cloud.sh verify
#
# Blocks: CA-1 (the quote of a policy bought today: the month paid by the
# issuance, one day used, the refund truncated to the peso, the card and the
# end of the day; quoting changes nothing); CA-2 (another amount answers 409
# refund_changed with the quote; the amount seen answers 201 with the policy
# cancelled and the refund registered with REF-<year>-<number>, amount, date
# and method; the detail and the wallet say Cancelada; the refund ends
# completed when the gateway is up; it cannot be cancelled twice); the rules
# (an invalid reason 422; the cancelled policy cannot change; the advisor does
# not count it as non-payment); roles (401 without a token, 403 for the advisor
# and the operations analyst); D-2 (a policy of the policy core, from the
# wallet, is not found).
#
# Inputs (environment; the password and the tokens are never printed):
#   PORTAL_URL           base URL of the portal (CloudFront)
#   SEED_USER_PASSWORD   password of the permanent users (or SOLVENTA_IDENTITY_SEED_PASSWORD)
#   INSURED_USER         default asegurado@example.com
#   ADVISOR_USER         default asesor@example.com
#   OPERATIONS_USER      default operaciones@example.com
#   OUTPUT_FILE          optional: a copy of the output (for the evidence)
#   RUN_ID               suffix of the fictitious customers (default v<epoch seconds>)
#   REFUND_WAIT_SECONDS  how long to wait for the refund to complete (default 120)
#   COGNITO_ENDPOINT     default https://cognito-idp.<region of config.json>.amazonaws.com/
#                        (a local endpoint only to rehearse the script)
#
# It only calls InitiateAuth of the user pool (the public sign-in, with the
# client and the region of the config.json of the portal) and the portal. It
# needs curl and jq, and exits with a non-zero code when any check fails.
set -euo pipefail
set +x

usage() {
  awk 'NR > 1 && /^set -euo pipefail/ { exit } NR > 1 { print }' "$0"
  exit "${1:-0}"
}
case "${1:-}" in
  verify) ;;
  -h | --help) usage 0 ;;
  *) usage 2 ;;
esac

for tool in curl jq; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done
PASSWORD="${SEED_USER_PASSWORD:-${SOLVENTA_IDENTITY_SEED_PASSWORD:-}}"
[[ -n "${PORTAL_URL:-}" ]] || { echo "PORTAL_URL is required (the CloudFront URL of the portal)" >&2; exit 2; }
[[ -n "$PASSWORD" ]] || { echo "SEED_USER_PASSWORD (or SOLVENTA_IDENTITY_SEED_PASSWORD) is required" >&2; exit 2; }
PORTAL_URL="${PORTAL_URL%/}"
INSURED_USER="${INSURED_USER:-asegurado@example.com}"
ADVISOR_USER="${ADVISOR_USER:-asesor@example.com}"
OPERATIONS_USER="${OPERATIONS_USER:-operaciones@example.com}"
RUN_ID="${RUN_ID:-v$(date +%s)}"
REFUND_WAIT_SECONDS="${REFUND_WAIT_SECONDS:-120}"
if [[ -n "${OUTPUT_FILE:-}" ]]; then
  mkdir -p "$(dirname "$OUTPUT_FILE")"
  exec > >(tee "$OUTPUT_FILE") 2>&1
fi

umask 077
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
FAILS=0
SKIPS=0
check() { # <description> <expected> <actual>
  if [[ "$2" == "$3" ]]; then
    echo "  ok    $1"
  else
    echo "  FAIL  $1 (expected '$2', got '$3')"
    FAILS=$((FAILS + 1))
  fi
}
skip() { echo "  SKIP  $1"; SKIPS=$((SKIPS + 1)); }
json_escape() { local s=${1//\\/\\\\}; printf '%s' "${s//\"/\\\"}"; }

# call <method> <path> [token] [json body]: sets STATUS and BODY. The token goes
# through a private header file, never through the command line. A GET never
# carries the body: CloudFront answers its own 403 to a GET with a body before
# it reaches the gateway.
call() {
  : >"$WORK/header"
  [[ -n "${3:-}" ]] && printf 'Authorization: Bearer %s\n' "$3" >>"$WORK/header"
  printf 'User-Agent: hu-w1.6-01 cancellation-cloud.sh\n' >>"$WORK/header"
  : >"$WORK/body"
  if [[ -n "${4:-}" && "$1" != GET ]]; then
    printf 'Content-Type: application/json\n' >>"$WORK/header"
    STATUS="$(printf '%s' "$4" | curl -s --max-time 30 -o "$WORK/body" -w '%{http_code}' -X "$1" -H @"$WORK/header" \
      --data-binary @- "$PORTAL_URL$2" || true)"
  else
    STATUS="$(curl -s --max-time 30 -o "$WORK/body" -w '%{http_code}' -X "$1" -H @"$WORK/header" "$PORTAL_URL$2" || true)"
  fi
  BODY="$(tr -d '\r' <"$WORK/body")"
}
field() { jq -r "$1" <<<"$BODY" 2>/dev/null || echo "<not json>"; }

# The client of the user pool, from the config.json of the portal.
call GET /config.json
check "the config.json of the portal (200)" 200 "$STATUS"
CLIENT_ID="$(field '.cognito.clienteId // empty')"
REGION="$(field '.cognito.region // "us-east-2"')"
[[ -n "$CLIENT_ID" ]] || { echo "The config.json of the portal has no Cognito client" >&2; exit 1; }
COGNITO_ENDPOINT="${COGNITO_ENDPOINT:-https://cognito-idp.$REGION.amazonaws.com/}"
if [[ ! "$COGNITO_ENDPOINT" =~ ^https://cognito-idp\.[a-z0-9-]+\.amazonaws\.com/?$ && ! "$COGNITO_ENDPOINT" =~ ^http://(localhost|127\.0\.0\.1)(:[0-9]+)?/?$ ]]; then
  echo "COGNITO_ENDPOINT must be the Cognito endpoint of the region or a local endpoint" >&2
  exit 2
fi
token() { # <user>: the ID token of the user (never printed)
  printf '{"AuthFlow":"USER_PASSWORD_AUTH","ClientId":"%s","AuthParameters":{"USERNAME":"%s","PASSWORD":"%s"}}' \
    "$CLIENT_ID" "$1" "$(json_escape "$PASSWORD")" |
    curl -s --max-time 30 -X POST "$COGNITO_ENDPOINT" -H 'Content-Type: application/x-amz-json-1.1' \
      -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' --data-binary @- |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken // empty'
}
INSURED_TOKEN="$(token "$INSURED_USER" || true)"
ADVISOR_TOKEN="$(token "$ADVISOR_USER" || true)"
OPERATIONS_TOKEN="$(token "$OPERATIONS_USER" || true)"
if [[ -z "$INSURED_TOKEN" || -z "$ADVISOR_TOKEN" || -z "$OPERATIONS_TOKEN" ]]; then
  echo "Could not sign in $INSURED_USER, $ADVISOR_USER or $OPERATIONS_USER" >&2
  exit 1
fi

call GET /v1/audit/consent-terms/current
[[ "$STATUS" == 200 ]] || { echo "The consent terms answered $STATUS" >&2; exit 1; }
TERMS_VERSION="$(field .version)"
TERMS_SOURCES="$(field '[.sources[].code] | map("\"" + . + "\"") | join(",")')"

# buy <tag> <holder name> <document>: buys a policy of 12 months for the
# insured through the real journey; sets POLICY.
buy() {
  local customer="cliente-$1-$RUN_ID" app="app-$1-$RUN_ID" holder="titular.$1.$RUN_ID@example.com" consent offer acceptance tok brand last4 i
  call POST /v1/audit/consents "" "{\"customer_id\":\"$customer\",\"document_id\":\"$3\",\"full_name\":\"$2\",
    \"terms_version\":\"$TERMS_VERSION\",\"accepted_sources\":[$TERMS_SOURCES],\"accepted_data_policy\":true,
    \"channel\":\"embedded\",\"partner_id\":\"portal-embebido\",\"application_id\":\"$app\"}"
  [[ "$STATUS" == 201 || "$STATUS" == 200 ]] || { echo "the consent of $1 answered $STATUS: $(field .error)" >&2; return 1; }
  consent="$(field .consent_id)"
  call POST /v1/offers "" "{\"customer_id\":\"$customer\",\"consent_id\":\"$consent\",\"partner_id\":\"portal-embebido\",
    \"application_id\":\"$app\",\"document_id\":\"$3\",\"full_name\":\"$2\",\"email\":\"$holder\",\"birth_date\":\"1985-03-14\",
    \"insured_amount\":68000000,\"term_months\":12}"
  [[ "$STATUS" == 201 ]] || { echo "the offer of $1 answered $STATUS: $(field .error)" >&2; return 1; }
  offer="$(field .offer_id)"
  call GET "/v1/offers/$offer" "$INSURED_TOKEN"
  call POST "/v1/offers/$offer/acceptance" "$INSURED_TOKEN" '{"channel":"web"}'
  [[ "$STATUS" == 202 ]] || { echo "the acceptance of $1 answered $STATUS: $(field .error)" >&2; return 1; }
  acceptance="$(field .acceptance_id)"
  for i in $(seq 1 60); do
    call GET "/v1/acceptances/$acceptance/charge" "$INSURED_TOKEN"
    [[ "$STATUS" == 200 ]] && break
    sleep 2
  done
  [[ "$STATUS" == 200 ]] || { echo "the charge of $1 was not created ($STATUS)" >&2; return 1; }
  call POST /pasarela/tokens "" '{"number":"4111111111111111","holder":"Prueba Cancelacion"}'
  tok="$(field .token)" brand="$(field .brand)" last4="$(field .last4)"
  call POST "/v1/acceptances/$acceptance/charge/payment-method" "$INSURED_TOKEN" \
    "{\"method\":\"card\",\"payment_token\":\"$tok\",\"brand\":\"$brand\",\"last4\":\"$last4\",\"installments\":1}"
  [[ "$STATUS" == 202 ]] || { echo "the card of $1 answered $STATUS: $(field .error)" >&2; return 1; }
  POLICY=""
  for i in $(seq 1 60); do
    call GET "/v1/acceptances/$acceptance/charge" "$INSURED_TOKEN"
    if [[ "$(field .status)" == confirmed && "$(field '.policy_id // empty')" != "" ]]; then
      POLICY="$(field .policy_id)"
      break
    fi
    sleep 2
  done
  [[ -n "$POLICY" ]] || { echo "the policy of $1 was not issued: $(field .status)" >&2; return 1; }
  for i in $(seq 1 30); do
    call GET "/v1/policies/$POLICY" "$INSURED_TOKEN"
    [[ "$STATUS" == 200 ]] && break
    sleep 2
  done
  [[ "$STATUS" == 200 ]] || { echo "the policy $POLICY of $1 is not in the wallet ($STATUS)" >&2; return 1; }
}

QUOTE() { call GET "/v1/policies/$1/cancellation/quote" "$INSURED_TOKEN"; }
CONFIRM() { # <policy> <refund amount> [reason]
  call POST "/v1/policies/$1/cancellation" "$INSURED_TOKEN" \
    "{\"reason\":\"${3:-property_sold}\",\"comments\":\"Verificacion por API\",\"refund_amount\":$2,\"accept_terms\":true}"
}

echo "== Buying the policies of the verification (run $RUN_ID), owned by $INSURED_USER"
buy v1 "Ines Barrera Cuesta" 1084012345
V1="$POLICY"
echo "  V1  $V1"

call GET "/v1/policies/expiring?status=cancelled_non_payment" "$ADVISOR_TOKEN"
NON_PAYMENT_BEFORE="$(field .cancelled_non_payment_last_30_days)"

echo "== CA-1: the refund is shown before confirming"
QUOTE "$V1"
check "the quote answers 200" 200 "$STATUS"
PAID="$(field .paid_period.paid_amount)" D="$(field .period_days)" N="$(field .unused_days)" AMOUNT="$(field .refund_amount)"
check "month 1, paid by the issuance" "1 issuance" "$(field '"\(.paid_period.month) \(.paid_period.kind)"')"
check "bought today: one day used" "$((D - 1))" "$N"
check "the refund is paid x N / D truncated to the peso" "$(jq -n "($PAID * 100 * $N / $D | floor) / 100 | floor")" "$AMOUNT"
check "the percentage, with one decimal" "$(jq -n "($N * 1000 / $D | round) / 10")" "$(field .prorate_percent)"
check "back to the card of the issuance" "card 1111" "$(field '"\(.refund_method.method) \(.refund_method.last4)"')"
check "effective at the end of the day in Colombia" "04:59:59Z" "$(field '.effective_at[11:]')"
echo "  ·     $D days, $N not used, $(field .prorate_percent) %, refund $AMOUNT $(field .currency)"
call GET "/v1/policies/$V1" "$INSURED_TOKEN"
check "quoting changes nothing" "active null" "$(field '"\(.status) \(.cancellation)"')"

echo "== Rules of the confirmation"
call POST "/v1/policies/$V1/cancellation" "$INSURED_TOKEN" '{"reason":"moved_abroad","refund_amount":1,"accept_terms":true}'
check "a reason out of the list: 422 reason=invalid" "422 invalid_request reason=invalid" \
  "$STATUS $(field .error) $(field '[.fields[]? | "\(.field)=\(.code)"] | join(" ")')"
CONFIRM "$V1" 1
check "another amount: 409 refund_changed" "409 refund_changed" "$STATUS $(field .error)"
check "with the quote to confirm" "$AMOUNT" "$(field .quote.refund_amount)"

echo "== CA-2: the policy is cancelled and the refund registered"
CONFIRM "$V1" "$AMOUNT"
check "the amount seen: 201 cancelled" "201 cancelled" "$STATUS $(field .status)"
REF="$(field .cancellation.refund_detail.reference)"
check "with its reference REF-<year>-<number>" true "$([[ "$REF" =~ ^REF-[0-9]{4}-[0-9]{5,}$ ]] && echo true || echo false)"
check "amount, method and date (CA-2)" "$AMOUNT card true" \
  "$(field '"\(.cancellation.refund_detail.amount) \(.cancellation.refund_detail.method.method) \(.cancellation.refund_detail.registered_at != null)"')"
echo "  ·     $REF"
REFUND=""
for _ in $(seq 1 "$((REFUND_WAIT_SECONDS / 5))"); do
  call GET "/v1/policies/$V1" "$INSURED_TOKEN"
  REFUND="$(field .cancellation.refund)"
  [[ "$REFUND" == completed ]] && break
  sleep 5
done
check "the detail says Cancelada, and the refund is completed by the gateway" "cancelled completed" "$(field .status) $REFUND"
call GET /v1/policies "$INSURED_TOKEN"
check "the wallet says Cancelada, without renewal" "cancelled false insured_request" \
  "$(field ".items[] | select(.policy_id == \"$V1\") | \"\(.status) \(.can_renew) \(.cancellation.reason)\"")"
QUOTE "$V1"
check "it cannot be cancelled twice" "409 policy_already_cancelled" "$STATUS $(field .error)"
call POST "/v1/policies/$V1/change-requests" "$INSURED_TOKEN" \
  '{"change_type":"insured_amount","channel":"web","insured_amount":120000000}'
check "the cancelled policy cannot change (HU-W1.4-01)" "409 policy_not_in_force" "$STATUS $(field .error)"
call GET "/v1/policies/expiring?status=cancelled_non_payment" "$ADVISOR_TOKEN"
check "the advisor does not count it as non-payment" "$NON_PAYMENT_BEFORE" "$(field .cancelled_non_payment_last_30_days)"

echo "== Roles"
call GET "/v1/policies/$V1/cancellation/quote"
check "without a token: 401" 401 "$STATUS"
call GET "/v1/policies/$V1/cancellation/quote" "$ADVISOR_TOKEN"
check "the advisor: 403" 403 "$STATUS"
call POST "/v1/policies/$V1/cancellation" "$OPERATIONS_TOKEN" '{"reason":"other","refund_amount":0,"accept_terms":true}'
check "the operations analyst: 403" 403 "$STATUS"

echo "== D-2: only the policies origination issued"
call GET /v1/policies "$INSURED_TOKEN"
CORE="$(field '[.items[] | select(.source == "policy_core") | .policy_id] | first // empty')"
if [[ -n "$CORE" ]]; then
  QUOTE "$CORE"
  check "a policy of the policy core: 404" "404 policy_not_found" "$STATUS $(field .error)"
else
  skip "the wallet has no policy of the policy core"
fi

echo
echo "== $FAILS failed, $SKIPS skipped"
[[ "$FAILS" == 0 ]] || exit 1
echo "== The cancellation with a prorated refund (HU-W1.6-01) passed by API in the cloud."
