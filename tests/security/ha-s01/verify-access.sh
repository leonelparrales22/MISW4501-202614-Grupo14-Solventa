#!/usr/bin/env bash
# Verifies the HA-S01 access rules against the ephemeral staging window
# (infra/terraform/environments/staging/ha-s01), with curl only. Two APIs:
#   [T] the test REST API (MOCK integrations that echo the claims)
#   [M] the real modules/apigateway, whose human route proxies to [T]
#
#   CA-1  human route without token / with a tampered signature or payload /
#         with an expired token -> 401 on [T] and [M]; the 401 of [M] carries
#         CORS headers and its OPTIONS preflight needs no token
#   CA-2  [M] answers 200 with the claims echoed by its backend: the module
#         forwards the JWT to the integration
#   CA-3  SignUp with the web client is rejected; a user cannot write
#         custom:partner_id; each seed user reaches the route with its group
#   CA-4  after RevokeToken the session cannot be renewed; the revoked ID token
#         is recorded right away and, with WAIT_FOR_EXPIRY=1, polled until it
#         stops passing: FAIL if it still passes 30 s after its exp (bound of
#         the residual window, decision DS-4)
#   CA-5  human route with only an API key -> 401; partner route with only a
#         Bearer token -> 403; partner route with the API key -> 200
#
# Secrets are never printed: the password comes from the environment, request
# bodies go through stdin and request headers through a private temporary file.
#
# Usage (from any directory, after terraform apply of the window):
#   export TF_VAR_seed_user_password=...      # same value used in the apply
#   bash tests/security/ha-s01/verify-access.sh
# Options (environment):
#   TF_DIR           composition directory (default: the staging/ha-s01 one)
#   WAIT_FOR_EXPIRY  1 = also bound the revocation window and wait until an ID
#                    token expires (~5-6 min)
#   POLL_SECONDS     interval of the revocation polling (default 30)
#   PARTNER_ID       expected custom:partner_id of the partner seed user (default socio-a)
#   COGNITO_URL      Cognito endpoint (default: the public one of the region; e.g. an emulator)
# The [M] cases are SKIPPED when the composition has no module outputs.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TF_DIR="${TF_DIR:-$SCRIPT_DIR/../../../infra/terraform/environments/staging/ha-s01}"
WAIT_FOR_EXPIRY="${WAIT_FOR_EXPIRY:-0}"
POLL_SECONDS="${POLL_SECONDS:-30}"
PARTNER_ID="${PARTNER_ID:-socio-a}"
PASSWORD="${SEED_USER_PASSWORD:-${TF_VAR_seed_user_password:-}}"
# A revoked ID token must stop passing at most this long after its exp (DS-4).
REVOCATION_GRACE=30

if [[ -z "$PASSWORD" ]]; then
  echo "Set TF_VAR_seed_user_password (or SEED_USER_PASSWORD) with the password of the seed users." >&2
  exit 2
fi
if [[ ! "$POLL_SECONDS" =~ ^[1-9][0-9]*$ ]]; then
  echo "POLL_SECONDS must be a positive number of seconds." >&2
  exit 2
fi

umask 077
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

tf_out() { terraform -chdir="$TF_DIR" output -raw "$1"; }
tf_out_opt() { terraform -chdir="$TF_DIR" output -raw "$1" 2>/dev/null || true; }
email_of() { # key of seed_user_emails
  terraform -chdir="$TF_DIR" output -json seed_user_emails | sed -n "s/.*\"$1\": *\"\([^\"]*\)\".*/\1/p" | head -n 1
}

REGION="$(tf_out region)"
HUMAN_URL="$(tf_out human_url)"
PARTNER_URL="$(tf_out partner_url)"
CLIENT_ID="$(tf_out web_client_id)"
API_KEY="$(tf_out api_key_value)"
MODULE_HUMAN_URL="$(tf_out_opt module_human_url)"
MODULE_PARTNER_URL="$(tf_out_opt module_partner_url)"
MODULE_API_KEY="$(tf_out_opt module_api_key_value)"
ADVISOR_EMAIL="$(email_of advisor)"
OPERATIONS_EMAIL="$(email_of operations)"
PARTNER_EMAIL="$(email_of partner)"
INSURED_EMAIL="$(email_of insured)"
COGNITO_URL="${COGNITO_URL:-https://cognito-idp.${REGION}.amazonaws.com/}"

