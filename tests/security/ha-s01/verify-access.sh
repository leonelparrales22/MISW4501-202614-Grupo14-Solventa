#!/usr/bin/env bash
# Verifies the HA-S01 access rules against the ephemeral staging window
# (infra/terraform/environments/staging/ha-s01), with curl only:
#
#   CA-1  human route without token / with a tampered or expired token -> 401
#   CA-3  SignUp with the web client is rejected; a user cannot write
#         custom:partner_id; the claims (sub, group, partner_id) reach the route
#   CA-4  after RevokeToken the session cannot be renewed; the code of the old
#         ID token on the route is recorded (decision D-6: residual window <= 5 min)
#   CA-5  human route with only an API key -> 401; partner route with only a
#         Bearer token -> 403; partner route with the API key -> 200
#
# Secrets are never printed: the password comes from the environment, request
# bodies go through stdin and headers through a private temporary file.
#
# Usage (from any directory, after terraform apply of the window):
#   export TF_VAR_seed_user_password=...      # same value used in the apply
#   bash tests/security/ha-s01/verify-access.sh
# Options (environment):
#   TF_DIR           composition directory (default: the staging/ha-s01 one)
#   WAIT_FOR_EXPIRY  1 = also wait until an ID token expires (~5 min) and check the 401
#   PARTNER_ID       expected custom:partner_id of the partner seed user (default socio-a)
#   COGNITO_URL      Cognito endpoint (default: the public one of the region; e.g. an emulator)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TF_DIR="${TF_DIR:-$SCRIPT_DIR/../../../infra/terraform/environments/staging/ha-s01}"
WAIT_FOR_EXPIRY="${WAIT_FOR_EXPIRY:-0}"
PARTNER_ID="${PARTNER_ID:-socio-a}"
PASSWORD="${SEED_USER_PASSWORD:-${TF_VAR_seed_user_password:-}}"

if [[ -z "$PASSWORD" ]]; then
  echo "Set TF_VAR_seed_user_password (or SEED_USER_PASSWORD) with the password of the seed users." >&2
  exit 2
fi

umask 077
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

tf_out() { terraform -chdir="$TF_DIR" output -raw "$1"; }
email_of() { # key of seed_user_emails
  terraform -chdir="$TF_DIR" output -json seed_user_emails | sed -n "s/.*\"$1\": *\"\([^\"]*\)\".*/\1/p" | head -n 1
}

REGION="$(tf_out region)"
HUMAN_URL="$(tf_out human_url)"
PARTNER_URL="$(tf_out partner_url)"
CLIENT_ID="$(tf_out web_client_id)"
API_KEY="$(tf_out api_key_value)"
ADVISOR_EMAIL="$(email_of advisor)"
OPERATIONS_EMAIL="$(email_of operations)"
PARTNER_EMAIL="$(email_of partner)"
COGNITO_URL="${COGNITO_URL:-https://cognito-idp.${REGION}.amazonaws.com/}"

FAILED=0
ok() { printf 'OK      %-62s %s\n' "$1" "$2"; }
fail() { printf 'FAIL    %-62s %s\n' "$1" "$2"; FAILED=1; }
record() { printf 'RECORD  %-62s %s\n' "$1" "$2"; }
skip() { printf 'SKIPPED %-62s %s\n' "$1" "$2"; }
expect_code() { # case expected obtained
  if [[ "$3" == "$2" ]]; then ok "$1" "HTTP $3"; else fail "$1" "expected HTTP $2, got $3"; fi
}

