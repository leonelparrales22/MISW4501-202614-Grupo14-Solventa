#!/usr/bin/env bash
# Smoke test of the web portal in AWS (T5): the Angular build served by CloudFront
# from a private S3 bucket, with the API Gateway of the platform on the same
# domain. Everything goes through the URL of the portal, as a browser would.
# Bash and curl only; JSON is read with grep and sed (there is no jq).
#
# Usage: smoke.sh [phase ...]        (no phase, or "all": every phase)
#        smoke.sh --help
#
# Phases:
#   static    W-1  / and the deep routes of the SPA -> index.html; config.json (no-store, only
#                  public URLs and identifiers, the portal as API, the permanent user pool); a
#                  bundle is immutable; a missing file -> 403; security headers
#   bucket    W-2  the bucket straight from S3 -> 403; its policy is not public
#   api       W-3  /v1/* through CloudFront: 401 without a token, 200 with the advisor token,
#                  never cached (X-Cache Miss twice), 403 JSON for an unknown route
#   partner   W-4  registers the consent of cliente-<run> through CloudFront first; then
#                  POST /v1/risk-profiles/resolve through CloudFront without a key -> 200 (and
#                  with an invented key, which CloudFront overwrites); a customer without a
#                  consent -> 422 consent_required (origination asks audit, HU-W1.2-01 CA-3);
#                  straight to the gateway without a key -> 403 {"message":"Forbidden"}
#   tokens    W-5  POST /pasarela/tokens -> 201 tok_...; invalid number -> 400; the rest of the
#                  simulator (/pasarela/charges, /pasarela/debug/charges) -> 403
#   journey   W-6  demo offer -> card tokenized through the portal -> charge confirmed ->
#                  issuance completed -> e-mail link on the portal -> PDF through CloudFront with
#                  its checksum; a card ending in 0002 -> payment_rejected
#   links     W-7  task definition of origination: OFFER_LINK_BASE_URL and
#                  DOCUMENTS_PUBLIC_BASE_URL are the portal (read-only AWS calls)
#   publish   W-8  objects of the site: index.html no-cache, a bundle immutable, config.json
#                  with the ETag Terraform wrote; the last invalidation completed
#   audit     W-9  consent terms and consent through CloudFront (201, repeated -> 200 with the
#                  same consent_id); POST /v1/offers with it -> 201 off-...; with the consent of
#                  another customer -> 422 consent_mismatch; GET /v1/offers/{id} without a token
#                  -> 401 and with the insured token -> 200; POST /v1/offers straight to the
#                  gateway without a key -> 403
#   internal  W-10 GET /v1/audit/consent-verifications through CloudFront and straight to the
#                  gateway -> 403 (not published); AUDIT_URL of origination; its log since its
#                  task started says "verified with audit at" and never "AUDIT_URL is not
#                  set"; audit is a service of the Cloud Map namespace (read-only AWS calls)
#
# Secrets are never printed: the password comes from the environment, bodies with
# credentials go through stdin, the Authorization header through a private temporary
# file, and tokens and links are cut in the output. The key of the portal partner is
# never read: CloudFront adds it.
#
# Environment:
#   ENVIRONMENT       staging (default) or production-demo; picks the default composition
#                     directories below
#   WEB_TF_DIR        web composition (default infra/terraform/environments/<env>/web); fills
#                     PORTAL_URL, BUCKET, DISTRIBUTION_ID and CONFIG_ETAG from its outputs
#                     (terraform output -raw, never -json)
#   PLATFORM_TF_DIR   platform composition (default .../<env>/platform); fills INVOKE_URL,
#                     LOG_GROUP, CLUSTER, EXPECTED_USER_POOL_ID and EXPECTED_AUDIT_URL
#   PORTAL_URL, BUCKET, DISTRIBUTION_ID, CONFIG_ETAG, INVOKE_URL, LOG_GROUP, CLUSTER,
#   EXPECTED_USER_POOL_ID, EXPECTED_AUDIT_URL: set them to skip the Terraform outputs
#   SEED_USER_PASSWORD (or SOLVENTA_IDENTITY_SEED_PASSWORD): password of the permanent users of
#                     bootstrap/identity; needed by api, journey and audit
#   INSURED_USER, ADVISOR_USER, OPERATIONS_USER: usernames (defaults asegurado@, asesor@ and
#                     operaciones@example.com, the permanent users of bootstrap/identity)
#   RUN_ID            suffix of the demo offers and customers (default r<epoch seconds>)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

usage() {
  awk 'NR > 1 && /^set -euo pipefail/ { exit } NR > 1 { print }' "$0"
  exit "${1:-0}"
}
[[ "${1:-}" == "--help" || "${1:-}" == "-h" ]] && usage 0

export AWS_PAGER=""
# Read-only AWS CLI calls: keep Git Bash from rewriting /ecs/... and print UTF-8 on Windows.
aws_ro() { MSYS_NO_PATHCONV=1 PYTHONUTF8=1 aws "$@"; }

ENVIRONMENT="${ENVIRONMENT:-staging}"
case "$ENVIRONMENT" in
  staging | production-demo) ;;
  *) echo "ENVIRONMENT must be staging or production-demo." >&2; exit 2 ;;
esac
REGION="${AWS_REGION:-us-east-2}"
WEB_TF_DIR="${WEB_TF_DIR:-$REPO_ROOT/infra/terraform/environments/$ENVIRONMENT/web}"
PLATFORM_TF_DIR="${PLATFORM_TF_DIR:-$REPO_ROOT/infra/terraform/environments/$ENVIRONMENT/platform}"

