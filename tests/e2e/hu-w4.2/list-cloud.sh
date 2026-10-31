#!/usr/bin/env bash
# API checks of HU-W4.2-01 for the cloud window (staging): the list of
# consents of the compliance portal through the portal of CloudFront, which
# sends /v1/audit/consents to the API Gateway with the API key of the partner
# and /v1/* with the token of the browser. It seeds its own consents through
# the partner route (marked with a run id in the name, so the checks do not
# depend on what else the environment holds), then checks CA-1, CA-2, CA-3,
# CA-4, CA-5 (a revocation from the app, with the token of the insured, and
# one from the portal) and CA-6. list.sh rehearses it against the local
# environment.
#
# Inputs (environment; the passwords are never printed nor written):
#   PORTAL_URL          base URL of the portal (CloudFront), or of audit in local runs
#   COGNITO_CLIENT_ID   app client of the user pool (USER_PASSWORD_AUTH)
#   OPS_EMAIL / OPS_PASSWORD           an operations user (the compliance role)
#   ADVISOR_EMAIL / ADVISOR_PASSWORD   an advisor (expects 403)
#   INSURED_EMAIL / INSURED_PASSWORD   an insured linked to INSURED_DOCUMENT
#   INSURED_DOCUMENT    the document of that insured (its consents are seeded
#                       with it and one of them is revoked as from the app)
#   PARTNER_ID          default aliado-banco-1
#   SEED_COUNT          consents to seed, default 13 (3 pages of 6)
#   AWS_REGION          default us-east-2
#   COGNITO_ENDPOINT    default https://cognito-idp.<region>.amazonaws.com/
#                       (a local endpoint only for the rehearsal with Moto)
#   CAPTURE_DIR         optional: keeps the responses as JSON
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
for v in PORTAL_URL COGNITO_CLIENT_ID OPS_EMAIL OPS_PASSWORD ADVISOR_EMAIL ADVISOR_PASSWORD INSURED_EMAIL INSURED_PASSWORD INSURED_DOCUMENT; do
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
PORTAL_URL="${PORTAL_URL%/}"
PARTNER_ID="${PARTNER_ID:-aliado-banco-1}"
SEED_COUNT="${SEED_COUNT:-13}"
RUN_ID="nube$(date +%s)"

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
yes_no() { if "$@"; then echo true; else echo false; fi; }
# call <method> <path> [token] [json body] [idempotency key]: sets STATUS and BODY.
call() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -X "$1" "$PORTAL_URL$2" -H 'User-Agent: hu-w4.2-01 list-cloud.sh')
  [[ -n "${3:-}" ]] && args+=(-H "Authorization: Bearer $3")
  [[ -n "${5:-}" ]] && args+=(-H "Idempotency-Key: $5")
  if [[ -n "${4:-}" ]]; then
    # The body goes through stdin: on Windows the arguments of curl lose their accents.
    STATUS="$(printf '%s' "$4" | curl "${args[@]}" -H 'Content-Type: application/json' --data-binary @-)"
  else
    STATUS="$(curl "${args[@]}")"
  fi
  BODY="$(cat "$WORK/body")"
}
field() { jq -r "$1" <<<"$BODY"; }
capture() {
  [[ -n "${CAPTURE_DIR:-}" ]] || return 0
  mkdir -p "$CAPTURE_DIR"
  jq . "$WORK/body" >"$CAPTURE_DIR/$1.json" 2>/dev/null || cp "$WORK/body" "$CAPTURE_DIR/$1.json"
}
uuid() {
  local hex
  hex="$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  echo "${hex:0:8}-${hex:8:4}-4${hex:13:3}-a${hex:17:3}-${hex:20:12}"
}

# token <email> <password>: the ID token of the user (never printed).
token() {
  jq -n --arg c "$COGNITO_CLIENT_ID" --arg u "$1" --arg p "$2" \
    '{AuthFlow:"USER_PASSWORD_AUTH",ClientId:$c,AuthParameters:{USERNAME:$u,PASSWORD:$p}}' |
    curl -sf -X POST "$COGNITO_ENDPOINT" -H 'Content-Type: application/x-amz-json-1.1' \
      -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' --data-binary @- |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken // empty'
}
OPS_TOKEN="$(token "$OPS_EMAIL" "$OPS_PASSWORD")"
ADVISOR_TOKEN="$(token "$ADVISOR_EMAIL" "$ADVISOR_PASSWORD")"
INSURED_TOKEN="$(token "$INSURED_EMAIL" "$INSURED_PASSWORD")"
if [[ -z "$OPS_TOKEN" || -z "$ADVISOR_TOKEN" || -z "$INSURED_TOKEN" ]]; then
  echo "Could not sign in the operations, advisor or insured user" >&2
  exit 1
