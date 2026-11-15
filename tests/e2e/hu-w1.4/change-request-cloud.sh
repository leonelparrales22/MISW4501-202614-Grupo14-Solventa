#!/usr/bin/env bash
# API checks of the change request of the insured (HU-W1.4-01) for the cloud
# window (staging), through the portal of CloudFront, as the browser calls it,
# and only with the permanent users of bootstrap/identity. It seeds nothing:
# the policies come from the single seed of the window
# (tests/e2e/hu-w1.5/assisted-renewal-cloud.sh --seed), which buys P8 (the
# demonstration), P9 (these checks) and, when asegurado2@example.com exists,
# P10 (owned by the second insured) through the real journey.
#
# Usage:
#   change-request-cloud.sh verify
#
# Blocks: the data of the seed; CA-1 (the current terms of P9); CA-3 (invalid
# or incomplete requests answer 422 field by field and nothing is filed); CA-1
# and CA-2 (a request of each type is filed with its SOL-MOD id, it is read
# back and listed, and a second one of the same type answers 409 with the open
# one); CA-4 (401 without a token, 403 for the advisor and the operations
# analyst and, with P10, 403 for the policy of another insured whose list never
# shows the requests of the first one); D-2 (a policy of the policy core is not
# found). The requests filed on P9 stay in the ephemeral database of staging
# and go away with down.sh.
#
# Inputs (environment; the password and the tokens are never printed):
#   PORTAL_URL           base URL of the portal (CloudFront)
#   SEED_USER_PASSWORD   password of the permanent users (or SOLVENTA_IDENTITY_SEED_PASSWORD)
#   SEED_FILE            the file written by assisted-renewal-cloud.sh --seed (P9 and P10)
#   INSURED_USER         default asegurado@example.com
#   INSURED2_USER        default asegurado2@example.com (owner of P10)
#   ADVISOR_USER         default asesor@example.com
#   OPERATIONS_USER      default operaciones@example.com
#   OUTPUT_FILE          optional: a copy of the output (for the evidence)
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
[[ -n "${SEED_FILE:-}" && -f "${SEED_FILE:-}" ]] || {
  echo "SEED_FILE is required: the file of tests/e2e/hu-w1.5/assisted-renewal-cloud.sh --seed (P9, P10)" >&2
  exit 2
}
PORTAL_URL="${PORTAL_URL%/}"
INSURED_USER="${INSURED_USER:-asegurado@example.com}"
INSURED2_USER="${INSURED2_USER:-asegurado2@example.com}"
ADVISOR_USER="${ADVISOR_USER:-asesor@example.com}"
OPERATIONS_USER="${OPERATIONS_USER:-operaciones@example.com}"
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
# through a private header file, never through the command line.
call() {
  : >"$WORK/header"
  [[ -n "${3:-}" ]] && printf 'Authorization: Bearer %s\n' "$3" >>"$WORK/header"
  printf 'User-Agent: hu-w1.4-01 change-request-cloud.sh\n' >>"$WORK/header"
  : >"$WORK/body"
  if [[ -n "${4:-}" ]]; then
    printf 'Content-Type: application/json\n' >>"$WORK/header"
    STATUS="$(printf '%s' "$4" | curl -s --max-time 30 -o "$WORK/body" -w '%{http_code}' -X "$1" -H @"$WORK/header" \
      --data-binary @- "$PORTAL_URL$2" || true)"
  else
    STATUS="$(curl -s --max-time 30 -o "$WORK/body" -w '%{http_code}' -X "$1" -H @"$WORK/header" "$PORTAL_URL$2" || true)"
  fi
  BODY="$(tr -d '\r' <"$WORK/body")"
}
field() { jq -r "$1" <<<"$BODY" 2>/dev/null || echo "<not json>"; }
# codes: the "field=code" pairs of a 422, sorted and joined by spaces.
codes() { field '[.fields[]? | "\(.field)=\(.code)"] | sort | join(" ")'; }

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
INSURED2_TOKEN="$(token "$INSURED2_USER" || true)"

P9="$(sed -n 's/^P9=//p' "$SEED_FILE" | tail -n 1)"
P10="$(sed -n 's/^P10=//p' "$SEED_FILE" | tail -n 1)"
TERMS="/v1/policies/$P9/current-terms"
FILE="/v1/policies/$P9/change-requests"
LIST=/v1/policies/change-requests

echo "== Data of the seed"
if [[ -z "$P9" ]]; then
  echo "== $SEED_FILE has no P9: run tests/e2e/hu-w1.5/assisted-renewal-cloud.sh --seed first; nothing was checked" >&2
  exit 2