# Without state Terraform prints "No outputs found" on stdout with exit code 0: that is nothing.
tf_raw() {
  local v
  v="$(terraform -chdir="$1" output -no-color -raw "$2" 2>/dev/null)" || return 0
  [[ "$v" == *"No outputs found"* || "$v" == *"Warning:"* ]] && return 0
  printf '%s' "$v"
}
[[ -n "${PORTAL_URL:-}" ]] || PORTAL_URL="$(tf_raw "$WEB_TF_DIR" url)"
[[ -n "${BUCKET:-}" ]] || BUCKET="$(tf_raw "$WEB_TF_DIR" bucket)"
[[ -n "${DISTRIBUTION_ID:-}" ]] || DISTRIBUTION_ID="$(tf_raw "$WEB_TF_DIR" distribution_id)"
[[ -n "${CONFIG_ETAG:-}" ]] || CONFIG_ETAG="$(tf_raw "$WEB_TF_DIR" config_etag)"
[[ -n "${INVOKE_URL:-}" ]] || INVOKE_URL="$(tf_raw "$PLATFORM_TF_DIR" invoke_url)"
[[ -n "${LOG_GROUP:-}" ]] || LOG_GROUP="$(tf_raw "$PLATFORM_TF_DIR" log_group)"
[[ -n "${CLUSTER:-}" ]] || CLUSTER="$(tf_raw "$PLATFORM_TF_DIR" ecs_cluster)"
[[ -n "${EXPECTED_AUDIT_URL:-}" ]] || EXPECTED_AUDIT_URL="$(tf_raw "$PLATFORM_TF_DIR" audit_url)"
if [[ -z "${EXPECTED_USER_POOL_ID:-}" ]]; then
  EXPECTED_USER_POOL_ID="$(terraform -chdir="$PLATFORM_TF_DIR" output -no-color cognito 2>/dev/null |
    sed -n 's/^ *"user_pool_id" *= *"\([^"]*\)".*/\1/p' | head -n 1 || true)"
fi
PORTAL_URL="${PORTAL_URL%/}"
INVOKE_URL="${INVOKE_URL%/}"

PASSWORD="${SEED_USER_PASSWORD:-${SOLVENTA_IDENTITY_SEED_PASSWORD:-}}"
INSURED_USER="${INSURED_USER:-asegurado@example.com}"
ADVISOR_USER="${ADVISOR_USER:-asesor@example.com}"
OPERATIONS_USER="${OPERATIONS_USER:-operaciones@example.com}"
RUN_ID="${RUN_ID:-r$(date +%s)}"
STARTED_MS="$(( ($(date +%s) - 60) * 1000 ))"

[[ -n "$PORTAL_URL" ]] || { echo "No portal URL: set PORTAL_URL or WEB_TF_DIR (an initialized web composition)." >&2; exit 2; }

