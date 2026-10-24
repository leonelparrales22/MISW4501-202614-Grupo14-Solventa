#!/usr/bin/env bash
# API checks of HU-M1.3-01 for the cloud window (staging or production-demo):
# CA-1, CA-2, CA-3, CA-4 (methods that are not published) and CA-6, against
# the API Gateway stage. run.sh rehearses it against the local environment.
#
# Inputs (environment; the passwords are never printed nor written):
#   API_URL             base URL of the stage (or of audit in local runs)
#   COGNITO_CLIENT_ID   app client of the user pool (USER_PASSWORD_AUTH)
#   USER_A_EMAIL / USER_A_PASSWORD   insured A, registered from the app, with consents
#   USER_B_EMAIL / USER_B_PASSWORD   insured B, registered from the app
#   OPS_EMAIL / OPS_PASSWORD         optional: an operations user (expects 403)
#   AWS_REGION          default us-east-2
#   COGNITO_ENDPOINT    default https://cognito-idp.<region>.amazonaws.com/
#                       (a local endpoint only for the rehearsal with Moto)
#
# The tokens come from InitiateAuth, the public sign-in of the user pool; the
# script calls nothing else in AWS. It exits with a non-zero code when any
# check fails.
set -euo pipefail
set +x

for tool in curl jq; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done
for v in API_URL COGNITO_CLIENT_ID USER_A_EMAIL USER_A_PASSWORD USER_B_EMAIL USER_B_PASSWORD; do
  if [[ -z "${!v:-}" ]]; then
    echo "$v is required" >&2
    exit 2
  fi
done
REGION="${AWS_REGION:-us-east-2}"
COGNITO_ENDPOINT="${COGNITO_ENDPOINT:-https://cognito-idp.$REGION.amazonaws.com/}"
if [[ ! "$COGNITO_ENDPOINT" =~ ^https://cognito-idp\.[a-z0-9-]+\.amazonaws\.com/?$ && ! "$COGNITO_ENDPOINT" =~ ^http://(localhost|127\.0\.0\.1)(:[0-9]+)?/?$ ]]; then
  echo "COGNITO_ENDPOINT must be the Cognito endpoint of the region or a local endpoint" >&2
  exit 2
fi
API_URL="${API_URL%/}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
FAILS=0
check() { # <description> <expected> <actual>
  if [[ "$2" == "$3" ]]; then
    echo "  ok    $1"
  else
    echo "  FAIL  $1 (expected '$2', got '$3')" >&2
    FAILS=$((FAILS + 1))
  fi
}
call() { # <method> <path> [token]: sets STATUS and BODY
  local args=(-s -o "$WORK/body" -w '%{http_code}' -X "$1" "$API_URL$2")
  [[ "$1" != GET ]] && args+=(-H 'Content-Type: application/json' -d '{}')
  [[ -n "${3:-}" ]] && args+=(-H "Authorization: Bearer $3")
  STATUS="$(curl "${args[@]}")"
  BODY="$(cat "$WORK/body")"
}
field() { jq -r "$1" <<<"$BODY"; }

# token <email> <password>: the ID token of the user (never printed).
token() {
  jq -n --arg c "$COGNITO_CLIENT_ID" --arg u "$1" --arg p "$2" \
    '{AuthFlow:"USER_PASSWORD_AUTH",ClientId:$c,AuthParameters:{USERNAME:$u,PASSWORD:$p}}' |
    curl -sf -X POST "$COGNITO_ENDPOINT" -H 'Content-Type: application/x-amz-json-1.1' \
      -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' --data-binary @- |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken // empty'
}

TOKEN_A="$(token "$USER_A_EMAIL" "$USER_A_PASSWORD")"
TOKEN_B="$(token "$USER_B_EMAIL" "$USER_B_PASSWORD")"
if [[ -z "$TOKEN_A" || -z "$TOKEN_B" ]]; then
  echo "Could not sign in A or B" >&2
  exit 1
fi

echo "== CA-1: A lists their consents, grouped"
call GET /v1/consents "$TOKEN_A"
check "A lists (200)" 200 "$STATUS"
TOTAL="$(field '.items | length')"
CURRENT="$(field '[.items[] | select(.current)] | length')"
echo "        $TOTAL consents, $CURRENT current"
check "A has at least one current consent" true "$([[ "$CURRENT" -ge 1 ]] && echo true || echo false)"
check "every item names its entity, dates and status" "$TOTAL" \
  "$(field '[.items[] | select((.entities | length) > 0 and .entities[0].name != null and .granted_at != null and .expires_at != null and .status != null)] | length')"
check "the current ones come first" true \
  "$(field '[.items[].current] as $c | ($c | index(false)) as $f | if $f == null then true else ($c[$f:] | all(. == false)) end')"
FIRST_ID="$(field '.items[0].consent_id')"
A_IDS="$(field '[.items[].consent_id] | join(" ")')"

echo "== CA-2: the detail of a consent"
call GET "/v1/consents/$FIRST_ID" "$TOKEN_A"
check "A reads the detail (200)" 200 "$STATUS"
check "the detail has every field of CA-2" true \
  "$(field '.consent_id != null and (.entities | length) > 0 and (.scope | length) > 0 and .purpose != null and .granted_at != null and .channel != null and .expires_at != null and .status != null and (.history | length) > 0')"
check "the history has dates and times" true "$(field '[.history[].occurred_at] | all(. != null)')"

echo "== CA-3: the history"
call GET /v1/consents "$TOKEN_A"
ENDED="$(field '[.items[] | select(.current | not)] | length')"
if [[ "$ENDED" -gt 0 ]]; then
  check "every revoked or expired one has its end" "$ENDED" "$(field '[.items[] | select((.current | not) and .ended_at != null)] | length')"
  check "every revoked one has its receipt" true "$(field '[.items[] | select(.status == "revoked")] | all(.revocation_receipt_id != null)')"
else
  echo "  note  A has no revoked or expired consent yet: CA-3 is not demonstrated by this run"
fi

echo "== CA-4: nothing but GET"
for method in POST PUT PATCH DELETE; do
  for path in /v1/consents "/v1/consents/$FIRST_ID"; do
    call "$method" "$path" "$TOKEN_A"
    case "$STATUS" in
    403 | 404 | 405) check "$method $path is refused" refused refused ;;
    *) check "$method $path is refused" refused "$STATUS" ;;
    esac
  done
done

echo "== CA-6: only the holder"
call GET /v1/consents "$TOKEN_B"
check "B lists (200)" 200 "$STATUS"
LEAK=0
for id in $A_IDS; do
  if field '[.items[].consent_id] | join(" ")' | tr ' ' '\n' | grep -qx -- "$id"; then
    LEAK=$((LEAK + 1))
  fi
done
check "none of A's consents is in B's list" 0 "$LEAK"
call GET "/v1/consents/$FIRST_ID" "$TOKEN_B"
check "A's consent is 404 for B" 404 "$STATUS"
call GET /v1/consents
check "without a token: 401" 401 "$STATUS"
if [[ -n "${OPS_EMAIL:-}" && -n "${OPS_PASSWORD:-}" ]]; then
  OPS_TOKEN="$(token "$OPS_EMAIL" "$OPS_PASSWORD")"
  call GET /v1/consents "$OPS_TOKEN"
  check "operations: 403" 403 "$STATUS"
fi

if [[ "$FAILS" -gt 0 ]]; then
  echo "== nube.sh: $FAILS check(s) failed" >&2
  exit 1
fi
echo "== nube.sh passed."
