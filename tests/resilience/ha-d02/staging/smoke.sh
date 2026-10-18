#!/usr/bin/env bash
# Smoke test of the HA-D02 staging window (infra/terraform/environments/staging/platform):
# the premium charge with the payment method of HU-W1.3-02 (PSE), the advisor inbox
# of HU-W1.1-02 and the contractual document of HU-W1.3-03, end to end through the
# API Gateway, with real Cognito tokens. Bash and curl only; JSON is read with sed.
#
# Phases (run them in this order; the outage ones need the gateway taken down and
# restored in between, see tests/resilience/ha-d02/README.md, "Ventana de staging"):
#   prepare         S-0   tokens of the three users; 401 without / with a tampered token;
#                         CORS preflight (only through the gateway)
#   happy           S-1…S-5 accept -> charge waiting for the payment method -> PSE ->
#                         confirmed -> policy -> document in S3 -> e-mail with the link ->
#                         PDF through the public route, with its checksum
#   inbox           S-6…S-8 advisor inbox: adjust (the adjusted offer is paid with PSE),
#                         reject and accept
#   auth            S-9   decided requests, a second decision (409), roles (403)
#   declined        S-10  PSE declined twice: payment_rejected, no policy, no real charge
#   outage-start    S-11  (payment gateway down) A: PSE, pending with growing attempts;
#                         C: no payment method
#   outage-expire   S-12  A -> retry_window_expired, C -> payment_method_missing, both in
#                         the issuance exceptions; then B1…B5 accepted with PSE, pending
#                         (prepare the restore plan before this phase)
#   outage-restore  S-13  (payment gateway restored) B1…B5 confirmed, one real charge each
#   resume          S-14  operations resumes A: confirmed with its key, policy and e-mail
#   readings        S-15  ECS, autoscaling, target groups and logs (read-only AWS calls)
#   regression      S-16  partner route without API key (403) and the health check
# Several phases can go in one call: smoke.sh prepare happy inbox auth declined
#
# State (run id and acceptance ids, never a secret) is kept in STATE_DIR between calls.
# Secrets are never printed: the password comes from the environment, request bodies go
# through stdin, the Authorization header through a private temporary file, and tokens
# and links are cut in the output.
#
# Environment:
#   API_URL           base URL: the invoke_url of the stage, or http://localhost:8190 for
#                     the local rehearsal (required unless TF_DIR is set)
#   TF_DIR            composition directory; fills API_URL, CLIENT_ID, S3_BUCKET and
#                     LOG_GROUP from its outputs (terraform output, never -json)
#   CLIENT_ID         web client of the user pool (required unless TF_DIR is set)
#   COGNITO_URL       Cognito endpoint (default: the public one of AWS_REGION, us-east-2);
#                     http://localhost:5110/ for Moto
#   SEED_USER_PASSWORD or TF_VAR_seed_user_password: password of the seed users
#   INSURED_USER, ADVISOR_USER, OPERATIONS_USER: usernames (defaults: the *.ha-d02 users
#                     of ha-d02.example.tfvars)
#   EMAIL_SOURCE      logs (CloudWatch, default) or api (GET $EMAIL_URL/messages)
#   GATEWAY_SOURCE    logs (CloudWatch, default) or api (GET $GATEWAY_URL/debug/charges)
#   EMAIL_URL, GATEWAY_URL: the stubs, for the api sources (local rehearsal)
#   LOG_GROUP         CloudWatch log group of the tasks (default /ecs/solventa-stg)
#   CLUSTER           ECS cluster for the readings (default solventa-stg)
#   S3_BUCKET         documents bucket; empty skips the S3 checks (local rehearsal)
#   WINDOW_SECONDS    retry window configured in origination (default 300)
#   STATE_DIR         where the state is kept (default ${TMPDIR:-/tmp}/ha-d02-smoke)
#   RUN_ID            suffix of the demo offers (default: the one in the state, or a new one)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
export AWS_PAGER=""
# The AWS CLI calls go through aws_ro, which keeps Git Bash from rewriting /ecs/...
# (MSYS_NO_PATHCONV only there: curl needs the conversion of its temporary files).
aws_ro() { MSYS_NO_PATHCONV=1 aws "$@"; }