PHASES=("$@")
if [[ ${#PHASES[@]} -eq 0 || "${PHASES[0]}" == all ]]; then
  PHASES=(static bucket api partner tokens journey links publish audit internal)
fi
for p in "${PHASES[@]}"; do
  case "$p" in
    static | bucket | api | partner | tokens | journey | links | publish | audit | internal) ;;
    *) echo "Unknown phase: $p (see --help)." >&2; exit 2 ;;
  esac
  if [[ "$p" == api || "$p" == journey || "$p" == audit ]] && [[ -z "$PASSWORD" ]]; then
    echo "Phase $p needs SEED_USER_PASSWORD or SOLVENTA_IDENTITY_SEED_PASSWORD." >&2
    exit 2
  fi
done

umask 077
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- Output -------------------------------------------------------------------------

PASSED=0
FAILED=0
ok() { printf 'OK      %-78s %s\n' "$1" "${2:-}"; PASSED=$((PASSED + 1)); }
fail() { printf 'FAIL    %-78s %s\n' "$1" "${2:-}"; FAILED=$((FAILED + 1)); }
skip() { printf 'SKIPPED %-78s %s\n' "$1" "${2:-}"; }
info() { printf 'INFO    %-78s %s\n' "$1" "${2:-}"; }
check() { if [[ "$2" == 0 ]]; then ok "$1" "$3"; else fail "$1" "$3"; fi; }
expect_code() { # case expected obtained [detail]
  if [[ "$3" == "$2" ]]; then ok "$1" "HTTP $3${4:+ $4}"; else fail "$1" "expected HTTP $2, got $3 $(cut -c1-160 "$TMP/body" 2>/dev/null)"; fi
}
cut_token() { printf '%s…' "${1:0:8}"; }
cut_link() { local l="$1" t="${1##*/v1/documents/}"; printf '%s/v1/documents/%s…' "${l%%/v1/documents/*}" "${t:0:8}"; }

# --- JSON (compact) -----------------------------------------------------------------

jget() { # key [text] -> first value of "key" (strings without quotes)
  local text v
  if [[ $# -ge 2 ]]; then text="$2"; else text="$(cat "$TMP/body")"; fi
  v="$(grep -o "\"$1\": *\"[^\"]*\"" <<<"$text" | head -n 1 || true)"
  if [[ -n "$v" ]]; then
    v="${v#*\"$1\"}"
    v="${v#*\"}"
    printf '%s' "${v%\"}"
    return 0
  fi
  v="$(grep -o "\"$1\": *[^,}\"]*" <<<"$text" | head -n 1 || true)"
  v="${v#*:}"
  printf '%s' "${v# }"
}

# --- HTTP ---------------------------------------------------------------------------

# request method url token [body] [header...] -> HTTP code; body in $TMP/body, headers in
# $TMP/headers. The token and the body never reach the process list.
request() {
  local method="$1" url="$2" token="$3" data="${4:-}" h
  shift 4 || shift $#
  : >"$TMP/header"
  [[ -n "$token" ]] && printf 'Authorization: Bearer %s\n' "$token" >>"$TMP/header"
  for h in "$@"; do printf '%s\n' "$h" >>"$TMP/header"; done
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
sha_of() { sha256sum "$1" | cut -d' ' -f1; }

# --- Cognito (client of config.json; tokens renewed every 4 minutes) -----------------

CLIENT_ID=""
load_config() { # fills CLIENT_ID and CONFIG_BODY from the config.json of the portal
  [[ -n "$CLIENT_ID" ]] && return 0
  local code
  code="$(request GET "$PORTAL_URL/config.json" "")"
  [[ "$code" == 200 ]] || return 1
  CONFIG_BODY="$(tr -d '\r\n' <"$TMP/body")"
  CLIENT_ID="$(jget clienteId "$CONFIG_BODY")"
  COGNITO_REGION="$(jget region "$CONFIG_BODY")"
}

token() { # role -> ID token
  local role="$1" user code now file="$TMP/token-$1"
  now="$(date +%s)"
  if [[ -s "$file" && $((now - $(cat "$file.at"))) -lt 240 ]]; then
    cat "$file"
    return 0
  fi
  load_config || { echo "config.json of the portal is not readable." >&2; return 1; }
  case "$role" in
    insured) user="$INSURED_USER" ;;
    advisor) user="$ADVISOR_USER" ;;
    operations) user="$OPERATIONS_USER" ;;
  esac
  code="$(printf '{"AuthFlow":"USER_PASSWORD_AUTH","ClientId":"%s","AuthParameters":{"USERNAME":"%s","PASSWORD":"%s"}}' \
    "$CLIENT_ID" "$user" "$(json_escape "$PASSWORD")" |
    curl -sS -o "$TMP/auth" -w '%{http_code}' -X POST "https://cognito-idp.${COGNITO_REGION:-$REGION}.amazonaws.com/" \
      -H 'Content-Type: application/x-amz-json-1.1' \
      -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' --data-binary @- || true)"
  if [[ "$code" != 200 ]]; then
    echo "Sign-in of $user failed: HTTP $code. Check the permanent users and the password." >&2
    rm -f "$TMP/auth"
    return 1
  fi
  grep -o '"IdToken": *"[^"]*"' "$TMP/auth" | sed 's/.*"\([^"]*\)"$/\1/' | tr -d '\n' >"$file"
  printf '%s' "$now" >"$file.at"
  rm -f "$TMP/auth"
  cat "$file"
}

# --- Origination through the portal --------------------------------------------------

accept() { # offer -> ACC
  local code
  ACC=""
  code="$(request POST "$PORTAL_URL/v1/offers/$1/acceptance" "$(token insured)" '{"channel":"web"}')"
  if [[ "$code" == 202 ]]; then
    ACC="$(jget acceptance_id)"
  else
    fail "Accept $1 -> 202" "HTTP $code $(cut -c1-160 "$TMP/body")"
    return 1
  fi
}
charge() { request GET "$PORTAL_URL/v1/acceptances/$1/charge" "$(token insured)"; }
wait_charge() { # acceptance timeout condition
  local deadline=$(( $(date +%s) + $2 )) code
  while :; do
    code="$(charge "$1")"
    if [[ "$code" == 200 ]] && "$3"; then return 0; fi
    (( $(date +%s) >= deadline )) && return 1
    sleep 2
  done
}
exists() { true; }
is_confirmed() { [[ "$(jget status)" == confirmed && -n "$(jget policy_id)" && "$(jget policy_id)" != null ]]; }
is_rejected() { [[ "$(jget status)" == failed && "$(jget failure_reason)" == payment_rejected ]]; }
# Called directly, never inside $(...): the globals it fills must reach the caller.
tokenize() { # card number -> TOKEN_CODE, TOK, BRAND, LAST4 (through the portal, without session)
  TOKEN_CODE="$(request POST "$PORTAL_URL/pasarela/tokens" "" "{\"number\":\"$1\",\"holder\":\"Prueba Portal\"}")"
  TOK="$(jget token)"
  BRAND="$(jget brand)"
  LAST4="$(jget last4)"
}
pay_card() { # acceptance -> code
  request POST "$PORTAL_URL/v1/acceptances/$1/charge/payment-method" "$(token insured)" \
    "{\"method\":\"card\",\"payment_token\":\"$TOK\",\"brand\":\"$BRAND\",\"last4\":\"$LAST4\",\"installments\":1}"
}
wait_issuance() { # policy timeout
  local deadline=$(( $(date +%s) + $2 )) code
  while :; do
    code="$(request GET "$PORTAL_URL/v1/policies/$1/issuance-status" "$(token insured)")"
    [[ "$code" == 200 && "$(jget status)" == completed ]] && return 0
    (( $(date +%s) >= deadline )) && return 1
    sleep 3
  done
}
logs_of() { # stream-prefix filter-pattern [start-ms]
  aws_ro logs filter-log-events --region "$REGION" --log-group-name "$LOG_GROUP" --log-stream-name-prefix "$1" \
    --start-time "${3:-$STARTED_MS}" --filter-pattern "$2" --query 'events[].message' --output text 2>/dev/null |
    tr '\t' '\n' | tr -d '\r'
}
wait_mail() { # to subject marker timeout
  local deadline=$(( $(date +%s) + $4 )) m
  while :; do
    m="$(logs_of email '"message accepted"' | grep -F "to=$1 " | grep -F "subject=\"$2\"" | grep -F "$3" | head -n 1 || true)"
    [[ -n "$m" ]] && { printf '%s' "$m"; return 0; }
    (( $(date +%s) >= deadline )) && return 1
    sleep 5
  done
}
# --- Consent of HU-W1.2-01 through the portal (W-4 and W-9) ---------------------------