json_escape() { local s=${1//\\/\\\\}; printf '%s' "${s//\"/\\\"}"; }
field() { sed -n "s/.*\"$1\": *\"\([^\"]*\)\".*/\1/p" "$TMP/body" | head -n 1; }

# Cognito user pool API (public, no AWS credentials). Body on stdin.
cognito() { # target -> HTTP code; body in $TMP/body
  curl -sS -o "$TMP/body" -w '%{http_code}' -X POST "$COGNITO_URL" \
    -H 'Content-Type: application/x-amz-json-1.1' \
    -H "X-Amz-Target: AWSCognitoIdentityProviderService.$1" \
    --data-binary @-
}

# GET on the test API with an optional header kept out of the process list.
get() { # url [header] -> HTTP code; body in $TMP/body
  if [[ $# -gt 1 ]]; then
    printf '%s\n' "$2" >"$TMP/header"
    curl -sS -o "$TMP/body" -w '%{http_code}' -H @"$TMP/header" "$1"
  else
    curl -sS -o "$TMP/body" -w '%{http_code}' "$1"
  fi
}

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

expiry_of() { # jwt -> exp (epoch seconds)
  local payload
  payload="$(cut -d. -f2 <<<"$1" | tr '_-' '/+')"
  while ((${#payload} % 4)); do payload+="="; done
  base64 -d <<<"$payload" 2>/dev/null | sed -n 's/.*"exp": *\([0-9][0-9]*\).*/\1/p'
}

echo "HA-S01 access verification - region $REGION"
echo

# --- CA-1 / CA-5: human route ----------------------------------------------------------
# The gateway flattens cognito:groups to a string, so the group is matched as a substring.
expect_code "Human route without token" 401 "$(get "$HUMAN_URL")"
expect_code "Human route with only the partner API key" 401 "$(get "$HUMAN_URL" "x-api-key: $API_KEY")"

sign_in "$ADVISOR_EMAIL"
ADVISOR_ID="$ID_TOKEN"
ADVISOR_ACCESS="$ACCESS_TOKEN"
ADVISOR_REFRESH="$REFRESH_TOKEN"

code="$(get "$HUMAN_URL" "Authorization: Bearer $ADVISOR_ID")"
if [[ "$code" == 200 && "$(field groups)" == *advisor* && -n "$(field sub)" ]]; then
  ok "Human route with a valid ID token (advisor)" "HTTP 200, sub and group advisor in the claims"
else
  fail "Human route with a valid ID token (advisor)" "HTTP $code, group '$(field groups)'"
fi

expect_code "Human route with a tampered token" 401 "$(get "$HUMAN_URL" "Authorization: Bearer $(tamper "$ADVISOR_ID")")"

# --- CA-5: partner route ---------------------------------------------------------------
expect_code "Partner route with only a Bearer token" 403 "$(get "$PARTNER_URL" "Authorization: Bearer $ADVISOR_ID")"
expect_code "Partner route with the API key" 200 "$(get "$PARTNER_URL" "x-api-key: $API_KEY")"

# --- CA-3: ownership claim, no self sign-up, no self-assigned ownership -------------------
sign_in "$PARTNER_EMAIL"
code="$(get "$HUMAN_URL" "Authorization: Bearer $ID_TOKEN")"
if [[ "$code" == 200 && "$(field groups)" == *partner* && "$(field partner_id)" == "$PARTNER_ID" ]]; then
  ok "Partner portal user: group and custom:partner_id in the claims" "HTTP 200, partner_id $PARTNER_ID"
else
  fail "Partner portal user: group and custom:partner_id in the claims" "HTTP $code, group '$(field groups)', partner_id '$(field partner_id)'"
fi

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

# Signed in now so its token expires while the other cases run.
sign_in "$OPERATIONS_EMAIL"
OPERATIONS_ID="$ID_TOKEN"

# --- CA-4: sign-out with token revocation -------------------------------------------------
code="$(printf '{"Token":"%s","ClientId":"%s"}' "$ADVISOR_REFRESH" "$CLIENT_ID" | cognito RevokeToken)"
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
record "Human route with the ID token issued before the revocation" "HTTP $(get "$HUMAN_URL" "Authorization: Bearer $ADVISOR_ID") (D-6)"

# --- CA-1: expired token -----------------------------------------------------------------
if [[ "$WAIT_FOR_EXPIRY" == 1 ]]; then
  exp="$(expiry_of "$OPERATIONS_ID")"
  if [[ -z "$exp" ]]; then
    fail "Human route with an expired ID token" "could not read exp from the ID token"
  else
    wait_seconds=$((exp - $(date +%s) + 15))
    if ((wait_seconds > 0)); then
      echo "Waiting ${wait_seconds}s for the operations ID token to expire..."
      sleep "$wait_seconds"
    fi
    expect_code "Human route with an expired ID token" 401 "$(get "$HUMAN_URL" "Authorization: Bearer $OPERATIONS_ID")"
  fi
else
  skip "Human route with an expired ID token" "run with WAIT_FOR_EXPIRY=1 (waits ~5 min)"
fi

echo
if ((FAILED)); then
  echo "Result: at least one case failed."
  exit 1
fi
echo "Result: every checked case passed (RECORD lines are evidence for D-6, not pass/fail)."