if [[ -n "${TF_DIR:-}" ]]; then
  tf_raw() { terraform -chdir="$TF_DIR" output -raw "$1"; }
  API_URL="${API_URL:-$(tf_raw invoke_url)}"
  LOG_GROUP="${LOG_GROUP:-$(tf_raw log_group)}"
  CLUSTER="${CLUSTER:-$(tf_raw ecs_cluster)}"
  CLIENT_ID="${CLIENT_ID:-$(terraform -chdir="$TF_DIR" output cognito | sed -n 's/^ *"web" *= *"\([^"]*\)".*/\1/p' | head -n 1)}"
  S3_BUCKET="${S3_BUCKET-$(terraform -chdir="$TF_DIR" output buckets | sed -n 's/^ *"documents" *= *"\([^"]*\)".*/\1/p' | head -n 1)}"
fi

API_URL="${API_URL:-}"
API_URL="${API_URL%/}"
CLIENT_ID="${CLIENT_ID:-}"
REGION="${AWS_REGION:-us-east-2}"
COGNITO_URL="${COGNITO_URL:-https://cognito-idp.${REGION}.amazonaws.com/}"
PASSWORD="${SEED_USER_PASSWORD:-${TF_VAR_seed_user_password:-}}"
INSURED_USER="${INSURED_USER:-asegurado.ha-d02@example.com}"
ADVISOR_USER="${ADVISOR_USER:-asesor.ha-d02@example.com}"
OPERATIONS_USER="${OPERATIONS_USER:-operaciones.ha-d02@example.com}"
EMAIL_SOURCE="${EMAIL_SOURCE:-logs}"
GATEWAY_SOURCE="${GATEWAY_SOURCE:-logs}"
EMAIL_URL="${EMAIL_URL:-}"
GATEWAY_URL="${GATEWAY_URL:-}"
LOG_GROUP="${LOG_GROUP:-/ecs/solventa-stg}"
CLUSTER="${CLUSTER:-solventa-stg}"
S3_BUCKET="${S3_BUCKET:-}"
WINDOW_SECONDS="${WINDOW_SECONDS:-300}"
STATE_DIR="${STATE_DIR:-${TMPDIR:-/tmp}/ha-d02-smoke}"

usage() {
  sed -n '2,52p' "$0" >&2
  exit 2
}
[[ $# -gt 0 ]] || usage
[[ -n "$API_URL" && -n "$CLIENT_ID" ]] || { echo "Set API_URL and CLIENT_ID (or TF_DIR)." >&2; exit 2; }
[[ -n "$PASSWORD" ]] || { echo "Set SEED_USER_PASSWORD or TF_VAR_seed_user_password." >&2; exit 2; }
[[ "$EMAIL_SOURCE" == logs || -n "$EMAIL_URL" ]] || { echo "EMAIL_SOURCE=api needs EMAIL_URL." >&2; exit 2; }
[[ "$GATEWAY_SOURCE" == logs || -n "$GATEWAY_URL" ]] || { echo "GATEWAY_SOURCE=api needs GATEWAY_URL." >&2; exit 2; }
VIA_GATEWAY=0
[[ "$API_URL" == *execute-api* ]] && VIA_GATEWAY=1

umask 077
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$STATE_DIR"
STATE="$STATE_DIR/state"
touch "$STATE"

# --- State --------------------------------------------------------------------------

state_get() { sed -n "s/^$1=//p" "$STATE" | tail -n 1; }
state_set() { printf '%s=%s\n' "$1" "$2" >>"$STATE"; }

RUN_ID="${RUN_ID:-$(state_get RUN_ID)}"
if [[ -z "$RUN_ID" ]]; then
  RUN_ID="r$(date +%s)"
  state_set RUN_ID "$RUN_ID"
  state_set STARTED_MS "$(( ($(date +%s) - 60) * 1000 ))"
fi
STARTED_MS="$(state_get STARTED_MS)"
STARTED_MS="${STARTED_MS:-$(( ($(date +%s) - 3600) * 1000 ))}"

# --- Output -------------------------------------------------------------------------

PASSED=0
FAILED=0
ok() { printf 'OK      %-74s %s\n' "$1" "${2:-}"; PASSED=$((PASSED + 1)); }
fail() { printf 'FAIL    %-74s %s\n' "$1" "${2:-}"; FAILED=$((FAILED + 1)); }
skip() { printf 'SKIPPED %-74s %s\n' "$1" "${2:-}"; }
info() { printf 'INFO    %-74s %s\n' "$1" "${2:-}"; }
check() { # case condition-result detail
  if [[ "$2" == 0 ]]; then ok "$1" "$3"; else fail "$1" "$3"; fi
}
expect_code() { # case expected obtained [detail]
  if [[ "$3" == "$2" ]]; then ok "$1" "HTTP $3${4:+ $4}"; else fail "$1" "expected HTTP $2, got $3 $(cut -c1-160 "$TMP/body" 2>/dev/null)"; fi
}
cut_token() { printf '%s…' "${1:0:8}"; }
cut_link() { local l="$1" t="${1##*/v1/documents/}"; printf '%s/v1/documents/%s…' "${l%%/v1/documents/*}" "${t:0:8}"; }

# --- JSON (compact, as origination writes it) ----------------------------------------

# jget key [text]: first value of "key" in the text (default: the last response body);
# a string comes without its quotes; numbers, booleans and null as they are.
jget() {
  local text v
  if [[ $# -ge 2 ]]; then text="$2"; else text="$(cat "$TMP/body")"; fi
  v="$(grep -o "\"$1\":\"[^\"]*\"" <<<"$text" | head -n 1 || true)"
  if [[ -n "$v" ]]; then
    v="${v#*\":\"}"
    printf '%s' "${v%\"}"
    return 0
  fi
  v="$(grep -o "\"$1\":[^,}\"]*" <<<"$text" | head -n 1 || true)"
  printf '%s' "${v#*\":}"
}
# jobj key [text]: the flat object of "key" ({...} without nested objects).
jobj() {
  local text
  if [[ $# -ge 2 ]]; then text="$2"; else text="$(cat "$TMP/body")"; fi
  grep -o "\"$1\":{[^{}]*}" <<<"$text" | head -n 1 || true
}
# item id [text]: the element of a list that starts with "acceptance_id":"<id>".
item() {
  local text rest
  if [[ $# -ge 2 ]]; then text="$2"; else text="$(cat "$TMP/body")"; fi
  [[ "$text" == *"\"acceptance_id\":\"$1\""* ]] || return 0
  rest="${text#*\"acceptance_id\":\"$1\"}"
  printf '%s' "${rest%%\{\"acceptance_id\":*}"
}

# --- HTTP ---------------------------------------------------------------------------

# request method url token [body] [header...] -> HTTP code; body in $TMP/body,
# response headers in $TMP/headers. The token and the body never reach the process list.
request() {
  local method="$1" url="$2" token="$3" data="${4:-}" h
  shift 4 || shift $#
  : >"$TMP/header"
  [[ -n "$token" ]] && printf 'Authorization: Bearer %s\n' "$token" >>"$TMP/header"
  for h in "$@"; do printf '%s\n' "$h" >>"$TMP/header"; done
  # On a network error curl writes 000 as the code.
  if [[ -n "$data" ]]; then
    printf 'Content-Type: application/json\n' >>"$TMP/header"
    printf '%s' "$data" | curl -sS -X "$method" -D "$TMP/headers" -o "$TMP/body" -w '%{http_code}' \
      -H @"$TMP/header" --data-binary @- "$url" || true
  else
    curl -sS -X "$method" -D "$TMP/headers" -o "$TMP/body" -w '%{http_code}' -H @"$TMP/header" "$url" || true
  fi
}
header() { sed -n "s/^$1: *//Ip" "$TMP/headers" | tr -d '\r' | tail -n 1; }
json_escape() { local s=${1//\\/\\\\}; printf '%s' "${s//\"/\\\"}"; }

# --- Cognito tokens (renewed every 4 minutes: they last 5) ----------------------------

# token role -> ID token. Kept in private files of $TMP (not variables), because it is
# mostly called inside $(...), where a variable would be lost.
token() {
  local role="$1" user code now file="$TMP/token-$1"
  now="$(date +%s)"
  if [[ -s "$file" && $((now - $(cat "$file.at"))) -lt 240 ]]; then
    cat "$file"
    return 0
  fi
  case "$role" in
    insured) user="$INSURED_USER" ;;
    advisor) user="$ADVISOR_USER" ;;
    operations) user="$OPERATIONS_USER" ;;
  esac
  code="$(printf '{"AuthFlow":"USER_PASSWORD_AUTH","ClientId":"%s","AuthParameters":{"USERNAME":"%s","PASSWORD":"%s"}}' \
    "$CLIENT_ID" "$user" "$(json_escape "$PASSWORD")" |
    curl -sS -o "$TMP/auth" -w '%{http_code}' -X POST "$COGNITO_URL" \
      -H 'Content-Type: application/x-amz-json-1.1' \
      -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' --data-binary @- || true)"
  if [[ "$code" != 200 ]]; then
    echo "Sign-in of $user failed: HTTP $code. Check the seed users, the client and the password." >&2
    rm -f "$TMP/auth"
    return 1
  fi
  grep -o '"IdToken": *"[^"]*"' "$TMP/auth" | sed 's/.*"\([^"]*\)"$/\1/' | tr -d '\n' >"$file"
  printf '%s' "$now" >"$file.at"
  rm -f "$TMP/auth"
  cat "$file"
}

# Changes one character in the middle of the signature of a JWT.
tamper() {
  local header payload signature i c
  IFS=. read -r header payload signature <<<"$1"
  i=$((${#signature} / 2))
  c="${signature:i:1}"
  [[ "$c" == A ]] && c=B || c=A
  printf '%s.%s.%s' "$header" "$payload" "${signature:0:i}${c}${signature:i+1}"
}

# --- Origination --------------------------------------------------------------------

PSE='{"method":"pse","bank_code":"1007"}'

accept() { # offer_id -> sets ACC (the acceptance id); fails the case otherwise
  local code
  ACC=""
  code="$(request POST "$API_URL/v1/offers/$1/acceptance" "$(token insured)" '{"channel":"web"}')"
  if [[ "$code" == 202 && "$(jget status)" == registered ]]; then
    ACC="$(jget acceptance_id)"
  else
    fail "Accept $1 -> 202 registered" "HTTP $code $(cut -c1-160 "$TMP/body")"
    return 1
  fi
}
charge() { request GET "$API_URL/v1/acceptances/$1/charge" "$(token insured)"; } # acceptance -> code
acceptance() { request GET "$API_URL/v1/acceptances/$1" "$(token insured)"; }
pay() { request POST "$API_URL/v1/acceptances/$1/charge/payment-method" "$(token "${2:-insured}")" "${3:-$PSE}"; }

# wait_charge acceptance timeout condition: polls the charge until the condition
# (a function over the body) holds; the last body stays in $TMP/body.
wait_charge() {
  local id="$1" deadline=$(( $(date +%s) + $2 )) cond="$3" code
  while :; do
    code="$(charge "$id")"
    if [[ "$code" == 200 ]] && "$cond"; then return 0; fi
    (( $(date +%s) >= deadline )) && return 1
    sleep 2
  done
}
exists() { true; }
is_pending() { [[ "$(jget status)" == pending ]]; }
is_confirmed() { [[ "$(jget status)" == confirmed && "$(jget policy_id)" != null && -n "$(jget policy_id)" ]]; }
is_failed() { [[ "$(jget status)" == failed ]]; }
retried_twice() { [[ "$(jget status)" == pending && "$(jget attempts)" -ge 2 ]]; }

wait_acceptance() { # acceptance status timeout
  local deadline=$(( $(date +%s) + $3 )) code
  while :; do
    code="$(acceptance "$1")"
    [[ "$code" == 200 && "$(jget status)" == "$2" ]] && return 0
    (( $(date +%s) >= deadline )) && return 1
    sleep 2
  done
}

wait_issuance() { # policy timeout -> 0 when completed; body in $TMP/body
  local deadline=$(( $(date +%s) + $2 )) code
  while :; do
    code="$(request GET "$API_URL/v1/policies/$1/issuance-status" "$(token insured)")"
    [[ "$code" == 200 && "$(jget status)" == completed ]] && return 0
    (( $(date +%s) >= deadline )) && return 1
    sleep 3
  done
}

# --- Evidence of the stubs (CloudWatch Logs or their APIs) ---------------------------

logs_of() { # stream-prefix filter-pattern -> messages, one per line
  aws_ro logs filter-log-events --region "$REGION" --log-group-name "$LOG_GROUP" --log-stream-name-prefix "$1" \
    --start-time "$STARTED_MS" --filter-pattern "$2" --query 'events[].message' --output text 2>/dev/null |
    tr '\t' '\n' | tr -d '\r'
}

# real_charges key -> how many times the gateway really charged the key.
real_charges() {
  local key="$1"
  if [[ "$GATEWAY_SOURCE" == api ]]; then
    curl -sS "$GATEWAY_URL/debug/charges" -o "$TMP/gw" || { echo 0; return; }
    local v
    v="$(grep -o '"real_charges":{[^}]*}' "$TMP/gw" | grep -o "\"$key\":[0-9]*" || true)"
    echo "${v##*:}" | sed 's/^$/0/'
  else
    logs_of payment-gateway '"charge request"' | grep -c "key=$key .*new_charge=true" || true
  fi
}

# wait_real_charges key expected timeout: CloudWatch takes a few seconds to show a line.
wait_real_charges() {
  local deadline=$(( $(date +%s) + $3 )) n
  while :; do
    n="$(real_charges "$1")"
    [[ "$n" == "$2" ]] && { echo "$n"; return 0; }
    (( $(date +%s) >= deadline )) && { echo "$n"; return 1; }
    sleep 5
  done
}

# mail_with to subject marker -> the message (body included) that contains the marker.
mail_with() {
  local to="$1" subject="$2" marker="$3"
  if [[ "$EMAIL_SOURCE" == api ]]; then
    curl -sS "$EMAIL_URL/messages?to=$to" -o "$TMP/mail" || return 0
    # One message per line, then the one with the subject and the marker.
    sed 's/},{"id"/}\n{"id"/g' "$TMP/mail" | grep -F "\"subject\":\"$subject\"" | grep -F "$marker" | head -n 1 || true
  else
    logs_of email '"message accepted"' | grep -F "to=$to " | grep -F "subject=\"$subject\"" | grep -F "$marker" | head -n 1 || true
  fi
}
wait_mail() { # to subject marker timeout -> message
  local deadline=$(( $(date +%s) + $4 )) m
  while :; do
    m="$(mail_with "$1" "$2" "$3")"
    [[ -n "$m" ]] && { printf '%s' "$m"; return 0; }
    (( $(date +%s) >= deadline )) && return 1
    sleep 5
  done
}
link_in() { grep -o 'https\?://[^ "\\]*/v1/documents/[A-Za-z0-9._~-]*' <<<"$1" | head -n 1 || true; }

sha_of() { sha256sum "$1" | cut -d' ' -f1; }

# =====================================================================================
# Phases
# =====================================================================================

phase_prepare() { # S-0
  echo "== S-0 prepare (run $RUN_ID, API $API_URL, via gateway: $VIA_GATEWAY)"
  local t
  for role in insured advisor operations; do
    t="$(token "$role")"
    check "Sign-in of the $role seed user (Cognito InitiateAuth)" "$([[ -n "$t" ]]; echo $?)" "ID token $(cut_token "$t")"
  done
  expect_code "Inbox without a token -> 401" 401 "$(request GET "$API_URL/v1/review-requests" "")"
  expect_code "Inbox with a tampered token -> 401 (#33)" 401 "$(request GET "$API_URL/v1/review-requests" "$(tamper "$(token advisor)")")"
  if [[ "$VIA_GATEWAY" == 1 ]]; then
    local code
    code="$(request OPTIONS "$API_URL/v1/review-requests" "" "" "Origin: https://portal.solventa.example" "Access-Control-Request-Method: GET")"
    if [[ "$code" == 200 && -n "$(header Access-Control-Allow-Origin)" ]]; then
      ok "CORS preflight with Origin -> 200 with CORS headers (MOCK with */* binary, D-4)" "Allow-Methods: $(header Access-Control-Allow-Methods)"
    else
      fail "CORS preflight with Origin -> 200 with CORS headers (MOCK with */* binary, D-4)" "HTTP $code, Allow-Origin '$(header Access-Control-Allow-Origin)'"
    fi
  else
    skip "CORS preflight with Origin" "only through the API Gateway"
  fi
}

# Pays an acceptance with PSE once its charge exists and waits for the policy.
# Sets PAID_POLICY. Used by the happy path and the inbox.
pay_and_confirm() { # case-prefix acceptance [expected amount]
  local label="$1" id="$2" amount="${3:-}" code
  if ! wait_charge "$id" 90 exists; then
    fail "$label: the charge exists after the underwriting" "HTTP $(charge "$id")"
    return 1
  fi
  code="$(pay "$id")"
  expect_code "$label: PSE handed over -> 202" 202 "$code" "payment_method $(jobj payment_method | grep -o '"method":"[^"]*"')"
  if wait_charge "$id" 120 is_confirmed; then
    ok "$label: charge confirmed with its policy" "key $(jget idempotency_key), amount $(jget amount), policy $(jget policy_id)"
    PAID_POLICY="$(jget policy_id)"
    if [[ -n "$amount" ]]; then
      check "$label: the amount charged is $amount" "$([[ "$(jget amount)" == "$amount" ]]; echo $?)" "amount $(jget amount)"
    fi
  else
    fail "$label: charge confirmed with its policy" "status $(jget status), failure $(jget failure_reason)"
    PAID_POLICY=""
    return 1
  fi
}

phase_happy() { # S-1 … S-5
  echo "== S-1 happy"
  local id code seen404=0 key policy deadline
  accept "demo-active-$RUN_ID" || return 0
  id="$ACC"
  ok "Accept demo-active-$RUN_ID -> 202 registered" "acceptance $id"
  state_set HAPPY "$id"
  deadline=$(( $(date +%s) + 90 ))
  while :; do
    code="$(charge "$id")"
    [[ "$code" == 404 ]] && seen404=$((seen404 + 1))
    [[ "$code" == 200 ]] && break
    (( $(date +%s) >= deadline )) && break
    sleep 1
  done
  info "Charge before the underwriting approved" "$seen404 answers 404 (charge_not_found) before it existed"
  if [[ "$code" == 200 && "$(jget status)" == pending && "$(jget payment_method)" == null && "$(jget payment_attempt)" == 0 ]]; then
    ok "Charge waits for the payment method (E4): pending, payment_method null, payment_attempt 0" "next_attempt_at $(jget next_attempt_at)"
  else
    fail "Charge waits for the payment method (E4): pending, payment_method null, payment_attempt 0" "HTTP $code $(cut -c1-200 "$TMP/body")"
  fi
  expect_code "PSE without bank_code -> 400" 400 "$(pay "$id" insured '{"method":"pse"}')"
  expect_code "Card with a 16-digit number as token -> 400 (PCI guard)" 400 \
    "$(pay "$id" insured '{"method":"card","payment_token":"4111111111111111","brand":"visa","last4":"1111","installments":1}')"
  expect_code "PSE with the advisor token -> 403" 403 "$(pay "$id" advisor)"
  code="$(pay "$id")"
  if [[ "$code" == 202 && "$(jobj payment_method)" == *'"method":"pse"'* && "$(jget next_attempt_at)" != null ]]; then
    ok "PSE handed over -> 202, payment_method pse, next attempt due" "next_attempt_at $(jget next_attempt_at)"
  else
    fail "PSE handed over -> 202, payment_method pse, next attempt due" "HTTP $code $(cut -c1-200 "$TMP/body")"
  fi
  if wait_charge "$id" 120 is_confirmed; then
    key="$(jget idempotency_key)"
    policy="$(jget policy_id)"
    check "Charge confirmed with its policy and the key cobro-<acceptance> (CA-1, CA-4)" \
      "$([[ "$key" == "cobro-$id" ]]; echo $?)" "key $key, policy $policy, coverage activated $(jget coverage_activated_at)"
  else
    fail "Charge confirmed with its policy (CA-1, CA-4)" "status $(jget status), failure $(jget failure_reason)"
    return 0
  fi
  state_set HAPPY_POLICY "$policy"
  local n
  n="$(wait_real_charges "cobro-$id" 1 90)" && ok "One real charge at the gateway for cobro-$id ($GATEWAY_SOURCE)" "new_charge=true × $n" ||
    fail "One real charge at the gateway for cobro-$id ($GATEWAY_SOURCE)" "new_charge=true × $n"
  expect_code "PSE after the charge was confirmed -> 409" 409 "$(pay "$id")" "$(jget error)"

  echo "== S-2 issuance"
  if wait_issuance "$policy" 120; then
    local doc notif
    doc="$(jobj document)"
    notif="$(jobj notification)"
    check "Issuance completed, notification sent, document within 60 s (HU-W1.3-03 CA-1)" \
      "$([[ "$(jget status "$notif")" == sent && "$(jget within_slo)" == true ]]; echo $?)" \
      "activated $(jget activated_at), available $(jget available_at), within_slo $(jget within_slo)"
    state_set HAPPY_SHA "$(jget checksum_sha256 "$doc")"
    state_set HAPPY_SIZE "$(jget size_bytes "$doc")"
  else
    fail "Issuance completed, notification sent (HU-W1.3-03)" "$(cut -c1-200 "$TMP/body")"
    return 0
  fi
  local sha size
  sha="$(state_get HAPPY_SHA)"
  size="$(state_get HAPPY_SIZE)"

  echo "== S-3 document in S3"
  if [[ -n "$S3_BUCKET" ]]; then
    local head versions
    head="$(aws_ro s3api head-object --region "$REGION" --bucket "$S3_BUCKET" --key "policies/$policy/contract-v1.pdf" \
      --query '[ContentType,ServerSideEncryption,ContentLength]' --output text 2>&1 | tr -d '\r' || true)"
    check "S3 object policies/$policy/contract-v1.pdf: application/pdf, AES256, $size bytes" \
      "$([[ "$head" == "application/pdf"*"AES256"*"$size" ]]; echo $?)" "$head"
    versions="$(aws_ro s3api list-object-versions --region "$REGION" --bucket "$S3_BUCKET" --prefix "policies/$policy/contract-v1.pdf" \
      --query 'length(Versions)' --output text 2>&1 | tr -d '\r' || true)"
    check "A single version of the document (never overwritten)" "$([[ "$versions" == 1 ]]; echo $?)" "versions $versions"
  else
    skip "S3 object of the document" "S3_BUCKET is empty (local rehearsal: the document directory)"
  fi

  echo "== S-4 e-mail"
  local mail link
  if mail="$(wait_mail "$INSURED_USER" "Tu póliza de Solventa fue emitida" "$policy" 120)"; then
    link="$(link_in "$mail")"
    check "E-mail to the insured with the signed link of the document ($EMAIL_SOURCE)" \
      "$([[ "$link" == "$API_URL/v1/documents/"* ]]; echo $?)" "link $(cut_link "$link")"
  else
    fail "E-mail to the insured with the signed link of the document ($EMAIL_SOURCE)" "no message for $policy"
    return 0
  fi

  echo "== S-5 public link"
  local accept_header got
  for accept_header in 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8' '*/*' 'application/json, text/plain, */*'; do
    code="$(request GET "$link" "" "" "Accept: $accept_header")"
    got="$(sha_of "$TMP/body")"
    if [[ "$code" == 200 && "$(header Content-Type)" == application/pdf* && "$got" == "$sha" && "$(header X-Document-Checksum-SHA256)" == "$sha" ]]; then
      ok "Link without session, Accept: ${accept_header:0:24}… -> PDF intact" "SHA-256 ${sha:0:12}… = checksum = header"
    else
      fail "Link without session, Accept: ${accept_header:0:24}… -> PDF intact" "HTTP $code, $(header Content-Type), sha ${got:0:12}… vs ${sha:0:12}…"
    fi
  done
  code="$(request GET "$(tamper_link "$link")" "")"
  check "Tampered link -> 403 invalid_link" "$([[ "$code" == 403 && "$(jget error)" == invalid_link ]]; echo $?)" "HTTP $code $(jget error)"
  code="$(request GET "$API_URL/v1/policies/$policy/contract-document" "$(token insured)")"
  check "Contract document with the insured token -> the same PDF" "$([[ "$code" == 200 && "$(sha_of "$TMP/body")" == "$sha" ]]; echo $?)" "HTTP $code"
}

tamper_link() { # changes one character of the signature of the token in the link
  local base="${1%/*}" tok="${1##*/}" payload mac i c
  payload="${tok%%.*}"
  mac="${tok#*.}"
  i=$((${#mac} / 2))
  c="${mac:i:1}"
  [[ "$c" == A ]] && c=B || c=A
  printf '%s/%s.%s' "$base" "$payload" "${mac:0:i}${c}${mac:i+1}"
}

# Waits until the request reaches the inbox; prints it from the pending list.
inbox_item() { # acceptance
  local deadline=$(( $(date +%s) + 60 )) it
  while :; do
    request GET "$API_URL/v1/review-requests?status=pending" "$(token advisor)" >/dev/null
    it="$(item "$1")"
    [[ -n "$it" ]] && { printf '%s' "$it"; return 0; }
    (( $(date +%s) >= deadline )) && return 1
    sleep 2
  done
}

decide() { # acceptance body -> code
  request POST "$API_URL/v1/review-requests/$1/decision" "$(token advisor)" "$2"
}

phase_inbox() { # S-6 … S-8
  echo "== S-6 inbox: adjust"
  local id it code adj premium adj_acc
  accept "demo-review-$RUN_ID" || return 0
  id="$ACC"
  state_set REVIEW_ADJUST "$id"
  if wait_acceptance "$id" in_review 60; then
    ok "Provisional offer routed to the advisor -> in_review" "reason $(jget reason "$(jobj underwriting)")"
  else
    fail "Provisional offer routed to the advisor -> in_review" "status $(jget status)"
    return 0
  fi
  expect_code "No charge while in review" 404 "$(charge "$id")"
  code="$(pay "$id")"
  check "PSE while in review -> 404 charge_not_found" "$([[ "$code" == 404 && "$(jget error)" == charge_not_found ]]; echo $?)" "HTTP $code $(jget error)"
  if it="$(inbox_item "$id")"; then
    check "The advisor sees the request: applicant, offer, reason, arrival and status (CA-1)" \
      "$([[ "$(jget reason "$it")" == provisional_offer && "$(jget status "$it")" == pending && -n "$(jget full_name "$it")" && -n "$(jget received_at "$it")" && -n "$(jget premium "$it")" ]]; echo $?)" \
      "reason $(jget reason "$it"), premium $(jget premium "$it"), received $(jget received_at "$it")"
  else
    fail "The advisor sees the request in the inbox (CA-1)" "not listed"
    return 0
  fi
  expect_code "The advisor opens the request" 200 "$(request GET "$API_URL/v1/review-requests/$id" "$(token advisor)")"
  code="$(decide "$id" '{"type":"adjust","premium_adjustment_pct":20,"observations":"Ajuste de prima por perfil provisional (humo de staging HA-D02)"}')"
  expect_code "Adjust +20 % with observations -> 201 (CA-2)" 201 "$code"
  if wait_acceptance "$id" adjusted 30; then
    adj="$(jobj adjusted_offer)"
    premium="$(jget premium "$adj")"
    check "The insured sees adjusted and the new offer adj-<id> (E6b)" "$([[ "$(jget offer_id "$adj")" == "adj-$id" ]]; echo $?)" "offer $(jget offer_id "$adj"), premium $premium"
  else
    fail "The insured sees adjusted (E6b)" "status $(jget status)"
    return 0
  fi
  if wait_mail "$INSURED_USER" "Revisamos tu solicitud de seguro: tienes una nueva oferta" "$id" 120 >/dev/null; then
    ok "E-mail of the adjusted offer to the insured" "$EMAIL_SOURCE"
  else
    fail "E-mail of the adjusted offer to the insured" "no message"
  fi
  accept "adj-$id" || return 0
  adj_acc="$ACC"
  ok "The insured accepts adj-$id -> 202" "acceptance $adj_acc"
  state_set REVIEW_ADJUSTED_ACCEPTANCE "$adj_acc"
  pay_and_confirm "Adjusted offer" "$adj_acc" "$premium" || true

  echo "== S-7 inbox: reject"
  accept "demo-highrisk-$RUN_ID" || return 0
  id="$ACC"
  state_set REVIEW_REJECT "$id"
  if wait_acceptance "$id" in_review 60 && [[ "$(jget reason "$(jobj underwriting)")" == high_risk_score ]]; then
    ok "High risk score routed to the advisor" "reason high_risk_score"
  else
    fail "High risk score routed to the advisor" "status $(jget status), reason $(jget reason "$(jobj underwriting)")"
  fi
  inbox_item "$id" >/dev/null || true
  expect_code "Reject with observations -> 201" 201 "$(decide "$id" '{"type":"reject","observations":"Riesgo fuera de la politica de suscripcion (humo de staging HA-D02)"}')"
  if wait_acceptance "$id" rejected 30; then ok "The insured sees rejected (E6c)" ""; else fail "The insured sees rejected (E6c)" "status $(jget status)"; fi
  if wait_mail "$INSURED_USER" "Tu solicitud de seguro no fue aprobada" "$id" 120 >/dev/null; then
    ok "E-mail of the rejection to the insured" "$EMAIL_SOURCE"
  else
    fail "E-mail of the rejection to the insured" "no message"
  fi
  expect_code "No charge for a rejected request" 404 "$(charge "$id")"

  echo "== S-8 inbox: accept"
  accept "demo-review-${RUN_ID}b" || return 0
  id="$ACC"
  state_set REVIEW_ACCEPT "$id"
  wait_acceptance "$id" in_review 60 || fail "Second provisional offer routed to the advisor" "status $(jget status)"
  inbox_item "$id" >/dev/null || true
  expect_code "Accept the risk -> 201" 201 "$(decide "$id" '{"type":"accept"}')"
  pay_and_confirm "Accepted by the advisor" "$id" || true
}

phase_auth() { # S-9
  echo "== S-9 decisions and roles"
  local code it id type key
  code="$(request GET "$API_URL/v1/review-requests?status=decided" "$(token advisor)")"
  expect_code "Decided requests" 200 "$code"
  for pair in "REVIEW_ADJUST adjust" "REVIEW_REJECT reject" "REVIEW_ACCEPT accept"; do
    read -r key type <<<"$pair"
    id="$(state_get "$key")"
    it="$(item "$id")"
    check "Decided $type listed with advisor, date, type and observations (CA-4)" \
      "$([[ -n "$it" && "$(jget type "$it")" == "$type" && "$(jget email "$(jobj advisor "$it")")" == "$ADVISOR_USER" && -n "$(jget decided_at "$it")" ]]; echo $?)" \
      "acceptance $id, decided $(jget decided_at "$it")"
  done
  code="$(decide "$(state_get REVIEW_ADJUST)" '{"type":"accept"}')"
  check "A second decision -> 409 review_request_already_decided" "$([[ "$code" == 409 ]]; echo $?)" "HTTP $code $(jget error)"
  expect_code "The insured in the inbox -> 403" 403 "$(request GET "$API_URL/v1/review-requests" "$(token insured)")"
  expect_code "The advisor in the issuance exceptions -> 403" 403 "$(request GET "$API_URL/v1/issuance-exceptions" "$(token advisor)")"
}

phase_declined() { # S-10
  echo "== S-10 declined payment"
  local id code
  accept "demo-active-declined$RUN_ID" || return 0
  id="$ACC"
  state_set DECLINED "$id"
  wait_charge "$id" 90 exists || { fail "Charge of the declined offer exists" "HTTP $(charge "$id")"; return 0; }
  expect_code "PSE handed over -> 202" 202 "$(pay "$id")"
  if wait_charge "$id" 120 is_failed && [[ "$(jget failure_reason)" == payment_rejected ]]; then
    ok "Declined by the gateway -> failed payment_rejected" "key $(jget idempotency_key)"
  else
    fail "Declined by the gateway -> failed payment_rejected" "status $(jget status), failure $(jget failure_reason)"
  fi
  expect_code "No policy without a confirmed payment (CA-4)" 404 "$(request GET "$API_URL/v1/policies/pol-$id/issuance-status" "$(token insured)")"
  check "No real charge at the gateway for cobro-$id" "$([[ "$(real_charges "cobro-$id")" == 0 ]]; echo $?)" "new_charge=true × $(real_charges "cobro-$id")"
  code="$(pay "$id")"
  check "Second PSE -> 202, payment_attempt 2, key cobro-<id>-2" \
    "$([[ "$code" == 202 && "$(jget payment_attempt)" == 2 && "$(jget idempotency_key)" == "cobro-$id-2" ]]; echo $?)" \
    "HTTP $code, attempt $(jget payment_attempt), key $(jget idempotency_key)"
  if wait_charge "$id" 120 is_failed && [[ "$(jget failure_reason)" == payment_rejected ]]; then
    ok "Declined again (the stub decides by the reference) -> payment_rejected" "attempt $(jget payment_attempt)"
  else
    fail "Declined again -> payment_rejected" "status $(jget status), failure $(jget failure_reason)"
  fi
  check "No real charge for cobro-$id-2 either" "$([[ "$(real_charges "cobro-$id-2")" == 0 ]]; echo $?)" ""
}

phase_outage_start() { # S-11 (the payment gateway is down)
  echo "== S-11 outage: A with PSE, C without a payment method"
  local a c first
  accept "demo-active-outA$RUN_ID" || return 0
  a="$ACC"
  accept "demo-active-outC$RUN_ID" || return 0
  c="$ACC"
  state_set OUTAGE_A "$a"
  state_set OUTAGE_C "$c"
  wait_charge "$a" 90 exists || fail "Charge of A exists" ""
  expect_code "A: PSE handed over while the gateway is down -> 202" 202 "$(pay "$a")"
  if wait_charge "$a" 120 retried_twice; then
    first="$(jget next_attempt_at)"
    ok "A: pending with failed attempts, the same key and the next one due (E4b, CA-2)" \
      "attempts $(jget attempts), key $(jget idempotency_key), next $first, window ends $(jget ends_at "$(jobj retry_window)")"
    state_set OUTAGE_A_ENDS "$(jget ends_at "$(jobj retry_window)")"
  else
    fail "A: pending with failed attempts (CA-2)" "status $(jget status), attempts $(jget attempts)"
  fi
  if wait_charge "$c" 90 exists && is_pending && [[ "$(jget payment_method)" == null ]]; then
    ok "C: pending without a payment method (not handed over on purpose)" "window ends $(jget ends_at "$(jobj retry_window)")"
  else
    fail "C: pending without a payment method" "status $(jget status), payment_method $(jget payment_method)"
  fi
}

in_exceptions() { # acceptance reason -> 0 if listed with that reason (body: the list)
  local it
  it="$(item "$1")"
  [[ -n "$it" && "$(jget reason "$it")" == "$2" ]]
}

phase_outage_expire() { # S-12
  echo "== S-12 outage: the windows of A and C expire"
  local a c i id timeout code
  a="$(state_get OUTAGE_A)"
  c="$(state_get OUTAGE_C)"
  timeout=$((WINDOW_SECONDS + 120))
  if wait_charge "$a" "$timeout" is_failed && [[ "$(jget failure_reason)" == retry_window_expired && "$(jget payment_method)" != null ]]; then
    ok "A: failed retry_window_expired, payment method kept (CA-3)" "attempts $(jget attempts), key $(jget idempotency_key)"
  else
    fail "A: failed retry_window_expired (CA-3)" "status $(jget status), failure $(jget failure_reason)"
  fi
  if wait_charge "$c" "$timeout" is_failed && [[ "$(jget failure_reason)" == payment_method_missing && "$(jget payment_method)" == null ]]; then
    ok "C: failed payment_method_missing, payment_method null (HU-W1.3-02 §5)" ""
  else
    fail "C: failed payment_method_missing" "status $(jget status), failure $(jget failure_reason)"
  fi
  code="$(request GET "$API_URL/v1/issuance-exceptions" "$(token operations)")"
  check "Operations sees A (retry_window_expired) in the issuance exceptions" "$([[ "$code" == 200 ]] && in_exceptions "$a" retry_window_expired; echo $?)" "HTTP $code"
  request GET "$API_URL/v1/issuance-exceptions" "$(token operations)" >/dev/null
  check "Operations sees C (payment_method_missing) in the issuance exceptions" "$(in_exceptions "$c" payment_method_missing; echo $?)" ""
  for id in "$a" "$c"; do
    code="$(pay "$id")"
    check "PSE to an expired charge -> 409 charge_not_payable" "$([[ "$code" == 409 && "$(jget error)" == charge_not_payable ]]; echo $?)" "acceptance $id: HTTP $code $(jget error)"
    expect_code "No policy for $id" 404 "$(request GET "$API_URL/v1/policies/pol-$id/issuance-status" "$(token insured)")"
  done

  echo "== S-12 outage: B1…B5 accepted with PSE while the gateway is still down"
  for i in 1 2 3 4 5; do
    accept "demo-active-outB$i$RUN_ID" || continue
    id="$ACC"
    state_set "OUTAGE_B$i" "$id"
    wait_charge "$id" 90 exists || { fail "B$i: charge exists" ""; continue; }
    code="$(pay "$id")"
    check "B$i: PSE -> 202, pending" "$([[ "$code" == 202 && "$(jget status)" == pending ]]; echo $?)" "acceptance $id, window ends $(jget ends_at "$(jobj retry_window)")"
  done
}

phase_outage_restore() { # S-13 (the payment gateway is back)
  echo "== S-13 outage: restored, B1…B5 confirmed once each (CA-5)"
  local i id n total=0
  for i in 1 2 3 4 5; do
    id="$(state_get "OUTAGE_B$i")"
    [[ -n "$id" ]] || { fail "B$i: acceptance in the state" "missing"; continue; }
    if wait_charge "$id" $((WINDOW_SECONDS + 60)) is_confirmed; then
      n="$(wait_real_charges "cobro-$id" 1 120)" || true
      total=$((total + n))
      check "B$i: confirmed by itself, exactly one real charge" "$([[ "$n" == 1 ]]; echo $?)" "key cobro-$id, new_charge=true × $n, attempts before $(jget attempts)"
    else
      fail "B$i: confirmed by itself" "status $(jget status), failure $(jget failure_reason)"
    fi
  done
  check "Five acceptances during the outage, five real charges: none lost, none duplicated" "$([[ "$total" == 5 ]]; echo $?)" "total $total"
}

phase_resume() { # S-14
  echo "== S-14 resume by operations"
  local a c code
  a="$(state_get OUTAGE_A)"
  c="$(state_get OUTAGE_C)"
  expect_code "The insured cannot resume -> 403" 403 "$(request POST "$API_URL/v1/acceptances/$a/charge/resume" "$(token insured)" '{"note":"intento del asegurado"}')"
  code="$(request POST "$API_URL/v1/acceptances/$a/charge/resume" "$(token operations)" '{"note":"Reanudado por operaciones tras restablecer la pasarela (humo HA-D02)"}')"
  check "Operations resumes A with a note -> 202 pending, PSE kept" \
    "$([[ "$code" == 202 && "$(jobj payment_method)" == *'"method":"pse"'* ]]; echo $?)" "HTTP $code, status $(jget status), resume_count $(jget resume_count)"
  code="$(request POST "$API_URL/v1/acceptances/$a/charge/resume" "$(token operations)" '{"note":"segunda"}')"
  check "A second resumption -> 409" "$([[ "$code" == 409 ]]; echo $?)" "HTTP $code $(jget error)"
  if wait_charge "$a" 120 is_confirmed && [[ "$(jget idempotency_key)" == "cobro-$a" ]]; then
    ok "A: confirmed with its own key cobro-<A> (CA-3)" "policy $(jget policy_id)"
    local policy n
    policy="$(jget policy_id)"
    n="$(wait_real_charges "cobro-$a" 1 120)" || true
    check "A: exactly one real charge" "$([[ "$n" == 1 ]]; echo $?)" "new_charge=true × $n"
    if wait_issuance "$policy" 120; then ok "A: issuance completed (document and e-mail)" "within_slo $(jget within_slo)"; else fail "A: issuance completed" "$(jget status)"; fi
    if wait_mail "$INSURED_USER" "Tu póliza de Solventa fue emitida" "$policy" 120 >/dev/null; then ok "A: e-mail with the link" ""; else fail "A: e-mail with the link" "no message"; fi
  else
    fail "A: confirmed with its own key (CA-3)" "status $(jget status), failure $(jget failure_reason)"
  fi
  info "C (payment_method_missing) is not resumed on purpose" "H-1: resuming it would charge without a payment method (HU-W1.3-02, Carlos)"
  : "$c"
}

phase_readings() { # S-15 (read-only AWS calls)
  echo "== S-15 readings"
  if [[ "$VIA_GATEWAY" != 1 ]]; then
    skip "ECS, autoscaling, target groups and logs" "only in the staging window"
    return 0
  fi
  local out
  out="$(aws_ro ecs list-services --region "$REGION" --cluster "$CLUSTER" --query 'serviceArns' --output text 2>&1 | tr '\t' '\n' | tr -d '\r' | grep -c arn || true)"
  check "ECS services of the cluster" "$([[ "$out" == 10 ]]; echo $?)" "$out services"
  out="$(aws_ro ecs describe-services --region "$REGION" --cluster "$CLUSTER" \
    --services origination claims partners audit notifications openfinance opendata kyc email payment-gateway \
    --query 'services[].[serviceName,desiredCount,runningCount,length(deployments)]' --output text 2>&1 | tr -d '\r' || true)"
  check "Every service runs its desired tasks with one deployment" \
    "$(awk '$2!=$3 || $4!=1 {bad=1} END {exit bad}' <<<"$out"; echo $?)" "$(tr '\n' ';' <<<"$out")"
  check "Origination runs exactly one task (C5)" "$(grep -qE '^origination[[:space:]]+1[[:space:]]+1[[:space:]]' <<<"$out"; echo $?)" ""
  out="$(aws_ro application-autoscaling describe-scalable-targets --region "$REGION" --service-namespace ecs \
    --query "ScalableTargets[?contains(ResourceId,'/$CLUSTER/')].ResourceId" --output text 2>&1 | tr -d '\r' || true)"
  check "No autoscaling target for origination" "$([[ "$out" != *origination* ]]; echo $?)" "$(tr '\t' ' ' <<<"$out")"
  out="$(aws_ro elbv2 describe-target-groups --region "$REGION" --query "TargetGroups[?starts_with(TargetGroupName,'$CLUSTER')].TargetGroupArn" --output text 2>&1 | tr -d '\r' || true)"
  local tg states healthy=0 groups=0
  for tg in $out; do
    groups=$((groups + 1))
    states="$(aws_ro elbv2 describe-target-health --region "$REGION" --target-group-arn "$tg" \
      --query 'TargetHealthDescriptions[].TargetHealth.State' --output text 2>/dev/null | tr '\t' '\n' | tr -d '\r' || true)"
    # Every target healthy ("unhealthy" also contains the word, so compare whole lines).
    if [[ -n "$states" ]] && ! grep -qvx healthy <<<"$states"; then
      healthy=$((healthy + 1))
    fi
  done
  check "Target groups with a healthy target" "$([[ "$groups" -gt 0 && "$healthy" == "$groups" ]]; echo $?)" "$healthy of $groups"
  for pattern in '"premium charge: PostgreSQL migrated"' '"PDF in s3://"' '"AUDIT_URL is not set"'; do
    out="$(logs_of origination "$pattern" | head -n 1)"
    check "Origination log: $pattern" "$([[ -n "$out" ]]; echo $?)" "$(cut -c1-120 <<<"$out")"
  done
  out="$(logs_of audit '"append-only records in PostgreSQL"' | head -n 1)"
  check "Audit started with its database" "$([[ -n "$out" ]]; echo $?)" "$(cut -c1-120 <<<"$out")"
}

phase_regression() { # S-16
  echo "== S-16 regression"
  if [[ "$VIA_GATEWAY" == 1 ]]; then
    local code
    code="$(request GET "$API_URL/v1/risk-profiles/cust-smoke?consent_active=true" "")"
    expect_code "Partner route without an API key -> 403 (HA-L01)" 403 "$code" "$(cut -c1-80 "$TMP/body")"
    skip "GET /health" "not published in the gateway: the health of the targets is read in S-15"
  else
    skip "Partner route without an API key" "only through the API Gateway"
    expect_code "GET /health" 200 "$(request GET "$API_URL/health" "")"
  fi
}

for phase in "$@"; do
  case "$phase" in
    prepare) phase_prepare ;;
    happy) phase_happy ;;
    inbox) phase_inbox ;;
    auth) phase_auth ;;
    declined) phase_declined ;;
    outage-start) phase_outage_start ;;
    outage-expire) phase_outage_expire ;;
    outage-restore) phase_outage_restore ;;
    resume) phase_resume ;;
    readings) phase_readings ;;
    regression) phase_regression ;;
    *) echo "Unknown phase: $phase" >&2; usage ;;
  esac
done

echo
echo "== $PASSED OK, $FAILED FAIL (run $RUN_ID, state $STATE)"
[[ "$FAILED" == 0 ]]