# Called directly, never inside $(...): the globals they fill must reach the caller.
TERMS_VERSION=""
CONSENTED=" "
load_terms() { # -> TERMS_CODE, TERMS_VERSION, TERMS_SOURCES (the codes of every source, quoted)
  TERMS_CODE="$(request GET "$PORTAL_URL/v1/audit/consent-terms/current" "")"
  TERMS_VERSION="$(jget version)"
  TERMS_SOURCES="$(grep -o '"code": *"[a-z_]*"' "$TMP/body" | sed 's/.*"\([a-z_]*\)"$/"\1"/' | paste -sd, - || true)"
}
# Idempotent by (customer, application, terms version): the first time 201, then 200.
register_consent() { # customer application -> CONSENT_CODE, CONSENT_ID
  [[ -n "$TERMS_VERSION" ]] || load_terms
  CONSENT_CODE="$(request POST "$PORTAL_URL/v1/audit/consents" "" \
    "{\"customer_id\":\"$1\",\"document_id\":\"1020304050\",\"full_name\":\"Ana Maria Perez\",\"terms_version\":\"$TERMS_VERSION\",\"accepted_sources\":[${TERMS_SOURCES}],\"accepted_data_policy\":true,\"channel\":\"embedded\",\"partner_id\":\"portal-embebido\",\"application_id\":\"$2\"}")"
  CONSENT_ID="$(jget consent_id)"
  [[ "$CONSENT_CODE" == 201 || "$CONSENT_CODE" == 200 ]] && CONSENTED+="$1|$2 "
  return 0
}
consented() { [[ "$CONSENTED" == *" $1|$2 "* ]]; } # customer application -> registered in this run
offer_body() { # customer consent -> body of POST /v1/offers (HU-W1.2-03)
  printf '{"customer_id":"%s","consent_id":"%s","partner_id":"portal-embebido","application_id":"app-%s","document_id":"1020304050","full_name":"Ana Maria Perez","email":"ana.demo@example.com","birth_date":"1985-03-14","insured_amount":250000000,"term_months":120}' \
    "$1" "$2" "$RUN_ID"
}

link_in() { grep -o 'https\?://[^ "\\]*/v1/documents/[A-Za-z0-9._~-]*' <<<"$1" | head -n 1 || true; }
bundle_name() { grep -o 'main-[A-Za-z0-9]*\.js' "$1" | head -n 1 || true; }

# =====================================================================================
# Phases
# =====================================================================================

phase_static() { # W-1
  echo "== W-1 static ($PORTAL_URL)"
  local code index_sha keys bundle
  code="$(request GET "$PORTAL_URL/" "")"
  check "GET / -> 200 text/html" "$([[ "$code" == 200 && "$(header Content-Type)" == text/html* ]]; echo $?)" "HTTP $code, $(header Content-Type)"
  index_sha="$(sha_of "$TMP/body")"
  cp "$TMP/body" "$TMP/index.html"
  check "Security headers on / (HSTS, nosniff, X-Frame-Options)" \
    "$([[ -n "$(header Strict-Transport-Security)" && "$(header X-Content-Type-Options)" == nosniff && -n "$(header X-Frame-Options)" ]]; echo $?)" \
    "HSTS '$(header Strict-Transport-Security)', X-Frame-Options '$(header X-Frame-Options)'"

  code="$(request GET "$PORTAL_URL/config.json" "")"
  CONFIG_BODY="$(tr -d '\r\n' <"$TMP/body")"
  check "GET /config.json -> 200 application/json, Cache-Control no-store" \
    "$([[ "$code" == 200 && "$(header Content-Type)" == application/json* && "$(header Cache-Control)" == *no-store* ]]; echo $?)" \
    "HTTP $code, $(header Cache-Control)"
  keys="$(sed 's/"cognito": *{[^}]*}//' <<<"$CONFIG_BODY" | grep -o '"[A-Za-z]*":' | sort | tr -d '":\n')"
  check "config.json has only apiGatewayUrl, cognito and pasarelaPagosUrl" \
    "$([[ "$keys" == apiGatewayUrlpasarelaPagosUrl && "$CONFIG_BODY" == *'"cognito"'* ]]; echo $?)" "top-level keys: $keys + cognito"
  check "config.json: the API and the payment gateway are the portal itself" \
    "$([[ "$(jget apiGatewayUrl "$CONFIG_BODY")" == "$PORTAL_URL" && "$(jget pasarelaPagosUrl "$CONFIG_BODY")" == "$PORTAL_URL/pasarela" ]]; echo $?)" \
    "apiGatewayUrl $(jget apiGatewayUrl "$CONFIG_BODY")"
  if [[ -n "$EXPECTED_USER_POOL_ID" ]]; then
    check "config.json: userPoolId is the pool of the platform (permanent pool)" \
      "$([[ "$(jget userPoolId "$CONFIG_BODY")" == "$EXPECTED_USER_POOL_ID" ]]; echo $?)" "userPoolId $(jget userPoolId "$CONFIG_BODY")"
  else
    skip "config.json: userPoolId is the pool of the platform" "EXPECTED_USER_POOL_ID unknown"
  fi
  check "config.json carries no key, secret or password" \
    "$(! grep -qiE 'api[-_]?key|secret|password|x-api-key' <<<"$CONFIG_BODY"; echo $?)" "$(wc -c <<<"$CONFIG_BODY" | tr -d ' ') bytes"

  for route in "/embebido/oferta/demo-active-$RUN_ID" "/originacion/bandeja" "/embebido/perfil-riesgo/cliente-$RUN_ID"; do
    code="$(request GET "$PORTAL_URL$route" "")"
    check "Reload $route -> 200 with index.html (SPA deep link)" \
      "$([[ "$code" == 200 && "$(sha_of "$TMP/body")" == "$index_sha" ]]; echo $?)" "HTTP $code"
  done

  bundle="$(bundle_name "$TMP/index.html")"
  if [[ -n "$bundle" ]]; then
    code="$(request GET "$PORTAL_URL/$bundle" "")"
    check "GET /$bundle -> 200, Cache-Control immutable" \
      "$([[ "$code" == 200 && "$(header Cache-Control)" == *immutable* ]]; echo $?)" "HTTP $code, $(header Cache-Control)"
  else
    fail "A bundle (main-*.js) referenced by index.html" "not found"
  fi
  code="$(request GET "$PORTAL_URL/no-existe-$RUN_ID.js" "")"
  expect_code "GET a missing file -> 403 of S3 (not the application)" 403 "$code"
}