fi

# The days of Colombia (UTC-5, no daylight saving time), without date -d.
TODAY="$(TZ=UTC+5 date +%Y-%m-%d)"

echo "== Seeding $SEED_COUNT consents through the partner route (run $RUN_ID)"
SEEDED=()
for i in $(seq 1 "$SEED_COUNT"); do
  document="$(printf '9%09d' "$(( ($(date +%s) + i * 7919) % 1000000000 ))")"
  [[ "$i" -le 2 ]] && document="$INSURED_DOCUMENT"
  # Plain ASCII and printf: the body does not depend on the encoding of the
  # command line of the system that runs the window.
  body="$(printf '{"customer_id":"cust-%s-%s","document_id":"%s","full_name":"Cliente %s Numero %s","terms_version":"v1",%s,"partner_id":"%s","application_id":"app-%s-%s"}' \
    "$RUN_ID" "$i" "$document" "$RUN_ID" "$i" '"accepted_sources":["open_finance","open_data","kyc"],"accepted_data_policy":true,"channel":"embedded"' \
    "$PARTNER_ID" "$RUN_ID" "$i")"
  call POST /v1/audit/consents "" "$body"
  if [[ "$STATUS" != 201 ]]; then
    check "consent $i recorded through the partner route" 201 "$STATUS"
    continue
  fi
  SEEDED+=("$(field .consent_id)")
done
check "every consent was recorded" "$SEED_COUNT" "${#SEEDED[@]}"
if [[ "${#SEEDED[@]}" -lt 4 ]]; then
  echo "== HU-W4.2-01 list-cloud: not enough consents to go on" >&2
  exit 1
fi
MOBILE="${SEEDED[0]}"   # of the insured, revoked as from the app
PORTAL="${SEEDED[2]}"   # revoked from the portal
DETAIL="${SEEDED[3]}"
NEWEST="${SEEDED[${#SEEDED[@]}-1]}"
MINE="q=$RUN_ID"

echo "== CA-1: a paged table with the holder, the id, the grant, the expiry, the status and the scope"
call GET "/v1/audit/consents?$MINE&page_size=6" "$OPS_TOKEN"
check "the analyst lists (200)" 200 "$STATUS"
capture cloud-list-page-1
check "the consents of the run, 6 per page" "$SEED_COUNT|6|$(((SEED_COUNT + 5) / 6))"   "$(field '"\(.total_items)|\(.items | length)|\(.total_pages)"')"
check "most recently granted first" "$NEWEST" "$(field '.items[0].consent_id')"
check "every row has holder, id, grant, expiry, status and scope" true \
  "$(field '[.items[] | (.holder.full_name != null) and (.holder.document_masked | test("^\\*+[0-9A-Za-z]{4}$")) and (.consent_id != null)
    and (.granted_at != null) and (.expires_at != null) and (.status == "active") and ((.scope | length) == 3)] | all')"
check "the order is by grant, newest first" true "$(field '[.items[].granted_at] | . == (sort | reverse)')"
check "no row carries the full document" 0 "$(grep -c -- "$INSURED_DOCUMENT" "$WORK/body" || true)"

echo "== CA-2: the filter by status and by days, and the pages keep it"
call GET "/v1/audit/consents?$MINE&status=active&granted_from=$TODAY&granted_to=$TODAY&page_size=6&page=2" "$OPS_TOKEN"
check "page 2 of the active ones granted today (200)" 200 "$STATUS"
capture cloud-list-filtered-page-2
check "page 2 keeps the filter: the total and only active rows" "$SEED_COUNT|true|2" \
  "$(field '"\(.total_items)|\([.items[].status] | all(. == "active"))|\(.page)"')"
call GET "/v1/audit/consents?$MINE&granted_to=2000-01-01" "$OPS_TOKEN"
check "a range of days with none of them" "200|0" "$STATUS|$(field .total_items)"

