#!/usr/bin/env bash
# API checks of the controlled application of the endorsement (HU-W1.4-02) for
# the cloud window (staging), through the portal of CloudFront, as the browser
# calls it, and only with the permanent users of bootstrap/identity. It buys
# its own policies through the real journey and files their requests as the
# insured, so the seeded E1..E6 of tests/e2e/hu-w1.5/assisted-renewal-cloud.sh
# --seed stay for the demonstration. It never creates users and never prints a
# password or a token: every datum lives in the ephemeral platform and goes
# away with down.sh.
#
# Usage:
#   endorsement-cloud.sh verify
#
# Blocks (V1..V6 are its own policies):
#   CA-1/CA-2/CA-5  V1 beneficiaries: inbox, take (in review for the insured), detail with five validations passed
#                   (the registry of KYC and claims answer inside the VPC), apply: END-<year>-<number>, the insured
#                   sees it applied and the current terms carry the beneficiaries
#   CA-3            V2 insured value: the premium of the engine v1; applied, the wallet of the insured and the panel
#                   of the analyst show the new value and premium
#   CA-2/CA-5       V3 a document ending in 000: not applied, the policy intact; returning without observations 422,
#                   with them the insured sees it rejected with the reason
#   CA-5            V4 rejecting without observations 422, with them rejected, applying it afterwards 409
#   concurrency     V5 two applications at once: one endorsement, both answers with the same one
#   CA-4            the history of V1 (the endorsement and its author) and of V3 (the attempt)
#   roles           without a token 401; the insured and the advisor 403 on the seven routes
#   D-12            (with the AWS CLI, read only) the e-mail stub logged one message per decision
#
# Inputs (environment; the password and the tokens are never printed):
#   PORTAL_URL           base URL of the portal (CloudFront)
#   SEED_USER_PASSWORD   password of the permanent users (or SOLVENTA_IDENTITY_SEED_PASSWORD)
#   INSURED_USER         default asegurado@example.com
#   ADVISOR_USER         default asesor@example.com
#   OPERATIONS_USER      default operaciones@example.com
#   OUTPUT_FILE          optional: a copy of the output (for the evidence)
#   RUN_ID               suffix of the fictitious customers (default v<epoch seconds>)
#   COGNITO_ENDPOINT     default https://cognito-idp.<region of config.json>.amazonaws.com/
#                        (a local endpoint only to rehearse the script)
#   LOG_GROUP            log group of staging for the D-12 check; default: the output log_group of
#                        PLATFORM_TF_DIR (infra/terraform/environments/staging/platform)
#   AWS_REGION           default us-east-2
#
# It needs curl and jq (and the AWS CLI only for D-12, read only: logs
# filter-log-events). It exits with a non-zero code when any check fails.
set -euo pipefail
set +x

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

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
export AWS_PAGER=""
REGION="${AWS_REGION:-us-east-2}"
PLATFORM_TF_DIR="${PLATFORM_TF_DIR:-$REPO_ROOT/infra/terraform/environments/staging/platform}"
PASSWORD="${SEED_USER_PASSWORD:-${SOLVENTA_IDENTITY_SEED_PASSWORD:-}}"
[[ -n "${PORTAL_URL:-}" ]] || { echo "PORTAL_URL is required (the CloudFront URL of the portal)" >&2; exit 2; }
[[ -n "$PASSWORD" ]] || { echo "SEED_USER_PASSWORD (or SOLVENTA_IDENTITY_SEED_PASSWORD) is required" >&2; exit 2; }
PORTAL_URL="${PORTAL_URL%/}"
INSURED_USER="${INSURED_USER:-asegurado@example.com}"
ADVISOR_USER="${ADVISOR_USER:-asesor@example.com}"
OPERATIONS_USER="${OPERATIONS_USER:-operaciones@example.com}"
RUN_ID="${RUN_ID:-v$(date +%s)}"
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
# A GET never carries the body: CloudFront answers its own 403 to a GET with a
# body before it reaches the gateway, so the role checks, which reuse one body
# for every route, would read that 403 instead of the answer of the service.
call() {
  : >"$WORK/header"
  [[ -n "${3:-}" ]] && printf 'Authorization: Bearer %s\n' "$3" >>"$WORK/header"
  printf 'User-Agent: hu-w1.4-02 endorsement-cloud.sh\n' >>"$WORK/header"
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
COGNITO_REGION="$(field '.cognito.region // "us-east-2"')"
[[ -n "$CLIENT_ID" ]] || { echo "The config.json of the portal has no Cognito client" >&2; exit 1; }
COGNITO_ENDPOINT="${COGNITO_ENDPOINT:-https://cognito-idp.$COGNITO_REGION.amazonaws.com/}"
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
# insured through the real journey (consent through the partner route, the
# offer, the acceptance and the card 4111); sets POLICY.
buy() {
  local customer="cliente-$1-$RUN_ID" app="app-$1-$RUN_ID" consent offer acceptance tok brand last4 i
  call POST /v1/audit/consents "" "{\"customer_id\":\"$customer\",\"document_id\":\"$3\",\"full_name\":\"$2\",
    \"terms_version\":\"$TERMS_VERSION\",\"accepted_sources\":[$TERMS_SOURCES],\"accepted_data_policy\":true,
    \"channel\":\"embedded\",\"partner_id\":\"portal-embebido\",\"application_id\":\"$app\"}"
  [[ "$STATUS" == 201 || "$STATUS" == 200 ]] || { echo "the consent of $1 answered $STATUS: $(field .error)" >&2; exit 1; }
  consent="$(field .consent_id)"
  call POST /v1/offers "" "{\"customer_id\":\"$customer\",\"consent_id\":\"$consent\",\"partner_id\":\"portal-embebido\",
    \"application_id\":\"$app\",\"document_id\":\"$3\",\"full_name\":\"$2\",\"email\":\"titular.$1.$RUN_ID@example.com\",
    \"birth_date\":\"1985-03-14\",\"insured_amount\":68000000,\"term_months\":12}"
  [[ "$STATUS" == 201 ]] || { echo "the offer of $1 answered $STATUS: $(field .error)" >&2; exit 1; }
  offer="$(field .offer_id)"
  call GET "/v1/offers/$offer" "$INSURED_TOKEN"
  call POST "/v1/offers/$offer/acceptance" "$INSURED_TOKEN" '{"channel":"web"}'
  [[ "$STATUS" == 202 ]] || { echo "the acceptance of $1 answered $STATUS: $(field .error)" >&2; exit 1; }
  acceptance="$(field .acceptance_id)"
  for i in $(seq 1 60); do
    call GET "/v1/acceptances/$acceptance/charge" "$INSURED_TOKEN"
    [[ "$STATUS" == 200 ]] && break
    sleep 2
  done
  [[ "$STATUS" == 200 ]] || { echo "the charge of $1 was not created ($STATUS)" >&2; exit 1; }
  call POST /pasarela/tokens "" '{"number":"4111111111111111","holder":"Prueba Endoso"}'
  tok="$(field .token)" brand="$(field .brand)" last4="$(field .last4)"
  call POST "/v1/acceptances/$acceptance/charge/payment-method" "$INSURED_TOKEN" \
    "{\"method\":\"card\",\"payment_token\":\"$tok\",\"brand\":\"$brand\",\"last4\":\"$last4\",\"installments\":1}"
  [[ "$STATUS" == 202 ]] || { echo "the card of $1 answered $STATUS: $(field .error)" >&2; exit 1; }
  POLICY=""
  for i in $(seq 1 60); do
    call GET "/v1/acceptances/$acceptance/charge" "$INSURED_TOKEN"
    if [[ "$(field .status)" == confirmed && "$(field '.policy_id // empty')" != "" ]]; then
      POLICY="$(field .policy_id)"
      break
    fi
    sleep 2
  done
  [[ -n "$POLICY" ]] || { echo "the policy of $1 was not issued: $(field .status)" >&2; exit 1; }
  for i in $(seq 1 30); do
    call GET "/v1/policies/$POLICY/current-terms" "$INSURED_TOKEN"
    [[ "$STATUS" == 200 ]] && break
    sleep 2
  done
  echo "  info  $1 $POLICY"
}

# file <policy> <body>: the insured files a change request; sets REQ.
file() {
  call POST "/v1/policies/$1/change-requests" "$INSURED_TOKEN" "$2"
  [[ "$STATUS" == 201 ]] || { echo "the request on $1 answered $STATUS: $(field .error)" >&2; exit 1; }
  REQ="$(field .request_id)"
}
beneficiaries() { # <document of the second beneficiary>
  printf '{"change_type":"beneficiaries","channel":"web","beneficiaries":[%s,%s]}' \
    '{"full_name":"Mateo Perez Gomez","relationship":"child","document_type":"TI","document_number":"1140223001","share_percent":60}' \
    "{\"full_name\":\"Lucia Perez Gomez\",\"relationship\":\"child\",\"document_type\":\"TI\",\"document_number\":\"$1\",\"share_percent\":40}"
}
ER=/v1/policies/endorsement-requests
insured_view() { # <request>: the stored status and the decision reason as the insured reads them
  call GET "/v1/policies/change-requests/$1" "$INSURED_TOKEN"
  echo "$(field .status) $(field .decision_reason)"
}
STARTED_MS="$(( ($(date +%s) - 60) * 1000 ))"

echo "== Policies of this verification (run $RUN_ID)"
buy v1 "Carla Nunez Prada" 1083012345
V1="$POLICY"
buy v2 "Hugo Pineda Sosa" 1083123456
V2="$POLICY"
buy v3 "Irene Valencia Mora" 1083234567
V3="$POLICY"
buy v4 "Oscar Benitez Cruz" 1083345678
V4="$POLICY"
buy v5 "Rosa Quiroga Lema" 1083456789
V5="$POLICY"

echo "== CA-1, CA-2, CA-5: beneficiaries applied"
file "$V1" "$(beneficiaries 1142998310)"
R1="$REQ"
call GET "$ER?page_size=50" "$OPERATIONS_TOKEN"
check "the inbox answers 200 with the request pending" "200 filed" "$STATUS $(field ".items[] | select(.request_id == \"$R1\") | .status")"
check "the indicators count the pending requests" true "$(field '.indicators.pending.count >= 1')"
call POST "$ER/$R1/review" "$OPERATIONS_TOKEN"
check "taking it: 200 in_review" "200 in_review" "$STATUS $(field .request.status)"
check "the insured sees it in review" "in_review null" "$(insured_view "$R1")"
call GET "$ER/$R1" "$OPERATIONS_TOKEN"
check "the detail: two rows and five validations passed" "2 5 passed" \
  "$(field '"\(.changes | length) \(.validations | length) \([.validations[].outcome] | unique | join(","))"')"
check "the registry of KYC checked the two new documents" 2 "$(field '.validations[] | select(.code == "identity_documents_valid") | .params.checks')"
check "claims answered inside the VPC: no claim in progress" 0 "$(field '.validations[] | select(.code == "no_open_claim") | .params.open_claims')"
call POST "$ER/$R1/application" "$OPERATIONS_TOKEN"
check "applying: 200 applied" "200 applied" "$STATUS $(field .outcome)"
END1="$(field .endorsement.endorsement_id)"
check "with an END number" true "$([[ "$END1" =~ ^END-[0-9]{4}-[0-9]{4,}$ ]] && echo true || echo false)"
check "the insured sees it applied" "applied null" "$(insured_view "$R1")"
call GET "/v1/policies/$V1/current-terms" "$INSURED_TOKEN"
check "the current terms carry the beneficiaries" 2 "$(field '.terms.beneficiaries | length')"

echo "== CA-3: a new insured value everywhere"
file "$V2" '{"change_type":"insured_amount","channel":"web","insured_amount":120000000}'
R2="$REQ"
call GET "$ER/$R2" "$OPERATIONS_TOKEN"
check "the engine v1 prices the new premium" "true v1" "$(field '"\(.premium.new > .premium.previous) \(.premium.rating_version)"')"
NEW_PREMIUM="$(field .premium.new)"
call POST "$ER/$R2/application" "$OPERATIONS_TOKEN"
check "applying: applied" applied "$(field .outcome)"
call GET /v1/policies "$INSURED_TOKEN"
check "the wallet of the insured (web and phone) shows the new value and premium" "120000000 $NEW_PREMIUM" \
  "$(field ".items[] | select(.policy_id == \"$V2\") | \"\(.insured_amount) \(.premium)\"")"
call GET "$ER/$R2" "$OPERATIONS_TOKEN"
check "the panel of the analyst shows them" "120000000 $NEW_PREMIUM" "$(field '"\(.policy.insured_amount) \(.policy.premium)"')"

echo "== CA-2, CA-5: not applied, then returned"
file "$V3" "$(beneficiaries 1142998000)"
R3="$REQ"
call POST "$ER/$R3/application" "$OPERATIONS_TOKEN"
check "applying: not_applied by identity_documents_valid" "not_applied identity_documents_valid" \
  "$(field .outcome) $(field '.attempt.failed | join(",")')"
call GET "/v1/policies/$V3/current-terms" "$INSURED_TOKEN"
check "the policy keeps its terms" 0 "$(field '.terms.beneficiaries | length')"
call POST "$ER/$R3/return" "$OPERATIONS_TOKEN" '{"observations":"corto"}'
check "returning without observations: 422" "422 observations_required" "$STATUS $(field .error)"
call POST "$ER/$R3/return" "$OPERATIONS_TOKEN" '{"observations":"El documento de Lucia no esta en la Registraduria. Corrigelo y radica de nuevo."}'
check "returning with observations: 200 returned" "200 true" "$STATUS $(field .request.returned)"
check "the insured sees the reason" "not_applied El documento de Lucia no esta en la Registraduria. Corrigelo y radica de nuevo." \
  "$(insured_view "$R3")"

echo "== CA-5: rejected"
file "$V4" '{"change_type":"insured_amount","channel":"web","insured_amount":90000000}'
R4="$REQ"
call POST "$ER/$R4/rejection" "$OPERATIONS_TOKEN" '{"observations":" "}'
check "rejecting without observations: 422" 422 "$STATUS"
call POST "$ER/$R4/rejection" "$OPERATIONS_TOKEN" '{"observations":"La suma pedida no corresponde al saldo del credito."}'
check "rejecting with observations: 200 rejected" "200 rejected" "$STATUS $(field .request.status)"
call POST "$ER/$R4/application" "$OPERATIONS_TOKEN"
check "applying it afterwards: 409" "409 request_already_decided" "$STATUS $(field .error)"

echo "== Two applications at once"
file "$V5" "$(beneficiaries 1142998310)"
R5="$REQ"
for i in 1 2; do
  printf 'Authorization: Bearer %s\nUser-Agent: hu-w1.4-02 endorsement-cloud.sh\n' "$OPERATIONS_TOKEN" >"$WORK/h$i"
  curl -s --max-time 30 -o "$WORK/b$i" -w '%{http_code}' -X POST -H @"$WORK/h$i" "$PORTAL_URL$ER/$R5/application" >"$WORK/s$i" &
done
wait
check "both answer 200" "200 200" "$(cat "$WORK/s1") $(cat "$WORK/s2")"
check "with the same endorsement" "$(jq -r .endorsement.endorsement_id <"$WORK/b1")" "$(jq -r .endorsement.endorsement_id <"$WORK/b2")"
call GET "/v1/policies/$V5/endorsements" "$OPERATIONS_TOKEN"
check "and one in the history" 1 "$(field '.entries | length')"

echo "== CA-4: the history"
call GET "/v1/policies/$V1/endorsements" "$OPERATIONS_TOKEN"
check "V1: the endorsement with its author" "200 applied $END1" "$STATUS $(field '.entries[0].outcome') $(field '.entries[0].endorsement_id')"
echo "  info  author: $(field '.entries[0].by.name') ($(field '.entries[0].by.role'))"
call GET "/v1/policies/$V3/endorsements" "$OPERATIONS_TOKEN"
check "V3: the attempt with the validation that failed" "not_applied identity_documents_valid" \
  "$(field '.entries[0].outcome') $(field '.entries[0].failed | join(",")')"

echo "== Roles"
for route in "GET $ER" "GET $ER/$R1" "POST $ER/$R1/review" "POST $ER/$R1/application" "POST $ER/$R1/rejection" \
  "POST $ER/$R1/return" "GET /v1/policies/$V1/endorsements"; do
  read -r method path <<<"$route"
  call "$method" "$path" "" '{"observations":"No procede el cambio pedido"}'
  no_token="$STATUS"
  call "$method" "$path" "$INSURED_TOKEN" '{"observations":"No procede el cambio pedido"}'
  insured="$STATUS"
  call "$method" "$path" "$ADVISOR_TOKEN" '{"observations":"No procede el cambio pedido"}'
  check "$method $path: 401 without a token, 403 insured and advisor" "401 403 403" "$no_token $insured $STATUS"
done

echo "== D-12: one e-mail per decision (CloudWatch Logs, read only)"
if [[ -z "${LOG_GROUP:-}" ]]; then
  LOG_GROUP="$(terraform -chdir="$PLATFORM_TF_DIR" output -no-color -raw log_group 2>/dev/null || true)"
  [[ "$LOG_GROUP" == *"No outputs found"* || "$LOG_GROUP" == *"Warning:"* ]] && LOG_GROUP=""
fi
if [[ -n "$LOG_GROUP" ]] && command -v aws >/dev/null; then
  sleep 10
  MAIL_LOG="$(MSYS_NO_PATHCONV=1 PYTHONUTF8=1 aws logs filter-log-events --region "$REGION" --log-group-name "$LOG_GROUP" \
    --log-stream-name-prefix email --start-time "$STARTED_MS" --filter-pattern '"endorsement-outcome"' \
    --query 'events[].message' --output text 2>/dev/null | tr '\t' '\n' | tr -d '\r' || true)"
  for r in "$R1" "$R3" "$R4"; do
    check "the e-mail stub logged the decision on $r" 1 "$(grep -c "idempotency_key=endorsement-outcome:$r " <<<"$MAIL_LOG" || true)"
  done
else
  skip "D-12 needs the AWS CLI and the log group of staging (LOG_GROUP)"
fi

echo
echo "== $FAILS failed, $SKIPS skipped (V1 $V1 · V2 $V2 · V3 $V3 · V4 $V4 · V5 $V5)"
[[ "$FAILS" == 0 ]] || exit 1
echo "== TODO OK: the controlled application of the endorsement (HU-W1.4-02) passed in the cloud."