phase_bucket() { # W-2
  echo "== W-2 bucket ($BUCKET)"
  if [[ -z "$BUCKET" ]]; then skip "Bucket checks" "BUCKET unknown"; return 0; fi
  local code status
  code="$(request GET "https://$BUCKET.s3.$REGION.amazonaws.com/index.html" "")"
  expect_code "index.html straight from S3 -> 403 (private bucket)" 403 "$code"
  status="$(aws_ro s3api get-bucket-policy-status --region "$REGION" --bucket "$BUCKET" --query 'PolicyStatus.IsPublic' --output text 2>&1 | tr -d '\r' || true)"
  check "get-bucket-policy-status -> IsPublic False" "$([[ "$status" == False ]]; echo $?)" "IsPublic $status"
}

phase_api() { # W-3
  echo "== W-3 api through CloudFront"
  local code t c1 c2
  code="$(request GET "$PORTAL_URL/v1/review-requests" "")"
  check "/v1/review-requests without a token -> 401 JSON of the gateway" \
    "$([[ "$code" == 401 && "$(header Content-Type)" == application/json* ]]; echo $?)" "HTTP $code $(cut -c1-80 "$TMP/body")"
  t="$(token advisor)"
  code="$(request GET "$PORTAL_URL/v1/review-requests" "$t")"
  c1="$(header X-Cache)"
  expect_code "/v1/review-requests with the advisor token -> 200 (Authorization forwarded)" 200 "$code" "token $(cut_token "$t")"
  code="$(request GET "$PORTAL_URL/v1/review-requests" "$t")"
  c2="$(header X-Cache)"
  check "Two equal GET of the API -> X-Cache Miss both times (never cached)" \
    "$([[ "$c1" == Miss* && "$c2" == Miss* ]]; echo $?)" "'$c1' / '$c2'"
  code="$(request GET "$PORTAL_URL/v1/no-existe-$RUN_ID" "")"
  check "/v1/<unknown> -> 403 JSON of the gateway (errors reach the browser untouched)" \
    "$([[ "$code" == 403 && "$(header Content-Type)" == application/json* ]]; echo $?)" "HTTP $code $(cut -c1-80 "$TMP/body")"
}

phase_partner() { # W-4
  echo "== W-4 partner route (run $RUN_ID)"
  local body code
  register_consent "cliente-$RUN_ID" "app-$RUN_ID"
  check "Consent of cliente-$RUN_ID registered through CloudFront first (HU-W1.2-01)" \
    "$([[ ( "$CONSENT_CODE" == 201 || "$CONSENT_CODE" == 200 ) && "$CONSENT_ID" == cns-* ]]; echo $?)" \
    "HTTP $CONSENT_CODE, consent $(cut_token "$CONSENT_ID")"
  body="{\"customer_id\":\"cliente-$RUN_ID\",\"consent_active\":true}"
  code="$(request POST "$PORTAL_URL/v1/risk-profiles/resolve" "" "$body")"
  expect_code "POST /v1/risk-profiles/resolve through CloudFront, no key -> 200 (CloudFront adds it)" 200 "$code" "mode $(jget mode)"
  code="$(request POST "$PORTAL_URL/v1/risk-profiles/resolve" "" "$body" "x-api-key: invented-$RUN_ID")"
  expect_code "The same with an invented x-api-key -> 200 (CloudFront overwrites it)" 200 "$code"
  code="$(request POST "$PORTAL_URL/v1/risk-profiles/resolve" "" "{\"customer_id\":\"sin-consentimiento-$RUN_ID\",\"consent_active\":true}")"
  check "sin-consentimiento-$RUN_ID -> 422 consent_required (origination asks audit, CA-3)" \
    "$([[ "$code" == 422 && "$(jget error)" == consent_required ]]; echo $?)" "HTTP $code $(cut -c1-80 "$TMP/body")"
  if [[ -n "$INVOKE_URL" ]]; then
    code="$(request POST "$INVOKE_URL/v1/risk-profiles/resolve" "" "$body")"
    check "Straight to the gateway without a key -> 403 {\"message\":\"Forbidden\"} (I-4)" \
      "$([[ "$code" == 403 && "$(jget message)" == Forbidden ]]; echo $?)" "HTTP $code $(cut -c1-80 "$TMP/body")"
  else
    skip "Straight to the gateway without a key" "INVOKE_URL unknown"
  fi
}