echo "== CA-3: an empty table with no error"
call GET "/v1/audit/consents?$MINE&status=expired" "$OPS_TOKEN"
check "no expired consent of the run: 200, no rows, page 1 of 1" "200|0|0|1" \
  "$STATUS|$(field '"\(.items | length)|\(.total_items)|\(.total_pages)"')"
capture cloud-list-empty

echo "== CA-4: the detail"
call GET "/v1/audit/consents/$DETAIL/revocation" "$OPS_TOKEN"
check "the detail (200)" 200 "$STATUS"
check "grant, expiry, purpose, channel, terms, scope, entity and history" "true" \
  "$(field '(.granted_at != null) and (.expires_at != null) and (.ended_at == null) and (.purpose != "") and (.grant_channel == "embedded")
    and (.terms_version == "v1") and ((.scope | length) == 3) and ((.entities | length) == 1)
    and (.history[0].status == "granted") and (.history[0].channel == "embedded") and (.history[0].occurred_at != null)')"
capture cloud-state

echo "== CA-5: a revocation from the app and one from the portal show in the next query"
call GET "/v1/audit/consents?$MINE&page_size=6" "$OPS_TOKEN"
APP_BEFORE="$(field .indicators.revoked_last_30_days.mobile_app)"
PORTAL_BEFORE="$(field .indicators.revoked_last_30_days.compliance_portal)"
# requested_at has whole seconds and may not be before the grant.
sleep 1
call POST "/v1/consents/$MOBILE/revocations" "$INSURED_TOKEN" \
  "{\"requested_at\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"device\":\"list-cloud.sh\",\"confirmation_method\":\"biometric\"}" "$(uuid)"
check "the insured revokes with the token of the app (201)" 201 "$STATUS"
call POST "/v1/audit/consents/$PORTAL/revocations" "$OPS_TOKEN" \
  '{"reason":"customer_request","reference":"LIST-CLOUD-1","role_confirmed":true}' "$(uuid)"
check "the portal revokes (201)" 201 "$STATUS"
call GET "/v1/audit/consents?$MINE&status=revoked&page_size=50" "$OPS_TOKEN"
REVOKED_NOW="$BODY"
if [[ "$(field .total_items)" != 2 ]]; then
  # The notice may have left one partially revoked already: both are revoked.
  call GET "/v1/audit/consents?$MINE&page_size=50" "$OPS_TOKEN"
  REVOKED_NOW="$(jq '{items: [.items[] | select(.revocation != null)], indicators}' <<<"$BODY")"
fi
BODY="$REVOKED_NOW"
check "both are revoked in the next query" "$(printf '%s\n' "$MOBILE" "$PORTAL" | sort | tr '\n' ' ')" \
  "$(field '[.items[].consent_id] | sort | join(" ")') "
check "with their channels" "compliance_portal mobile_app" "$(field '[.items[].revocation.channel] | sort | join(" ")')"
call GET "/v1/audit/consents?$MINE&page_size=6" "$OPS_TOKEN"
capture cloud-list-after-revocations
check "the indicator of the app went up" true "$(yes_no test "$(field .indicators.revoked_last_30_days.mobile_app)" -gt "$APP_BEFORE")"
check "the indicator of the portal went up" true "$(yes_no test "$(field .indicators.revoked_last_30_days.compliance_portal)" -gt "$PORTAL_BEFORE")"
call GET "/v1/audit/consents/$MOBILE/revocation" "$OPS_TOKEN"
check "the detail shows the revocation of the app with its channel" "mobile_app|mobile_app" \
  "$(field '"\(.revocation.channel)|\([.history[] | select(.status == "revoked")][0].channel)"')"

echo "== CA-6: only the operations role"
call GET "/v1/audit/consents?$MINE" ""
check "without a token: 401 (the gateway or the service)" 401 "$STATUS"
call GET "/v1/audit/consents?$MINE" "$ADVISOR_TOKEN"
check "the advisor: 403" 403 "$STATUS"
call GET "/v1/audit/consents?$MINE" "$INSURED_TOKEN"
check "the insured: 403" 403 "$STATUS"
check "the refusals carry no consent" 0 "$(grep -c -- "$RUN_ID" "$WORK/body" || true)"

echo
if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-W4.2-01 list-cloud: $FAILS check(s) failed" >&2
  exit 1
fi
echo "== HU-W4.2-01 list-cloud passed."
