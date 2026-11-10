#!/usr/bin/env bash
# The single seed and the API checks of the renewal stories (HU-W1.5-01 and
# HU-W1.5-03) for the cloud window (staging), always through the portal of
# CloudFront, as the browser calls it, and only with the permanent users of
# bootstrap/identity. It never creates users and never prints a password or a
# token: every seeded datum lives in the ephemeral platform and disappears with
# down.sh.
#
# Usage:
#   assisted-renewal-cloud.sh --seed           buys P1..P7 through the real journey and writes SEED_FILE
#   assisted-renewal-cloud.sh verify           buys its own policies and checks CA-1..CA-4, D-8, D-9, roles
#                                              and concurrency by API (the seeded P1..P7 stay for the demo)
#   assisted-renewal-cloud.sh code <policy_id> reads from CloudWatch Logs (read only) the last confirmation
#                                              code e-mailed to the holder of a seeded policy, for the demo
#
# The real journey of every policy: consent of the holder in audit through the
# partner route (CloudFront adds the API key), POST /v1/offers, the insured
# opens and accepts the offer, the card 4111 is tokenised at /pasarela/tokens
# and handed over, and the charge is confirmed. The seeded policies, with
# fictitious holders (@example.com), all owned by the insured:
#   P1 12 months  happy path, CA-1..CA-4         P5 12 months  consent revoked (D-8)
#   P2 12 months  degraded mode (CA-5)           P6 12 months  confirmation code (D-9)
#   P3 12 months  renewed online by the insured  P7 24 months  outside the window
#   P4 12 months  failed payment (01·D-3 a+)
# A policy without a payment method cannot be produced through the real journey
# (every policy is born of a confirmed charge with a captured method), nor one
# born in the assisted channel without creating a user: both are local only.
#
# Inputs (environment; the password and the tokens are never printed):
#   PORTAL_URL           base URL of the portal (CloudFront); required by --seed and verify
#   SEED_USER_PASSWORD   password of the permanent users (or SOLVENTA_IDENTITY_SEED_PASSWORD)
#   SEED_FILE            where --seed writes the seeded ids and code reads them
#                        (default: a file in the temporary directory, printed at the end)
#   INSURED_USER         default asegurado@example.com
#   ADVISOR_USER         default asesor@example.com
#   OPERATIONS_USER      default operaciones@example.com
#   OUTPUT_FILE          optional: a copy of the output (for the evidence)
#   RUN_ID               suffix of the fictitious customers (default r<epoch seconds>)
#   COGNITO_ENDPOINT     default https://cognito-idp.<region of config.json>.amazonaws.com/
#                        (a local endpoint only to rehearse the script)
#   LOG_GROUP            log group of staging (code, and D-9 of verify); default: the output
#                        log_group of PLATFORM_TF_DIR (infra/terraform/environments/staging/platform)
#   AWS_REGION           default us-east-2
#
# It needs curl and jq; code and the D-9 check of verify also need the AWS CLI
# (logs filter-log-events, read only). It exits with a non-zero code when any
# check fails.
set -euo pipefail
set +x

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

usage() {
  awk 'NR > 1 && /^set -euo pipefail/ { exit } NR > 1 { print }' "$0"
  exit "${1:-0}"
}
COMMAND="${1:-}"
case "$COMMAND" in
  --seed | verify) ;;
  code) [[ -n "${2:-}" ]] || { echo "code needs the policy id: assisted-renewal-cloud.sh code <policy_id>" >&2; exit 2; } ;;
  -h | --help) usage 0 ;;
  *) usage 2 ;;
esac

for tool in curl jq; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done
export AWS_PAGER=""
REGION="${AWS_REGION:-us-east-2}"
PLATFORM_TF_DIR="${PLATFORM_TF_DIR:-$REPO_ROOT/infra/terraform/environments/staging/platform}"
RUN_ID="${RUN_ID:-r$(date +%s)}"
SEED_FILE="${SEED_FILE:-${TMPDIR:-/tmp}/hu-w1.5-seed-$RUN_ID.env}"
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

# Read-only AWS CLI calls: keep Git Bash from rewriting /ecs/... and print UTF-8 on Windows.
aws_ro() { MSYS_NO_PATHCONV=1 PYTHONUTF8=1 aws "$@"; }
log_group() {
  if [[ -z "${LOG_GROUP:-}" ]]; then
    LOG_GROUP="$(terraform -chdir="$PLATFORM_TF_DIR" output -no-color -raw log_group 2>/dev/null || true)"
    [[ "$LOG_GROUP" == *"No outputs found"* || "$LOG_GROUP" == *"Warning:"* ]] && LOG_GROUP=""
  fi
  printf '%s' "${LOG_GROUP:-}"
}