fi
call GET "$TERMS" "$INSURED_TOKEN"
if [[ "$STATUS" != 200 || "$(field .policy.in_force)" != true ]]; then
  echo "== P9 ($P9) is not a policy in force of $INSURED_USER (status $STATUS): nothing was checked" >&2
  exit 2
fi
echo "  info  P9 $P9 · P10 ${P10:-<not seeded>}"

echo "== CA-1: the terms of the policy the insured can change"
check "GET current-terms (200)" 200 "$STATUS"
check "the policy is in force" true "$(field .policy.in_force)"
check "the insured value of the policy" 68000000 "$(field .terms.insured_amount)"
check "no beneficiaries designated yet" 0 "$(field '.terms.beneficiaries | length')"
check "the onerous beneficiary cannot change: its basis" outstanding_balance "$(field .terms.onerous_beneficiary.basis)"
echo "  info  onerous beneficiary: $(field '.terms.onerous_beneficiary | "\(.name // "-") (\(.partner_id // "-"))"')"
check "the contact e-mail of the policy is known; address and phone are not registered" "true|null|null" \
  "$(field '"\(.terms.personal_data.email != null)|\(.terms.personal_data.address)|\(.terms.personal_data.phone)"')"
check "nothing open" 0 "$(field '.open_requests | length')"
check "the limits of the capture" "10000000|2000000000|10" \
  "$(field '"\(.limits.insured_amount_min)|\(.limits.insured_amount_max)|\(.limits.max_beneficiaries)"')"

call GET "$LIST?policy_id=$P9" "$INSURED_TOKEN"
check "the list of the requests of P9 (200)" 200 "$STATUS"
BEFORE="$(field .total_items)"

echo "== CA-3: invalid or incomplete data is flagged and nothing is filed"
call POST "$FILE" "$INSURED_TOKEN" '{"change_type":"beneficiaries","channel":"web","beneficiaries":[
  {"full_name":"Mateo Perez Gomez","relationship":"child","document_type":"TI","document_number":"1140223001","share_percent":80},
  {"full_name":"Lucia Perez Gomez","relationship":"child","document_type":"TI","document_number":"1142998310","share_percent":40}]}'
check "shares that add up to 120 % (422)" "422|beneficiaries=share_total_not_100" "$STATUS|$(codes)"
call POST "$FILE" "$INSURED_TOKEN" '{"change_type":"beneficiaries","channel":"web","beneficiaries":[
  {"full_name":"","relationship":"child","document_type":"CC","document_number":"","share_percent":100}]}'
check "empty name and document (422, both at once)" \
  "422|beneficiaries[0].document_number=required beneficiaries[0].full_name=required" "$STATUS|$(codes)"
call POST "$FILE" "$INSURED_TOKEN" '{"change_type":"personal_data","channel":"web","personal_data":{"email":"no-es-un-correo"}}'
check "an invalid e-mail (422)" "422|personal_data.email=invalid_format" "$STATUS|$(codes)"
call POST "$FILE" "$INSURED_TOKEN" '{"change_type":"insured_amount","channel":"web","insured_amount":5000000}'
check "an insured value out of range (422)" "422|insured_amount=out_of_range" "$STATUS|$(codes)"
call POST "$FILE" "$INSURED_TOKEN" '{"change_type":"insured_amount","channel":"web","insured_amount":68000000}'
check "the same insured value (422)" "422|insured_amount=no_change" "$STATUS|$(codes)"
call POST "$FILE" "$INSURED_TOKEN" '{"change_type":"beneficiaries","channel":"web","beneficiaries":[],"onerous_beneficiary":{"partner_id":"otro"}}'
check "the onerous beneficiary never travels (400)" "400|invalid_request" "$STATUS|$(field .error)"
call GET "$LIST?policy_id=$P9" "$INSURED_TOKEN"
check "nothing was filed" "$BEFORE" "$(field .total_items)"

echo "== CA-1 and CA-2: a request of each type, its id and its status"
call POST "$FILE" "$INSURED_TOKEN" '{"change_type":"beneficiaries","channel":"web","beneficiaries":[
  {"full_name":"Mateo Perez Gomez","relationship":"child","document_type":"TI","document_number":"1140223001","share_percent":60},
  {"full_name":"Lucia Perez Gomez","relationship":"child","document_type":"TI","document_number":"1142998310","share_percent":40}]}'
check "beneficiaries filed (201)" 201 "$STATUS"
ID="$(field '.request_id // empty')"
check "the id is SOL-MOD-<year>-<number>" true "$(jq -r '.request_id | test("^SOL-MOD-[0-9]{4}-[0-9]{4,}$")' <<<"$BODY" 2>/dev/null || echo false)"
check "filed, by the insured, on the web portal" "filed|web" "$(field '"\(.status)|\(.channel)"')"
echo "  info  $ID"
call GET "$LIST/$ID" "$INSURED_TOKEN"
check "the detail (200)" 200 "$STATUS"
check "the photo keeps the onerous beneficiary and the requested ones" "outstanding_balance|2|2" \
  "$(field '"\(.previous.onerous_beneficiary.basis)|\(.requested.beneficiaries | length)|\(.changes | length)"')"
