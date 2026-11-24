#!/usr/bin/env bash
# API checks of HU-W1.5-01 for the cloud window (staging): the list of the
# policies about to expire of the advisor, GET /v1/policies/expiring, through
# the portal of CloudFront, as the browser calls it. It seeds nothing: the
# policies come from the single seed of HU-W1.5-01 and HU-W1.5-03
# (assisted-renewal-cloud.sh --seed), which buys them through the real journey
# for asegurado@example.com. This script reads them as asesor@example.com and
# checks CA-1, CA-2, CA-3 and CA-4, the status renewal_in_progress (it accepts
# the online renewal of one policy as the insured, HU-W1.5-02) and the roles.
#
# Inputs (environment; the password and the tokens are never printed):
#   PORTAL_URL              base URL of the portal (CloudFront)
#   SEED_USER_PASSWORD      password of the permanent users of bootstrap/identity
#                           (or SOLVENTA_IDENTITY_SEED_PASSWORD)
#   RENEW_ONLINE_POLICY_ID  the seeded policy the insured renews online for the
#                           check of renewal_in_progress (P3 of the seed: the
#                           others are kept for the demonstration of HU-W1.5-03)
#   INSURED_USER            default asegurado@example.com
#   ADVISOR_USER            default asesor@example.com
#   OUTPUT_FILE             optional: a copy of the output (for the evidence)
#   COGNITO_ENDPOINT        default https://cognito-idp.<region of config.json>.amazonaws.com/
#                           (a local endpoint only to rehearse the script)
#
# The Cognito client and region come from the config.json of the portal; the
# tokens come from InitiateAuth, the public sign-in of the user pool. The
# script calls nothing else in AWS. It needs curl and jq, and exits with a
# non-zero code when any check fails.
set -euo pipefail
set +x

for tool in curl jq; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done
PASSWORD="${SEED_USER_PASSWORD:-${SOLVENTA_IDENTITY_SEED_PASSWORD:-}}"
[[ -n "${PORTAL_URL:-}" ]] || { echo "PORTAL_URL is required" >&2; exit 2; }
[[ -n "$PASSWORD" ]] || { echo "SEED_USER_PASSWORD (or SOLVENTA_IDENTITY_SEED_PASSWORD) is required" >&2; exit 2; }
[[ -n "${RENEW_ONLINE_POLICY_ID:-}" ]] || {
  echo "RENEW_ONLINE_POLICY_ID is required: the seeded policy the insured renews online (P3 of assisted-renewal-cloud.sh --seed)" >&2
  exit 2
}
PORTAL_URL="${PORTAL_URL%/}"
INSURED_USER="${INSURED_USER:-asegurado@example.com}"
ADVISOR_USER="${ADVISOR_USER:-asesor@example.com}"
if [[ -n "${OUTPUT_FILE:-}" ]]; then
  mkdir -p "$(dirname "$OUTPUT_FILE")"
  exec > >(tee "$OUTPUT_FILE") 2>&1
fi