# last_code <holder e-mail> <since epoch ms>: the code of the last renewal e-mail
# the e-mail stub accepted for the holder (its log line carries the body).
last_code() {
  local group
  group="$(log_group)"
  [[ -n "$group" ]] && command -v aws >/dev/null || return 1
  aws_ro logs filter-log-events --region "$REGION" --log-group-name "$group" --log-stream-name-prefix email \
    --start-time "$2" --filter-pattern '"message accepted"' --query 'events[].message' --output text 2>/dev/null |
    tr '\t' '\n' | tr -d '\r' | grep -F "to=$1 " | grep -F 'renovación' | tail -n 1 |
    sed -n 's/.*body="\(.*\)"$/\1/p' | sed 's/[\]n/ /g' | grep -oE '\b[0-9]{6}\b' | head -n 1
}

if [[ "$COMMAND" == code ]]; then
  POLICY="$2"
  [[ -f "$SEED_FILE" ]] || { echo "SEED_FILE ($SEED_FILE) not found: run --seed first or set SEED_FILE" >&2; exit 2; }
  HOLDER="$(sed -n "s/^EMAIL_${POLICY//[^A-Za-z0-9]/_}=//p" "$SEED_FILE" | tail -n 1)"
  [[ -n "$HOLDER" ]] || { echo "$POLICY is not a policy of the seed in $SEED_FILE" >&2; exit 2; }
  command -v aws >/dev/null || { echo "the AWS CLI is required to read the log (read only)" >&2; exit 1; }
  [[ -n "$(log_group)" ]] || { echo "LOG_GROUP is required (or the output log_group of $PLATFORM_TF_DIR)" >&2; exit 2; }
  SINCE="$(( ($(date +%s) - 3600) * 1000 ))"
  for _ in $(seq 1 24); do
    CODE="$(last_code "$HOLDER" "$SINCE" || true)"
    [[ -n "$CODE" ]] && { echo "Código de la renovación de $POLICY: $CODE"; exit 0; }
    sleep 5
  done
  echo "No renewal code was e-mailed to the holder of $POLICY in the last hour" >&2
  exit 1
fi

PASSWORD="${SEED_USER_PASSWORD:-${SOLVENTA_IDENTITY_SEED_PASSWORD:-}}"
[[ -n "${PORTAL_URL:-}" ]] || { echo "PORTAL_URL is required (the CloudFront URL of the portal)" >&2; exit 2; }
[[ -n "$PASSWORD" ]] || { echo "SEED_USER_PASSWORD (or SOLVENTA_IDENTITY_SEED_PASSWORD) is required" >&2; exit 2; }
PORTAL_URL="${PORTAL_URL%/}"
INSURED_USER="${INSURED_USER:-asegurado@example.com}"
ADVISOR_USER="${ADVISOR_USER:-asesor@example.com}"
OPERATIONS_USER="${OPERATIONS_USER:-operaciones@example.com}"
json_escape() { local s=${1//\\/\\\\}; printf '%s' "${s//\"/\\\"}"; }

# call <method> <path> [token] [json body] [extra header]: sets STATUS and BODY. The
# token goes through a private header file, never through the command line.
call() {
  : >"$WORK/header"
  [[ -n "${3:-}" ]] && printf 'Authorization: Bearer %s\n' "$3" >>"$WORK/header"
  [[ -n "${5:-}" ]] && printf '%s\n' "$5" >>"$WORK/header"
  printf 'User-Agent: hu-w1.5-03 assisted-renewal-cloud.sh\n' >>"$WORK/header"
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

# The client of the user pool, from the config.json of the portal.
call GET /config.json
[[ "$STATUS" == 200 ]] || { echo "The config.json of the portal answered $STATUS" >&2; exit 1; }
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

# buy <tag> <holder name> <document> <term months>: buys a policy through the real
# journey for the insured; sets POLICY, CONSENT and HOLDER (the fictitious e-mail).
buy() {
  local customer="cliente-$1-$RUN_ID" app="app-$1-$RUN_ID" offer acceptance tok brand last4 i
  HOLDER="titular.$1.$RUN_ID@example.com"
  call POST /v1/audit/consents "" "{\"customer_id\":\"$customer\",\"document_id\":\"$3\",\"full_name\":\"$2\",
    \"terms_version\":\"$TERMS_VERSION\",\"accepted_sources\":[$TERMS_SOURCES],\"accepted_data_policy\":true,
    \"channel\":\"embedded\",\"partner_id\":\"portal-embebido\",\"application_id\":\"$app\"}"
  [[ "$STATUS" == 201 || "$STATUS" == 200 ]] || { echo "the consent of $1 answered $STATUS: $(field .error)" >&2; return 1; }
  CONSENT="$(field .consent_id)"
  call POST /v1/offers "" "{\"customer_id\":\"$customer\",\"consent_id\":\"$CONSENT\",\"partner_id\":\"portal-embebido\",
    \"application_id\":\"$app\",\"document_id\":\"$3\",\"full_name\":\"$2\",\"email\":\"$HOLDER\",\"birth_date\":\"1985-03-14\",
    \"insured_amount\":68000000,\"term_months\":$4}"
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
  call POST /pasarela/tokens "" '{"number":"4111111111111111","holder":"Prueba Renovacion"}'
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
    call GET "/v1/policies/$POLICY/assisted-renewal" "$ADVISOR_TOKEN"
    [[ "$STATUS" == 200 ]] && break
    sleep 2
  done
  [[ "$STATUS" == 200 ]] || { echo "the policy $POLICY of $1 is not visible to the advisor ($STATUS)" >&2; return 1; }
}

if [[ "$COMMAND" == --seed ]]; then
  echo "== Seed of HU-W1.5-01 and HU-W1.5-03 (run $RUN_ID), owned by $INSURED_USER"
  : >"$SEED_FILE"
  printf '# Seed of the renewal stories, run %s (fictitious data of the ephemeral staging window)\n' "$RUN_ID" >>"$SEED_FILE"
  SEEDS=(
    "P1|Carolina Perez Ruiz|1020345678|12|happy path, CA-1..CA-4"
    "P2|Mateo Rios Lara|1030456789|12|degraded mode (CA-5)"
    "P3|Valentina Soto Gil|1040567890|12|renewed online by the insured (read only, concurrency)"
    "P4|Andres Mora Paz|1050678901|12|failed payment (01·D-3 a+)"
    "P5|Lucia Vega Rey|1060789012|12|consent revoked (D-8)"
    "P6|Sofia Duarte Leon|1070890123|12|confirmation code (D-9)"
    "P7|Jorge Molina Castro|1080901234|24|outside the window"
  )
  for seed in "${SEEDS[@]}"; do
    IFS='|' read -r tag name document months purpose <<<"$seed"
    buy "$(tr '[:upper:]' '[:lower:]' <<<"$tag")" "$name" "$document" "$months"
    printf '%s=%s\nCONSENT_%s=%s\nEMAIL_%s=%s\n' "$tag" "$POLICY" "$tag" "$CONSENT" "${POLICY//[^A-Za-z0-9]/_}" "$HOLDER" >>"$SEED_FILE"
    echo "  $tag  $POLICY  $months months  $name ($HOLDER) · $purpose"
  done
  echo
  echo "== Seeded. The ids are in $SEED_FILE"
  echo "   HU-W1.5-01: RENEW_ONLINE_POLICY_ID=$(sed -n 's/^P3=//p' "$SEED_FILE") ./tests/e2e/hu-w1.5/expiring-cloud.sh"
  echo "   Renovar:    $PORTAL_URL/originacion/renovaciones/$(sed -n 's/^P1=//p' "$SEED_FILE")"
  exit 0
fi

# --- verify: its own policies, so P1..P7 stay for the demonstration ----------------
STARTED_MS="$(( ($(date +%s) - 60) * 1000 ))"
NOW() { date -u +%Y-%m-%dT%H:%M:%SZ; }
formalize_body() { # <requote> <method> <reference> [code]
  printf '{"requote_id":"%s","acceptance":{"method":"%s","accepted_at":"%s","reference":"%s"},"confirmation_code":"%s"}' \
    "$1" "$2" "$(NOW)" "$3" "${4:-}"
}
ar() { echo "/v1/policies/$1/assisted-renewal"; }

echo "== verify (run $RUN_ID): buying the policies of the checks"
buy v1 "Prueba Uno Verificacion" 1090012345 12
V1="$POLICY"
buy v2 "Prueba Dos Verificacion" 1090023456 12
V2="$POLICY" V2_HOLDER="$HOLDER"
buy v3 "Prueba Tres Verificacion" 1090034567 12
V3="$POLICY" V3_CONSENT="$CONSENT"
buy v4 "Prueba Cuatro Verificacion" 1090045678 12
V4="$POLICY"
echo "  info  V1 $V1 · V2 $V2 · V3 $V3 · V4 $V4"

echo "== CA-1: the advisor requotes with the engine"
call GET "$(ar "$V1")" "$ADVISOR_TOKEN"
check "GET assisted-renewal (200)" 200 "$STATUS"
check "it can be requoted; the consent is verified with audit" "true|audit" "$(field '"\(.can_requote)|\(.consent_mode)"')"
call POST "$(ar "$V1")/requotes" "$ADVISOR_TOKEN" '{}'
check "the requote answers 201 with the engine v1" "201|v1" "$STATUS|$(field .rating_version)"
check "requoted, with the discount and the factors of the engine" "true|0.08|true" \
  "$(field '"\(.requoted)|\(.discount_rate)|\(.applied_rules | length > 0)"')"
check "the answer carries no score" 0 "$(grep -c '"score"' <<<"$BODY" || true)"
echo "  info  mode $(field .mode), source $(field .source), sources $(field '[.sources[] | "\(.source)=\(.status)"] | join(" ")')"
R1="$(field .requote_id)"
PREMIUM1="$(field .renewal.premium)"

echo "== CA-2: nothing is formalized without a valid acceptance"
call POST "$(ar "$V1")/formalization" "$ADVISOR_TOKEN" "{\"requote_id\":\"$R1\"}"
check "without acceptance: 422 validation_failed" "422|validation_failed" "$STATUS|$(field .error)"
call POST "$(ar "$V1")/formalization" "$ADVISOR_TOKEN" "$(formalize_body "$R1" remote_signature "")"
check "a signature without reference: 422 on acceptance.reference" "422|acceptance.reference" "$STATUS|$(field '.fields[0].field')"
call GET "$(ar "$V1")" "$ADVISOR_TOKEN"
check "no renewal was stored" null "$(field .renewal)"

echo "== CA-3: formalizing charges and issues the renewed policy"
call POST "$(ar "$V1")/formalization" "$ADVISOR_TOKEN" "$(formalize_body "$R1" recorded_call GRAB-NUBE-0001)"
check "the formalization answers 201, channel advisor" "201|advisor" "$STATUS|$(field .renewal.channel)"
NEW1=""
for _ in $(seq 1 60); do
  call GET "$(ar "$V1")" "$ADVISOR_TOKEN"
  [[ "$(field .renewal.status)" == renewed ]] && { NEW1="$(field .renewal.new_policy_id)"; break; }
  sleep 2
done
check "the renewal is renewed with a new policy" true "$([[ -n "$NEW1" && "$NEW1" != null ]] && echo true || echo false)"
check "the charge is confirmed" confirmed "$(field .charge.status)"
ISSUED=no
for _ in $(seq 1 45); do
  call GET "/v1/policies/$NEW1/issuance-status" "$ADVISOR_TOKEN"
  [[ "$(field .status)" == completed ]] && { ISSUED=yes; break; }
  sleep 2
done
check "the certificate of the renewed policy was issued" yes "$ISSUED"
call GET /v1/policies "$INSURED_TOKEN"
check "the insured sees the new policy, renewal of V1" "$V1" "$(field "[.items[] | select(.policy_id == \"$NEW1\") | .renewal_of][0] // empty")"
check "with the requoted premium" "$PREMIUM1" "$(field "[.items[] | select(.policy_id == \"$NEW1\") | .premium][0] // empty")"

echo "== CA-4: the record of the advisor"
call GET "$(ar "$V1")" "$ADVISOR_TOKEN"
check "the advisor, the method, the reference and the requote applied" "$ADVISOR_USER|recorded_call|GRAB-NUBE-0001|$R1" \
  "$(field '"\(.formalization.advisor_email)|\(.formalization.acceptance.method)|\(.formalization.acceptance.reference)|\(.formalization.requote_id)"')"
echo "  info  advisor_name: $(field '.formalization.advisor_name // "null (the screen shows the e-mail)"'), formalized_at $(field .formalization.formalized_at)"

echo "== D-9: the confirmation code e-mailed to the holder"
call POST "$(ar "$V2")/requotes" "$ADVISOR_TOKEN" '{}'
R2="$(field .requote_id)"
call POST "$(ar "$V2")/consent" "$ADVISOR_TOKEN" "{\"requote_id\":\"$R2\"}"
check "the code is sent to the masked address (200)" "200|ti***@example.com" "$STATUS|$(field .sent_to)"
CODE=""
if [[ -n "$(log_group)" ]] && command -v aws >/dev/null; then
  for _ in $(seq 1 24); do
    CODE="$(last_code "$V2_HOLDER" "$STARTED_MS" || true)"
    [[ -n "$CODE" ]] && break
    sleep 5
  done
fi
if [[ -n "$CODE" ]]; then
  WRONG=000000
  [[ "$CODE" == "$WRONG" ]] && WRONG=111111
  call POST "$(ar "$V2")/formalization" "$ADVISOR_TOKEN" "$(formalize_body "$R2" confirmation_code "" "$WRONG")"
  check "a wrong code: 422 invalid_confirmation_code" "422|invalid_confirmation_code" "$STATUS|$(field .error)"
  call POST "$(ar "$V2")/formalization" "$ADVISOR_TOKEN" "$(formalize_body "$R2" confirmation_code "" "$CODE")"
  check "the code read from the e-mail formalizes (201)" "201|confirmation_code" "$STATUS|$(field .formalization.acceptance.method)"
else
  skip "the code could not be read from CloudWatch Logs (LOG_GROUP and the AWS CLI): the formalization with it was not checked"
fi

echo "== D-8: operations revokes the consent; nothing is consulted"
IDEMPOTENCY="$(printf '%04x%04x-%04x-4%03x-a%03x-%04x%04x%04x' $RANDOM $RANDOM $RANDOM $((RANDOM % 4096)) $((RANDOM % 4096)) $RANDOM $RANDOM $RANDOM)"
call POST "/v1/audit/consents/$V3_CONSENT/revocations" "$OPERATIONS_TOKEN" \
  '{"reason":"customer_request","reference":"TCK-NUBE-0001","role_confirmed":true}' "Idempotency-Key: $IDEMPOTENCY"
check "operations revokes the consent of V3 through the portal (201)" 201 "$STATUS"
call POST "$(ar "$V3")/requotes" "$ADVISOR_TOKEN" '{}'
check "the requote answers 422 consent_required" "422|consent_required" "$STATUS|$(field .error)"

echo "== Roles"
call GET "$(ar "$V4")"
check "without a token: 401 (the gateway or the service)" 401 "$STATUS"
call GET "$(ar "$V4")" "$INSURED_TOKEN"
check "the insured: 403" 403 "$STATUS"
call POST "$(ar "$V4")/requotes" "$OPERATIONS_TOKEN" '{}'
check "operations: 403" 403 "$STATUS"

echo "== Concurrency: one outcome per period"
call POST "$(ar "$V4")/requotes" "$ADVISOR_TOKEN" '{}'
R4="$(field .requote_id)"
BODY4="$(formalize_body "$R4" recorded_call GRAB-NUBE-0004)"
printf 'Authorization: Bearer %s\nContent-Type: application/json\n' "$ADVISOR_TOKEN" >"$WORK/advisor"
printf 'Authorization: Bearer %s\nContent-Type: application/json\n' "$INSURED_TOKEN" >"$WORK/insured"
curl -s --max-time 30 -o "$WORK/f1" -w '%{http_code}' -X POST -H @"$WORK/advisor" -d "$BODY4" "$PORTAL_URL$(ar "$V4")/formalization" >"$WORK/s1" &
curl -s --max-time 30 -o "$WORK/f2" -w '%{http_code}' -X POST -H @"$WORK/advisor" -d "$BODY4" "$PORTAL_URL$(ar "$V4")/formalization" >"$WORK/s2" &
curl -s --max-time 30 -o "$WORK/f3" -w '%{http_code}' -X POST -H @"$WORK/insured" -d '{}' "$PORTAL_URL/v1/policies/$V4/renewal" >"$WORK/s3" &
wait
rm -f "$WORK/advisor" "$WORK/insured"
echo "  info  advisor $(cat "$WORK/s1") and $(cat "$WORK/s2"), insured $(cat "$WORK/s3")"
call GET "$(ar "$V4")" "$ADVISOR_TOKEN"
RENEWAL4="$(field .renewal.renewal_id)" CHARGE4="$(field .renewal.charge_id)"
check "one renewal for the period" true "$([[ -n "$RENEWAL4" && "$RENEWAL4" != null ]] && echo true || echo false)"
SAME=true
for f in f1 f2 f3; do
  got="$(tr -d '\r' <"$WORK/$f" | jq -r '.renewal.charge_id // empty' 2>/dev/null || true)"
  [[ -z "$got" || "$got" == "$CHARGE4" ]] || SAME=false
done
check "every answer that carries a charge names the same one" true "$SAME"

echo
if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-W1.5-03 assisted-renewal-cloud verify: $FAILS check(s) failed, $SKIPS skipped"
  exit 1
fi
echo "== HU-W1.5-03 assisted-renewal-cloud verify: TODO OK ($SKIPS skipped)."