FAILED=0
ok() { printf 'OK      %-70s %s\n' "$1" "$2"; }
fail() { printf 'FAIL    %-70s %s\n' "$1" "$2"; FAILED=1; }
record() { printf 'RECORD  %-70s %s\n' "$1" "$2"; }
skip() { printf 'SKIPPED %-70s %s\n' "$1" "$2"; }
expect_code() { # case expected obtained
  if [[ "$3" == "$2" ]]; then ok "$1" "HTTP $3"; else fail "$1" "expected HTTP $2, got $3"; fi
}
no_module() { skip "$1" "the composition has no module outputs"; }

json_escape() { local s=${1//\\/\\\\}; printf '%s' "${s//\"/\\\"}"; }
field() { sed -n "s/.*\"$1\": *\"\([^\"]*\)\".*/\1/p" "$TMP/body" | head -n 1; }
header() { sed -n "s/^$1: *//Ip" "$TMP/headers" | tr -d '\r' | head -n 1; }

# Cognito user pool API (public, no AWS credentials). Body on stdin.
cognito() { # target -> HTTP code; body in $TMP/body
  curl -sS -o "$TMP/body" -w '%{http_code}' -X POST "$COGNITO_URL" \
    -H 'Content-Type: application/x-amz-json-1.1' \
    -H "X-Amz-Target: AWSCognitoIdentityProviderService.$1" \
    --data-binary @-
}

# HTTP request with optional headers kept out of the process list.
request() { # method url [header...] -> HTTP code; body in $TMP/body, response headers in $TMP/headers
  local method="$1" url="$2" h
  shift 2
  : >"$TMP/header"
  for h in "$@"; do printf '%s\n' "$h" >>"$TMP/header"; done
  curl -sS -X "$method" -D "$TMP/headers" -o "$TMP/body" -w '%{http_code}' -H @"$TMP/header" "$url"
}
get() { request GET "$@"; }

sign_in() { # email -> sets ID_TOKEN, ACCESS_TOKEN, REFRESH_TOKEN
  local code
  code="$(printf '{"AuthFlow":"USER_PASSWORD_AUTH","ClientId":"%s","AuthParameters":{"USERNAME":"%s","PASSWORD":"%s"}}' \
    "$CLIENT_ID" "$1" "$(json_escape "$PASSWORD")" | cognito InitiateAuth)"
  if [[ "$code" != 200 ]]; then
    echo "Sign-in of $1 failed: HTTP $code $(field __type). Check the seed users and the password." >&2
    exit 1
  fi
  ID_TOKEN="$(field IdToken)"
  ACCESS_TOKEN="$(field AccessToken)"
  REFRESH_TOKEN="$(field RefreshToken)"
}

b64url_decode() { # base64url -> bytes
  local s
  s="$(tr '_-' '/+' <<<"$1")"
  while ((${#s} % 4)); do s+="="; done
  base64 -d <<<"$s" 2>/dev/null
}
b64url_encode() { base64 -w0 | tr '+/' '-_' | tr -d '='; }

# Changes one character in the middle of the signature (the last character
# may only carry padding bits).
tamper() { # jwt
  local header payload signature i c
  IFS=. read -r header payload signature <<<"$1"
  i=$((${#signature} / 2))
  c="${signature:i:1}"
  [[ "$c" == A ]] && c=B || c=A
  printf '%s.%s.%s' "$header" "$payload" "${signature:0:i}${c}${signature:i+1}"
}

# Rewrites the group of the payload to operations and keeps the original
# signature (an escalation attempt). Prints nothing if the payload has no group.
tamper_payload() { # jwt
  local header payload signature claims changed
  IFS=. read -r header payload signature <<<"$1"
  claims="$(b64url_decode "$payload")"
  changed="$(sed 's/\("cognito:groups": *\[\)[^]]*\]/\1"operations"]/' <<<"$claims")"
  [[ "$changed" == "$claims" ]] && return 0
  printf '%s.%s.%s' "$header" "$(printf '%s' "$changed" | b64url_encode)" "$signature"
}

expiry_of() { # jwt -> exp (epoch seconds)
  local payload
  payload="$(cut -d. -f2 <<<"$1")"
  b64url_decode "$payload" | sed -n 's/.*"exp": *\([0-9][0-9]*\).*/\1/p'
}

# Checks that a route echoes the claims of the expected group.
expect_claims() { # case url token group [route]
  local code
  code="$(get "$2" "Authorization: Bearer $3")"
  if [[ "$code" == 200 && "$(field groups)" == *"$4"* && -n "$(field sub)" && (-z "${5:-}" || "$(field route)" == "$5") ]]; then
    ok "$1" "HTTP 200, sub and group $4 in the claims${5:+ (route $5)}"
  else
    fail "$1" "HTTP $code, route '$(field route)', group '$(field groups)'"
  fi
}

echo "HA-S01 access verification - region $REGION"
[[ -z "$MODULE_HUMAN_URL" ]] && echo "No module outputs: the [M] cases are skipped."
echo

# --- 1. CA-1 / CA-5: without a token, and with only an API key ---------------------------
expect_code "Human route without token [T]" 401 "$(get "$HUMAN_URL")"
if [[ -n "$MODULE_HUMAN_URL" ]]; then
  code="$(get "$MODULE_HUMAN_URL")"
  allow_origin="$(header Access-Control-Allow-Origin)"
  if [[ "$code" == 401 && -n "$allow_origin" ]]; then
    ok "Human route without token [M]" "HTTP 401 with Access-Control-Allow-Origin: $allow_origin"
  else
    fail "Human route without token [M]" "HTTP $code, Access-Control-Allow-Origin '${allow_origin}'"
  fi

  code="$(request OPTIONS "$MODULE_HUMAN_URL" "Origin: http://localhost:4200" "Access-Control-Request-Method: GET")"
  allow_methods="$(header Access-Control-Allow-Methods)"
  if [[ "$code" == 200 && "$allow_methods" == *GET* ]]; then
    ok "CORS preflight (OPTIONS) without token [M]" "HTTP 200, Access-Control-Allow-Methods: $allow_methods"
  else
    fail "CORS preflight (OPTIONS) without token [M]" "HTTP $code, Access-Control-Allow-Methods '${allow_methods}'"
  fi
else
  no_module "Human route without token [M]"
  no_module "CORS preflight (OPTIONS) without token [M]"
fi

expect_code "Human route with only the partner API key [T]" 401 "$(get "$HUMAN_URL" "x-api-key: $API_KEY")"
if [[ -n "$MODULE_HUMAN_URL" && -n "$MODULE_API_KEY" ]]; then
  expect_code "Human route with only the partner API key [M]" 401 "$(get "$MODULE_HUMAN_URL" "x-api-key: $MODULE_API_KEY")"
else
  no_module "Human route with only the partner API key [M]"
fi

# --- 2. Advisor session (t0): valid, tampered and access tokens --------------------------
sign_in "$ADVISOR_EMAIL"
ADVISOR_ID="$ID_TOKEN"
ADVISOR_ACCESS="$ACCESS_TOKEN"
ADVISOR_REFRESH="$REFRESH_TOKEN"

# The gateway flattens cognito:groups to a string, so the group is matched as a substring.
expect_claims "Human route with a valid ID token (advisor) [T]" "$HUMAN_URL" "$ADVISOR_ID" advisor
ADVISOR_SUB="$(field sub)"
if [[ -n "$MODULE_HUMAN_URL" ]]; then
  code="$(get "$MODULE_HUMAN_URL" "Authorization: Bearer $ADVISOR_ID")"
  if [[ "$code" == 200 && "$(field route)" == module-backend && "$(field groups)" == *advisor* && -n "$ADVISOR_SUB" && "$(field sub)" == "$ADVISOR_SUB" ]]; then
    ok "Human route with a valid ID token (advisor) [M]" "HTTP 200, claims echoed by the backend: the JWT reached the integration"
  else
    fail "Human route with a valid ID token (advisor) [M]" "HTTP $code, route '$(field route)', group '$(field groups)'"
  fi
else
  no_module "Human route with a valid ID token (advisor) [M]"
fi

TAMPERED_SIGNATURE="$(tamper "$ADVISOR_ID")"
TAMPERED_PAYLOAD="$(tamper_payload "$ADVISOR_ID")"
for api in T M; do
  url="$HUMAN_URL"
  if [[ "$api" == M ]]; then
    url="$MODULE_HUMAN_URL"
    if [[ -z "$url" ]]; then
      no_module "Human route with a tampered signature [M]"
      no_module "Human route with a tampered payload (group -> operations) [M]"
      no_module "Human route with the access token instead of the ID token [M]"
      continue
    fi
  fi
  expect_code "Human route with a tampered signature [$api]" 401 "$(get "$url" "Authorization: Bearer $TAMPERED_SIGNATURE")"
  if [[ -n "$TAMPERED_PAYLOAD" ]]; then
    expect_code "Human route with a tampered payload (group -> operations) [$api]" 401 \
      "$(get "$url" "Authorization: Bearer $TAMPERED_PAYLOAD")"
  else
    fail "Human route with a tampered payload (group -> operations) [$api]" "the ID token has no cognito:groups to tamper"
  fi
  record "Human route with the access token instead of the ID token [$api]" \
    "HTTP $(get "$url" "Authorization: Bearer $ADVISOR_ACCESS") (D-4: the routes expect ID tokens)"
done

# --- 3. CA-5: partner route -------------------------------------------------------------
expect_code "Partner route with only a Bearer token [T]" 403 "$(get "$PARTNER_URL" "Authorization: Bearer $ADVISOR_ID")"
if [[ -n "$MODULE_PARTNER_URL" ]]; then
  expect_code "Partner route with only a Bearer token [M]" 403 "$(get "$MODULE_PARTNER_URL" "Authorization: Bearer $ADVISOR_ID")"
else
  no_module "Partner route with only a Bearer token [M]"
fi
expect_code "Partner route with the API key [T]" 200 "$(get "$PARTNER_URL" "x-api-key: $API_KEY")"

# --- 4. CA-3: every seed user reaches the route with its own group ------------------------
sign_in "$PARTNER_EMAIL"
code="$(get "$HUMAN_URL" "Authorization: Bearer $ID_TOKEN")"
if [[ "$code" == 200 && "$(field groups)" == *partner* && "$(field partner_id)" == "$PARTNER_ID" ]]; then
  ok "Partner portal user: group and custom:partner_id in the claims [T]" "HTTP 200, partner_id $PARTNER_ID"
else
  fail "Partner portal user: group and custom:partner_id in the claims [T]" "HTTP $code, group '$(field groups)', partner_id '$(field partner_id)'"
fi

# Signed in now (t1) so its token expires while the other cases run.
sign_in "$OPERATIONS_EMAIL"
OPERATIONS_ID="$ID_TOKEN"
expect_claims "Operations analyst: group in the claims [T]" "$HUMAN_URL" "$OPERATIONS_ID" operations

if [[ -n "$INSURED_EMAIL" ]]; then
  sign_in "$INSURED_EMAIL"
  expect_claims "Insured: group in the claims [T]" "$HUMAN_URL" "$ID_TOKEN" insured
else
  skip "Insured: group in the claims [T]" "the composition has no insured seed user"
fi

# --- 5. CA-3: no self sign-up, no self-assigned ownership ---------------------------------
code="$(printf '{"ClientId":"%s","Username":"signup.ha-s01@example.com","Password":"Signup-%s-%s!"}' \
  "$CLIENT_ID" "$RANDOM" "$(date +%s)" | cognito SignUp)"
if [[ "$code" != 200 && "$(field __type)" == *NotAuthorizedException ]]; then
  ok "SignUp with the web client" "rejected: HTTP $code NotAuthorizedException"
else
  fail "SignUp with the web client" "expected NotAuthorizedException, got HTTP $code $(field __type)"
fi

code="$(printf '{"AccessToken":"%s","UserAttributes":[{"Name":"custom:partner_id","Value":"socio-b"}]}' \
  "$ADVISOR_ACCESS" | cognito UpdateUserAttributes)"
if [[ "$code" != 200 ]]; then
  ok "User writes custom:partner_id on their own account" "rejected: HTTP $code $(field __type)"
else
  fail "User writes custom:partner_id on their own account" "accepted (HTTP 200)"
fi

# --- 6. CA-4: sign-out with token revocation ----------------------------------------------
code="$(printf '{"Token":"%s","ClientId":"%s"}' "$ADVISOR_REFRESH" "$CLIENT_ID" | cognito RevokeToken)"
REVOKED_AT="$(date +%s)"
expect_code "RevokeToken of the advisor session" 200 "$code"

code="$(printf '{"AuthFlow":"REFRESH_TOKEN_AUTH","ClientId":"%s","AuthParameters":{"REFRESH_TOKEN":"%s"}}' \
  "$CLIENT_ID" "$ADVISOR_REFRESH" | cognito InitiateAuth)"
if [[ "$code" != 200 ]]; then
  ok "Renewal with the revoked refresh token" "rejected: HTTP $code $(field __type)"
else
  fail "Renewal with the revoked refresh token" "accepted (HTTP 200)"
fi

code="$(printf '{"AccessToken":"%s"}' "$ADVISOR_ACCESS" | cognito GetUser)"
record "Cognito GetUser with the revoked access token" "HTTP $code $(field __type)"
record "Human route with the ID token issued before the revocation [T]" "HTTP $(get "$HUMAN_URL" "Authorization: Bearer $ADVISOR_ID") right after RevokeToken"
if [[ -n "$MODULE_HUMAN_URL" ]]; then
  record "Human route with the ID token issued before the revocation [M]" "HTTP $(get "$MODULE_HUMAN_URL" "Authorization: Bearer $ADVISOR_ID") right after RevokeToken"
else
  no_module "Human route with the ID token issued before the revocation [M]"
fi

# --- 7. CA-4 / CA-1 with WAIT_FOR_EXPIRY=1: residual window and expired token -----------
bound_label() { printf 'Revoked ID token stops passing by exp + %ss %s' "$REVOCATION_GRACE" "$1"; }
report_bound() { # api seconds-after-revocation last-code exp
  local rel
  if [[ -n "$2" ]]; then
    rel=$((REVOKED_AT + $2 - $4))
    if ((rel >= 0)); then rel="exp + ${rel}s"; else rel="exp - $((-rel))s"; fi
    ok "$(bound_label "$1")" "HTTP 401 ${2}s after the revocation (${rel})"
  else
    fail "$(bound_label "$1")" "still HTTP $3 ${REVOCATION_GRACE}s after exp"
  fi
}

# Polls [T] and [M] with the revoked ID token until each one answers 401; the
# last poll happens at exp + REVOCATION_GRACE at the latest (BN-4).
bound_revocation() {
  local exp deadline now pause t_secs="" m_secs="" t_last="" m_last=""
  exp="$(expiry_of "$ADVISOR_ID")"
  if [[ -z "$exp" ]]; then
    fail "$(bound_label "[T]")" "could not read exp from the ID token"
    return 0
  fi
  deadline=$((exp + REVOCATION_GRACE))
  echo "Polling the revoked ID token every ${POLL_SECONDS}s until HTTP 401 (limit: exp + ${REVOCATION_GRACE}s, in $((deadline - $(date +%s)))s)..."
  while :; do
    now="$(date +%s)"
    if [[ -z "$t_secs" ]]; then
      t_last="$(get "$HUMAN_URL" "Authorization: Bearer $ADVISOR_ID")"
      if [[ "$t_last" == 401 ]]; then t_secs=$((now - REVOKED_AT)); fi
    fi
    if [[ -n "$MODULE_HUMAN_URL" && -z "$m_secs" ]]; then
      m_last="$(get "$MODULE_HUMAN_URL" "Authorization: Bearer $ADVISOR_ID")"
      if [[ "$m_last" == 401 ]]; then m_secs=$((now - REVOKED_AT)); fi
    fi
    if [[ -n "$t_secs" && (-z "$MODULE_HUMAN_URL" || -n "$m_secs") ]]; then break; fi
    if ((now >= deadline)); then break; fi
    pause=$((deadline - now))
    if ((pause > POLL_SECONDS)); then pause=$POLL_SECONDS; fi
    sleep "$pause"
  done
  report_bound "[T]" "$t_secs" "$t_last" "$exp"
  if [[ -n "$MODULE_HUMAN_URL" ]]; then
    report_bound "[M]" "$m_secs" "$m_last" "$exp"
  else
    no_module "$(bound_label "[M]")"
  fi
}

if [[ "$WAIT_FOR_EXPIRY" == 1 ]]; then
  bound_revocation

  exp="$(expiry_of "$OPERATIONS_ID")"
  if [[ -z "$exp" ]]; then
    fail "Human route with an expired ID token [T]" "could not read exp from the ID token"
  else
    wait_seconds=$((exp - $(date +%s) + 15))
    if ((wait_seconds > 0)); then
      echo "Waiting ${wait_seconds}s for the operations ID token to expire..."
      sleep "$wait_seconds"
    fi
    expect_code "Human route with an expired ID token [T]" 401 "$(get "$HUMAN_URL" "Authorization: Bearer $OPERATIONS_ID")"
    if [[ -n "$MODULE_HUMAN_URL" ]]; then
      expect_code "Human route with an expired ID token [M]" 401 "$(get "$MODULE_HUMAN_URL" "Authorization: Bearer $OPERATIONS_ID")"
    else
      no_module "Human route with an expired ID token [M]"
    fi
  fi
else
  for api in T M; do
    skip "$(bound_label "[$api]")" "run with WAIT_FOR_EXPIRY=1 (waits up to exp + ${REVOCATION_GRACE}s)"
    skip "Human route with an expired ID token [$api]" "run with WAIT_FOR_EXPIRY=1 (waits ~5 min)"
  done
fi

echo
if ((FAILED)); then
  echo "Result: at least one case failed."
  exit 1
fi
echo "Result: every checked case passed (RECORD lines are evidence for D-4/D-6, not pass/fail)."