phase_tokens() { # W-5
  echo "== W-5 tokens"
  tokenize 4111111111111111
  check "POST /pasarela/tokens with 4111… -> 201 tok_…" "$([[ "$TOKEN_CODE" == 201 && "$TOK" == tok_* ]]; echo $?)" "HTTP $TOKEN_CODE, brand $BRAND, last4 $LAST4"
  tokenize 4111111111111112
  expect_code "An invalid card number -> 400" 400 "$TOKEN_CODE" "$(jget error)"
  expect_code "GET /pasarela/debug/charges -> 403 (not published)" 403 "$(request GET "$PORTAL_URL/pasarela/debug/charges" "")"
  expect_code "POST /pasarela/charges -> 403 (not published)" 403 "$(request POST "$PORTAL_URL/pasarela/charges" "" '{}')"
}

phase_journey() { # W-6
  echo "== W-6 journey (run $RUN_ID)"
  local id code policy sha mail link
  accept "demo-active-$RUN_ID" || return 0
  id="$ACC"
  ok "Accept demo-active-$RUN_ID through the portal -> 202" "acceptance $id"
  wait_charge "$id" 90 exists || { fail "The charge exists after the underwriting" "HTTP $(charge "$id")"; return 0; }
  tokenize 4111111111111111
  [[ "$TOKEN_CODE" == 201 ]] || { fail "Card tokenized through the portal" "HTTP $TOKEN_CODE"; return 0; }
  expect_code "Card token handed over -> 202" 202 "$(pay_card "$id")"
  if wait_charge "$id" 120 is_confirmed; then
    policy="$(jget policy_id)"
    ok "Charge confirmed with its policy" "policy $policy"
  else
    fail "Charge confirmed with its policy" "status $(jget status), failure $(jget failure_reason)"
    return 0
  fi
  if wait_issuance "$policy" 120; then
    sha="$(grep -o '"checksum_sha256":"[^"]*"' "$TMP/body" | head -n 1 | sed 's/.*:"\([^"]*\)"/\1/')"
    ok "Issuance completed" "checksum ${sha:0:12}…"
  else
    fail "Issuance completed" "$(cut -c1-160 "$TMP/body")"
    return 0
  fi
  if mail="$(wait_mail "$INSURED_USER" "Tu póliza de Solventa fue emitida" "$policy" 120)"; then
    link="$(link_in "$mail")"
    check "E-mail link on the portal: $PORTAL_URL/v1/documents/… (D-11)" \
      "$([[ "$link" == "$PORTAL_URL/v1/documents/"* ]]; echo $?)" "link $(cut_link "$link")"
  else
    fail "E-mail with the link of the document" "no message for $policy"
    return 0
  fi
  code="$(request GET "$link" "" "" "Accept: text/html,application/xhtml+xml,*/*;q=0.8")"
  check "The link opens the PDF through CloudFront, intact (SHA-256 = checksum_sha256)" \
    "$([[ "$code" == 200 && "$(header Content-Type)" == application/pdf* && "$(sha_of "$TMP/body")" == "$sha" ]]; echo $?)" \
    "HTTP $code, $(header Content-Type)"

  accept "demo-active-${RUN_ID}d" || return 0
  id="$ACC"
  wait_charge "$id" 90 exists || { fail "The charge of the declined card exists" "HTTP $(charge "$id")"; return 0; }
  tokenize 4000000000000002
  [[ "$TOKEN_CODE" == 201 ]] || { fail "Declined card tokenized" "HTTP $TOKEN_CODE"; return 0; }
  expect_code "Declined card token handed over -> 202" 202 "$(pay_card "$id")"
  if wait_charge "$id" 120 is_rejected; then
    ok "Card ending in 0002 -> failed payment_rejected (E4c)" "acceptance $id"
  else
    fail "Card ending in 0002 -> failed payment_rejected (E4c)" "status $(jget status), failure $(jget failure_reason)"
  fi
}

phase_links() { # W-7
  echo "== W-7 links of origination"
  if [[ -z "$CLUSTER" ]]; then skip "Task definition of origination" "CLUSTER unknown"; return 0; fi
  local td offer docs
  td="$(aws_ro ecs describe-services --region "$REGION" --cluster "$CLUSTER" --services origination \
    --query 'services[0].taskDefinition' --output text 2>&1 | tr -d '\r' || true)"
  offer="$(aws_ro ecs describe-task-definition --region "$REGION" --task-definition "$td" \
    --query "taskDefinition.containerDefinitions[0].environment[?name=='OFFER_LINK_BASE_URL'].value | [0]" --output text 2>&1 | tr -d '\r' || true)"
  docs="$(aws_ro ecs describe-task-definition --region "$REGION" --task-definition "$td" \
    --query "taskDefinition.containerDefinitions[0].environment[?name=='DOCUMENTS_PUBLIC_BASE_URL'].value | [0]" --output text 2>&1 | tr -d '\r' || true)"
  check "OFFER_LINK_BASE_URL of origination = the portal" "$([[ "$offer" == "$PORTAL_URL" ]]; echo $?)" "$offer (${td##*/})"
  check "DOCUMENTS_PUBLIC_BASE_URL of origination = the portal" "$([[ "$docs" == "$PORTAL_URL" ]]; echo $?)" "$docs"
}