umask 077
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
FAILS=0
check() { # <description> <expected> <actual>
  if [[ "$2" == "$3" ]]; then
    echo "  ok    $1"
  else
    echo "  FAIL  $1 (expected '$2', got '$3')"
    FAILS=$((FAILS + 1))
  fi
}
json_escape() { local s=${1//\\/\\\\}; printf '%s' "${s//\"/\\\"}"; }

# call <method> <path> [token] [json body]: sets STATUS and BODY. The token goes
# through a private header file, never through the command line.
call() {
  : >"$WORK/header"
  [[ -n "${3:-}" ]] && printf 'Authorization: Bearer %s\n' "$3" >>"$WORK/header"
  printf 'User-Agent: hu-w1.5-01 expiring-cloud.sh\n' >>"$WORK/header"
  if [[ -n "${4:-}" ]]; then
    printf 'Content-Type: application/json\n' >>"$WORK/header"
    STATUS="$(printf '%s' "$4" | curl -s -o "$WORK/body" -w '%{http_code}' -X "$1" -H @"$WORK/header" \
      --data-binary @- "$PORTAL_URL$2" || true)"
  else
    STATUS="$(curl -s -o "$WORK/body" -w '%{http_code}' -X "$1" -H @"$WORK/header" "$PORTAL_URL$2" || true)"
  fi
  BODY="$(tr -d '\r' <"$WORK/body")"
}
field() { jq -r "$1" <<<"$BODY" 2>/dev/null || echo "<not json>"; }
# field_with <filter using $v> <value>: the value goes as a jq argument, never into the filter.
field_with() { jq -c --arg v "$2" "$1" <<<"$BODY" 2>/dev/null || echo "<not json>"; }

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

# token <user>: the ID token of the user (never printed).
token() {
  printf '{"AuthFlow":"USER_PASSWORD_AUTH","ClientId":"%s","AuthParameters":{"USERNAME":"%s","PASSWORD":"%s"}}' \
    "$CLIENT_ID" "$1" "$(json_escape "$PASSWORD")" |
    curl -s -X POST "$COGNITO_ENDPOINT" -H 'Content-Type: application/x-amz-json-1.1' \
      -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' --data-binary @- |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken // empty'
}
ADVISOR_TOKEN="$(token "$ADVISOR_USER" || true)"
INSURED_TOKEN="$(token "$INSURED_USER" || true)"
if [[ -z "$ADVISOR_TOKEN" || -z "$INSURED_TOKEN" ]]; then
  echo "Could not sign in $ADVISOR_USER or $INSURED_USER" >&2
  exit 1
fi

# Civil days of Colombia (UTC-5, no daylight saving time), computed by jq so
# the script does not depend on date -d. cot_day <epoch seconds>.
TODAY="$(TZ=UTC+5 date +%Y-%m-%d)"
cot_day() { jq -nr --argjson s "$1" '($s - 18000) | strftime("%Y-%m-%d")'; }
today_plus() { jq -nr --arg d "$TODAY" --argjson n "$1" '($d + "T00:00:00Z" | fromdateiso8601) + $n * 86400 | strftime("%Y-%m-%d")'; }
# end_epoch <coverage_end>: the instant of an RFC 3339 time with or without fraction.
end_epoch() { jq -nr --arg t "$1" '$t | sub("\\.[0-9]+"; "") | fromdateiso8601'; }

echo "== Data of the seed: the policies of $INSURED_USER"
call GET /v1/policies "$INSURED_TOKEN"
check "the wallet of the insured (200)" 200 "$STATUS"
RENEWABLE="$(field '[.items[] | select(.source == "origination" and .can_renew) | .policy_id] | join(" ")')"
COUNT="$(wc -w <<<"$RENEWABLE" | tr -d ' ')"
if [[ "$COUNT" -lt 2 ]]; then
  echo "== The insured has $COUNT renewable policies: run assisted-renewal-cloud.sh --seed first (HU-W1.5-03)" >&2
  exit 1
fi
echo "  info  renewable policies of the insured: $COUNT"

echo "== CA-1: the advisor sees the policies about to expire with their end and status"
call GET "/v1/policies/expiring?page_size=50" "$ADVISOR_TOKEN"
check "the advisor lists (200)" 200 "$STATUS"
DEFAULT="$BODY"
WINDOW="$(field .window_days)"
echo "  info  window_days=$WINDOW due_within_window=$(field .due_within_window) total=$(field .total)"
check "the default range starts today in Colombia" "$TODAY" "$(field .expires_from)"
check "and lasts the renewal window" "$(today_plus "$WINDOW")" "$(field .expires_to)"
check "every row has its end, a known status and the holder without e-mail" true \
  "$(field '[.items[] | (.coverage_end != null) and (.status == "expiring" or .status == "renewal_in_progress")
    and (.holder.full_name != null) and (.holder | has("email") | not) and (.line == "life")] | all')"
check "ordered by the end of the coverage" true "$(field '[.items[].coverage_end] | . == sort')"
MISSING=0
for id in $RENEWABLE; do
  row="$(jq -c --arg id "$id" '.items[] | select(.policy_id == $id)' <<<"$DEFAULT")"
  [[ -n "$row" ]] || { MISSING=$((MISSING + 1)); continue; }
done
check "every renewable policy of the insured within the range is listed" 0 "$MISSING"

TARGET="$(jq -r --arg skip "$RENEW_ONLINE_POLICY_ID" \
  '[.items[] | select(.can_renew and .status == "expiring" and .policy_id != $skip)][0].policy_id // empty' <<<"$DEFAULT")"
[[ -n "$TARGET" ]] || { echo "== No renewable row in the default range" >&2; exit 1; }
TARGET_END="$(jq -r --arg id "$TARGET" '.items[] | select(.policy_id == $id) | .coverage_end' <<<"$DEFAULT")"
TARGET_DAY="$(cot_day "$(end_epoch "$TARGET_END")")"
echo "  info  policy of the checks: $TARGET, ends $TARGET_END (day $TARGET_DAY in Colombia)"

echo "== CA-2: the filter by range of expiration (civil days of Colombia, both included)"
DAY_BEFORE="$(cot_day "$(( $(end_epoch "$TARGET_END") - 86400 ))")"
call GET "/v1/policies/expiring?expires_from=$TODAY&expires_to=$DAY_BEFORE&page_size=50" "$ADVISOR_TOKEN"
check "a range that ends the day before excludes it (200)" "200|false" \
  "$STATUS|$(field_with 'any(.items[]; .policy_id == $v)' "$TARGET")"
call GET "/v1/policies/expiring?expires_from=$TARGET_DAY&expires_to=$TARGET_DAY&page_size=50" "$ADVISOR_TOKEN"
check "the range of its own day includes it (200)" "200|true" "$STATUS|$(field_with 'any(.items[]; .policy_id == $v)' "$TARGET")"
check "the range applied is the one asked for" "$TARGET_DAY|$TARGET_DAY" "$(field '"\(.expires_from)|\(.expires_to)"')"
call GET "/v1/policies/expiring?expires_from=$TARGET_DAY&expires_to=$DAY_BEFORE" "$ADVISOR_TOKEN"
check "the end before the start: 400 out_of_range" "400|out_of_range" "$STATUS|$(field .error)"

echo "== CA-3: a range with no policies is an empty list, not an error"
EMPTY_FROM="$(today_plus 1800)"
call GET "/v1/policies/expiring?expires_from=$EMPTY_FROM&expires_to=$(today_plus 1807)" "$ADVISOR_TOKEN"
check "200 with no rows and total 0" "200|0|0" "$STATUS|$(field '.items | length')|$(field .total)"

echo "== CA-4: a renewable policy is the entry to the assisted renewal (HU-W1.5-03)"
ROW="$(jq -c --arg id "$TARGET" '.items[] | select(.policy_id == $id)' <<<"$DEFAULT")"
check "the row comes with can_renew and no renewal" "true|null" "$(jq -r '"\(.can_renew)|\(.renewal)"' <<<"$ROW")"
echo "  info  Renew (Renovar) -> $PORTAL_URL/originacion/renovaciones/$TARGET"
call GET "/v1/policies/expiring?expires_to=$(today_plus 800)&page_size=50" "$ADVISOR_TOKEN"
LATER="$(field '[.items[] | select(.status == "expiring" and (.can_renew | not))][0] // empty')"
if [[ -n "$LATER" ]]; then
  check "a policy outside the window has no Renovar and says when its window opens" true \
    "$(jq -r '(.renewal_window.opens_at > (now | todateiso8601))' <<<"$LATER")"
  echo "  info  its window opens at $(jq -r '.renewal_window.opens_at' <<<"$LATER") ($(jq -r .policy_id <<<"$LATER"))"
else
  echo "  info  no seeded policy ends outside the window (P7 of the seed has 24 months): nothing to check"
fi

echo "== Renewal in progress: the insured renews $RENEW_ONLINE_POLICY_ID online (HU-W1.5-02)"
call POST "/v1/policies/$RENEW_ONLINE_POLICY_ID/renewal" "$INSURED_TOKEN" '{}'
ACCEPT_STATUS="$STATUS"
check "the insured accepts the renewal (201, or 200 when it was already accepted)" true \
  "$([[ "$STATUS" == 201 || "$STATUS" == 200 ]] && echo true || echo false)"
call GET "/v1/policies/expiring?status=renewal_in_progress&page_size=50" "$ADVISOR_TOKEN"
IN_PROGRESS="$(field_with '.items[] | select(.policy_id == $v)' "$RENEW_ONLINE_POLICY_ID")"
if [[ -n "$IN_PROGRESS" && "$IN_PROGRESS" != "<not json>" ]]; then
  check "the advisor sees it in progress, with no Renovar and its renewal accepted" "renewal_in_progress|false|accepted" \
    "$(jq -r '"\(.status)|\(.can_renew)|\(.renewal.status)"' <<<"$IN_PROGRESS")"
else
  # The sweep may have closed it between the two calls (its charge is
  # confirmed in about a second): then it left the list, as it must.
  call GET "/v1/policies/$RENEW_ONLINE_POLICY_ID/renewal/status" "$INSURED_TOKEN"
  check "not in progress any more: the sweep already renewed it (accepted with $ACCEPT_STATUS)" "200|renewed" \
    "$STATUS|$(field .renewal.status)"
fi

echo "== Only the advisor"
call GET /v1/policies/expiring ""
check "without a token: 401 (the gateway or the service)" 401 "$STATUS"
call GET /v1/policies/expiring "$INSURED_TOKEN"
check "the insured: 403" 403 "$STATUS"
check "the refusal carries no policy" 0 "$(grep -c -- "$TARGET" "$WORK/body" || true)"

echo
if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-W1.5-01 expiring-cloud: $FAILS check(s) failed"
  exit 1
fi
echo "== HU-W1.5-01 expiring-cloud passed."