call GET "$LIST?policy_id=$P9" "$INSURED_TOKEN"
check "it is listed" "$ID" "$(jq -r --arg id "$ID" '[.items[] | select(.request_id == $id)][0].request_id // empty' <<<"$BODY")"
check "and counted as open" true "$(field '.indicators.open.count >= 1')"
call POST "$FILE" "$INSURED_TOKEN" '{"change_type":"beneficiaries","channel":"web","beneficiaries":[
  {"full_name":"Ana Gomez Ruiz","relationship":"parent","document_type":"CC","document_number":"52123456","share_percent":100}]}'
check "another one of the same type (409 with the open one)" "409|change_request_open|$ID" \
  "$STATUS|$(field .error)|$(field .request_id)"
call POST "$FILE" "$INSURED_TOKEN" '{"change_type":"insured_amount","channel":"web","insured_amount":80000000}'
check "a new insured value (201)" 201 "$STATUS"
call POST "$FILE" "$INSURED_TOKEN" '{"change_type":"personal_data","channel":"web","personal_data":{"address":"Calle 10 # 43-20, Medellin","phone":"3001234567"}}'
check "new contact data (201)" 201 "$STATUS"
call GET "$TERMS" "$INSURED_TOKEN"
check "the three types are open now" 3 "$(field '.open_requests | length')"

echo "== CA-4: only the insured who owns the policy"
for route in "GET $TERMS" "POST $FILE" "GET $LIST" "GET $LIST/$ID"; do
  read -r method path <<<"$route"
  call "$method" "$path" "" '{"change_type":"insured_amount","channel":"web","insured_amount":90000000}'
  check "$method $path without a token (401)" 401 "$STATUS"
done
for who in advisor operations; do
  tok="$ADVISOR_TOKEN"
  [[ "$who" == operations ]] && tok="$OPERATIONS_TOKEN"
  call GET "$TERMS" "$tok"
  check "the $who reads no terms (403)" 403 "$STATUS"
  call POST "$FILE" "$tok" '{"change_type":"insured_amount","channel":"web","insured_amount":90000000}'
  check "the $who files nothing (403)" 403 "$STATUS"
done
call GET "$LIST" "$ADVISOR_TOKEN"
check "the advisor lists nothing (403)" 403 "$STATUS"
if [[ -n "$P10" && -n "$INSURED2_TOKEN" ]]; then
  call GET "/v1/policies/$P10/current-terms" "$INSURED_TOKEN"
  check "the policy of another insured: no terms (403)" 403 "$STATUS"
  call POST "/v1/policies/$P10/change-requests" "$INSURED_TOKEN" '{"change_type":"insured_amount","channel":"web","insured_amount":90000000}'
  check "the policy of another insured: nothing filed (403)" 403 "$STATUS"
  call GET "$LIST?policy_id=$P10" "$INSURED2_TOKEN"
  check "the owner of P10 has no request on it" "200|0" "$STATUS|$(field .total_items)"
  call GET "$LIST" "$INSURED2_TOKEN"
  check "the list of the second insured never shows the requests of the first one" "200|0" \
    "$STATUS|$(jq -r --arg p "$P9" '[.items[] | select(.policy_id == $p)] | length' <<<"$BODY" 2>/dev/null || echo x)"
  call GET "$LIST/$ID" "$INSURED2_TOKEN"
  check "nor reads one of them (403)" 403 "$STATUS"
else
  skip "another insured in the cloud: $INSURED2_USER or P10 does not exist (chore/T4-second-insured-user); local evidence in change_request_test.go"
fi

echo "== D-2: a policy of the policy core is not origination's"
call GET /v1/policies "$INSURED_TOKEN"
CORE="$(field '[.items[] | select(.source == "policy_core") | .policy_id][0] // empty')"
if [[ -n "$CORE" ]]; then
  call POST "/v1/policies/$CORE/change-requests" "$INSURED_TOKEN" '{"change_type":"insured_amount","channel":"web","insured_amount":90000000}'
  check "a change of $CORE (404 policy_not_found)" "404|policy_not_found" "$STATUS|$(field .error)"
else
  skip "the wallet has no policy of the policy core"
fi

echo
if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-W1.4-01 cloud checks FAILED: $FAILS failed, $SKIPS skipped"
  exit 1
fi
echo "== HU-W1.4-01 cloud checks passed ($SKIPS skipped)."