phase_publish() { # W-8
  echo "== W-8 published objects"
  if [[ -z "$BUCKET" ]]; then skip "Published objects" "BUCKET unknown"; return 0; fi
  local cc bundle etag inv
  cc="$(aws_ro s3api head-object --region "$REGION" --bucket "$BUCKET" --key index.html --query CacheControl --output text 2>&1 | tr -d '\r' || true)"
  check "index.html: Cache-Control no-cache" "$([[ "$cc" == *no-cache* ]]; echo $?)" "$cc"
  if [[ ! -s "$TMP/index.html" ]]; then
    request GET "$PORTAL_URL/" "" >/dev/null
    cp "$TMP/body" "$TMP/index.html"
  fi
  bundle="$(bundle_name "$TMP/index.html")"
  if [[ -n "$bundle" ]]; then
    cc="$(aws_ro s3api head-object --region "$REGION" --bucket "$BUCKET" --key "$bundle" --query CacheControl --output text 2>&1 | tr -d '\r' || true)"
    check "$bundle: Cache-Control immutable" "$([[ "$cc" == *immutable* ]]; echo $?)" "$cc"
  else
    fail "A bundle referenced by index.html" "not found"
  fi
  etag="$(aws_ro s3api head-object --region "$REGION" --bucket "$BUCKET" --key config.json --query ETag --output text 2>&1 | tr -d '\r"' || true)"
  if [[ -n "$CONFIG_ETAG" ]]; then
    check "config.json is the one Terraform wrote (same ETag; publishing never touches it)" \
      "$([[ "$etag" == "${CONFIG_ETAG//\"/}" ]]; echo $?)" "ETag ${etag:0:12}…"
  else
    skip "config.json ETag against Terraform" "CONFIG_ETAG unknown"
  fi
  if [[ -n "$DISTRIBUTION_ID" ]]; then
    inv="$(aws_ro cloudfront list-invalidations --distribution-id "$DISTRIBUTION_ID" --query 'InvalidationList.Items[0].Status' --output text 2>&1 | tr -d '\r' || true)"
    check "The last invalidation of the distribution -> Completed" "$([[ "$inv" == Completed ]]; echo $?)" "$inv"
  else
    skip "Last invalidation" "DISTRIBUTION_ID unknown"
  fi
}

phase_audit() { # W-9
  echo "== W-9 consent and personalized offer through the portal (run $RUN_ID)"
  local expected first other code offer t
  load_terms
  expect_code "GET /v1/audit/consent-terms/current through CloudFront -> 200" 200 "$TERMS_CODE" \
    "version $TERMS_VERSION, sources $TERMS_SOURCES"
  expected=201
  consented "cliente-$RUN_ID" "app-$RUN_ID" && expected=200 # W-4 already registered it
  register_consent "cliente-$RUN_ID" "app-$RUN_ID"
  first="$CONSENT_ID"
  check "POST /v1/audit/consents through CloudFront -> $expected cns-…" \
    "$([[ "$CONSENT_CODE" == "$expected" && "$first" == cns-* ]]; echo $?)" "HTTP $CONSENT_CODE, consent $(cut_token "$first")"
  register_consent "cliente-$RUN_ID" "app-$RUN_ID"
  check "Repeating it -> 200 with the same consent_id (idempotent)" \
    "$([[ "$CONSENT_CODE" == 200 && "$CONSENT_ID" == "$first" ]]; echo $?)" "HTTP $CONSENT_CODE, consent $(cut_token "$CONSENT_ID")"
  register_consent "cliente-$RUN_ID-b" "app-$RUN_ID-b"
  other="$CONSENT_ID"
  check "Consent of a second customer cliente-$RUN_ID-b -> 201" \
    "$([[ "$CONSENT_CODE" == 201 && "$other" == cns-* && "$other" != "$first" ]]; echo $?)" "HTTP $CONSENT_CODE, consent $(cut_token "$other")"

  code="$(request POST "$PORTAL_URL/v1/offers" "" "$(offer_body "cliente-$RUN_ID" "$first")")"
  offer="$(jget offer_id)"
  check "POST /v1/offers through CloudFront with that consent -> 201 off-…" \
    "$([[ "$code" == 201 && "$offer" == off-* ]]; echo $?)" "HTTP $code $offer, mode $(jget mode)"
  code="$(request POST "$PORTAL_URL/v1/offers" "" "$(offer_body "cliente-$RUN_ID" "$other")")"
  check "POST /v1/offers with the consent of cliente-$RUN_ID-b -> 422 consent_mismatch" \
    "$([[ "$code" == 422 && "$(jget error)" == consent_mismatch ]]; echo $?)" "HTTP $code $(cut -c1-80 "$TMP/body")"

  if [[ "$offer" == off-* ]]; then
    code="$(request GET "$PORTAL_URL/v1/offers/$offer" "")"
    expect_code "GET /v1/offers/$offer without a token -> 401 (the human route keeps Cognito)" 401 "$code"
    t="$(token insured)"
    code="$(request GET "$PORTAL_URL/v1/offers/$offer" "$t")"
    expect_code "GET /v1/offers/$offer with the insured token -> 200" 200 "$code" "status $(jget status), token $(cut_token "$t")"
  else
    skip "GET /v1/offers/{offer_id} with and without a token" "no personalized offer"
  fi
  if [[ -n "$INVOKE_URL" ]]; then
    code="$(request POST "$INVOKE_URL/v1/offers" "" "$(offer_body "cliente-$RUN_ID" "$first")")"
    check "POST /v1/offers straight to the gateway without a key -> 403 {\"message\":\"Forbidden\"}" \
      "$([[ "$code" == 403 && "$(jget message)" == Forbidden ]]; echo $?)" "HTTP $code $(cut -c1-80 "$TMP/body")"
  else
    skip "POST /v1/offers straight to the gateway without a key" "INVOKE_URL unknown"
  fi
}

phase_internal() { # W-10
  echo "== W-10 internal consent verification"
  local code td url task started started_ms verified unset_line namespace ns_id svc
  code="$(request GET "$PORTAL_URL/v1/audit/consent-verifications?customer_id=cliente-$RUN_ID" "")"
  expect_code "GET /v1/audit/consent-verifications through CloudFront -> 403 (not published)" 403 "$code"
  if [[ -n "$INVOKE_URL" ]]; then
    code="$(request GET "$INVOKE_URL/v1/audit/consent-verifications?customer_id=cliente-$RUN_ID" "")"
    expect_code "The same straight to the gateway -> 403 (not published)" 403 "$code"
  else
    skip "GET /v1/audit/consent-verifications straight to the gateway" "INVOKE_URL unknown"
  fi
  if [[ -z "$CLUSTER" ]]; then skip "AUDIT_URL, log and Cloud Map of origination" "CLUSTER unknown"; return 0; fi

  td="$(aws_ro ecs describe-services --region "$REGION" --cluster "$CLUSTER" --services origination \
    --query 'services[0].taskDefinition' --output text 2>&1 | tr -d '\r' || true)"
  url="$(aws_ro ecs describe-task-definition --region "$REGION" --task-definition "$td" \
    --query "taskDefinition.containerDefinitions[0].environment[?name=='AUDIT_URL'].value | [0]" --output text 2>&1 | tr -d '\r' || true)"
  if [[ -n "$EXPECTED_AUDIT_URL" ]]; then
    check "AUDIT_URL of origination = $EXPECTED_AUDIT_URL (Cloud Map, D-21 a)" \
      "$([[ "$url" == "$EXPECTED_AUDIT_URL" ]]; echo $?)" "$url (${td##*/})"
  else
    check "AUDIT_URL of origination = http://audit.<name>.local:8080 (Cloud Map, D-21 a)" \
      "$([[ "$url" =~ ^http://audit\.[a-z0-9-]+\.local:8080$ ]]; echo $?)" "$url (${td##*/})"
  fi

  task="$(aws_ro ecs list-tasks --region "$REGION" --cluster "$CLUSTER" --service-name origination --desired-status RUNNING \
    --query 'taskArns[0]' --output text 2>&1 | tr -d '\r' || true)"
  started="$(aws_ro ecs describe-tasks --region "$REGION" --cluster "$CLUSTER" --tasks "$task" \
    --query 'tasks[0].startedAt' --output text 2>&1 | tr -d '\r' || true)"
  started_ms="$(( $(date -d "$started" +%s 2>/dev/null || echo 0) * 1000 ))"
  if [[ "$task" == arn:* && "$started_ms" -gt 0 ]]; then
    verified="$(logs_of "origination/origination/${task##*/}" '"verified with audit at"' "$started_ms" | head -n 1 || true)"
    unset_line="$(logs_of "origination/origination/${task##*/}" '"AUDIT_URL is not set"' "$started_ms" | head -n 1 || true)"
    check "Log of origination since its task started: consent (HU-W1.2-01): verified with audit at …" \
      "$([[ "$verified" == *"consent (HU-W1.2-01): verified with audit at "* ]]; echo $?)" "task ${task##*/}, ${verified:0:110}"
    check "… and no line \"AUDIT_URL is not set\"" "$([[ -z "$unset_line" ]]; echo $?)" "${unset_line:0:110}"
  else
    fail "Running task of origination and its start" "task '${task##*/}', startedAt '$started'"
  fi

  namespace="${url#http://audit.}"
  namespace="${namespace%%:*}"
  ns_id="$(aws_ro servicediscovery list-namespaces --region "$REGION" \
    --query "Namespaces[?Name=='$namespace'].Id | [0]" --output text 2>&1 | tr -d '\r' || true)"
  svc="$(aws_ro servicediscovery list-services --region "$REGION" --filters "Name=NAMESPACE_ID,Values=$ns_id,Condition=EQ" \
    --query "Services[?Name=='audit'].Name | [0]" --output text 2>&1 | tr -d '\r' || true)"
  check "Cloud Map: the namespace $namespace has the service audit" "$([[ "$svc" == audit ]]; echo $?)" "namespace $ns_id, service $svc"
}

for p in "${PHASES[@]}"; do
  "phase_$p"
done

echo "== $PASSED OK, $FAILED FAIL (run $RUN_ID, $ENVIRONMENT, ${PHASES[*]})"
[[ "$FAILED" == 0 ]]
