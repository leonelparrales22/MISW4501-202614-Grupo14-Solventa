#!/usr/bin/env bash
# Runs the HU-W4.2-01 end-to-end scenarios with Docker, against the real audit
# service (its PostgreSQL, the Open Finance stub behind Toxiproxy for the
# notices and Moto as Cognito): the compliance portal lists every consent of
# Open Finance with filters, pages and indicators.
#
#   L1   CA-1  13 consents granted through the partner route and one expired
#              (granted 13 months ago, by SQL): the list as operations has
#              the holder, the id, the grant, the expiry, the status and the
#              scope, 6 per page, 3 pages, newest first
#   L2   CA-2  status, days of grant, both, page 2 with the same filter, the
#              entity, and the search by name without accents, by the full
#              document and by id
#   L3   CA-3  a filter with nothing: 200, no items, page 1 of 1
#   L4   CA-4  the state of the portal has the grant, the expiry, the purpose,
#              the channel, the scope, the entities and the history with times
#   L5   CA-5  a revocation of the insured (mobile_app) and one of the portal
#              (compliance_portal): the next list and the state show them and
#              the indicators go up by one per channel
#   L6   CA-6  no token 401; advisor and insured 403; no row is written
#   L7         minimization: neither the responses nor the logs of audit carry
#              the full document or a token; the search by the full document
#              still finds it
#
# At the end it rehearses list-cloud.sh (the checks of the cloud window)
# against this environment. The script exits with a non-zero code when any
# check fails. The environment is always removed at the end (down -v).
# Requirements: Docker (compose v2), curl, jq and the AWS CLI (only to seed
# the local Moto). The secrets (PostgreSQL, Moto users) are random, generated
# here and never printed. It shares docker-compose.yml with run.sh under its
# own project name and ports, so both can run one after the other.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl jq aws; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done

# MSYS_NO_PATHCONV keeps Git Bash from rewriting container paths.
COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -p hu-w4-2-01-list -f docker-compose.yml"
export AUDIT_PORT="${AUDIT_PORT:-8496}" ORIGINATION_PORT="${ORIGINATION_PORT:-8497}" MOTO_PORT="${MOTO_PORT:-5141}"
export OPENFINANCE_PORT="${OPENFINANCE_PORT:-9496}" TOXIPROXY_PORT="${TOXIPROXY_PORT:-8498}"
export REVOCATION_DEADLINE_SECONDS="${REVOCATION_DEADLINE_SECONDS:-5}"
AUDIT="http://localhost:$AUDIT_PORT"
RUN_ID="l$(date +%s)"

secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
export PG_PASSWORD MOTO_USER_PASSWORD

WORK="$(mktemp -d)"
cleanup() {
  $COMPOSE down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

stamp() { date -u +%H:%M:%S; }
FAILS=0
check() { # <description> <expected> <actual>
  if [[ "$2" == "$3" ]]; then
    echo "  ok    $1"
  else
    echo "  FAIL  $1 (expected '$2', got '$3')" >&2
    FAILS=$((FAILS + 1))
  fi
}

# req <method> <url> <token> [json body] [idempotency key]: sets STATUS and
# BODY; every response is kept for the minimization check (L7).
req() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -X "$1" "$2" -H 'User-Agent: hu-w4.2-01-e2e (Macintosh)')
  [[ -n "${3:-}" ]] && args+=(-H "Authorization: Bearer $3")
  [[ -n "${5:-}" ]] && args+=(-H "Idempotency-Key: $5")
  if [[ -n "${4:-}" ]]; then
    # The body goes through stdin: on Windows the arguments of curl lose their accents.
    STATUS="$(printf '%s' "$4" | curl "${args[@]}" -H 'Content-Type: application/json' --data-binary @-)"
  else
    STATUS="$(curl "${args[@]}")"
  fi
  BODY="$(cat "$WORK/body")"
  printf '%s\n' "$BODY" >>"$WORK/responses"
}
field() { jq -r "$1" <<<"$BODY"; }
list() { req GET "$AUDIT/v1/audit/consents?$1" "$OPS_TOKEN"; } # <query>

# capture <name>: with CAPTURE_DIR set, keeps the last response as <name>.json
# (the examples of docs/contratos/HU-W4.2-01-api.md come from here).
capture() {
  [[ -n "${CAPTURE_DIR:-}" ]] || return 0
  mkdir -p "$CAPTURE_DIR"
  jq . "$WORK/body" >"$CAPTURE_DIR/$1.json" 2>/dev/null || cp "$WORK/body" "$CAPTURE_DIR/$1.json"
}

wait_healthy() { # <url>
  local i
  for i in $(seq 1 90); do
    curl -sf "$1/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "$1 did not become healthy" >&2
  $COMPOSE logs audit | tail -40 >&2
  return 1
}

psql_audit() { $COMPOSE exec -T postgres-audit psql -U audit -d audit -v ON_ERROR_STOP=1 -tA "$@" 2>&1 | tr -d '\r' || true; }

uuid() { # a random UUID (version 4), the Idempotency-Key the portal generates
  local hex
  hex="$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  echo "${hex:0:8}-${hex:8:4}-4${hex:13:3}-a${hex:17:3}-${hex:20:12}"
}

# grant <n> <document> <name> <partner>: records a consent through the
# partner route and appends its id to GRANTED.
GRANTED=()
grant() {
  local body
  # printf, not jq --arg: the values (accents included) reach curl as they
  # are, whatever the encoding of the command line of the system.
  body="$(printf '{"customer_id":"cust-%s-%s","document_id":"%s","full_name":"%s","terms_version":"v1",%s,"partner_id":"%s","application_id":"app-%s-%s"}' \
    "$RUN_ID" "$1" "$2" "$3" '"accepted_sources":["open_finance","open_data","kyc"],"accepted_data_policy":true,"channel":"embedded"' \
    "$4" "$RUN_ID" "$1")"
  req POST "$AUDIT/v1/audit/consents" "" "$body"
  [[ "$STATUS" == 201 ]] || check "consent $1 recorded through the partner route" 201 "$STATUS"
  GRANTED+=("$(field .consent_id)")
}

DOC_A="1020304050"
NAMES=("Ana María Pérez" "José Ñuñez" "Luis Gómez" "Sofía Ríos")

echo "== [$(stamp)] Starting PostgreSQL and Moto"
$COMPOSE up -d --build --wait postgres-audit moto

echo "== [$(stamp)] Seeding the local Cognito (Moto)"
SEED_OUT=""
for attempt in 1 2 3 4 5; do
  if SEED_OUT="$(env -u MSYS_NO_PATHCONV MOTO_URL="http://localhost:$MOTO_PORT" bash ../../security/ha-s01/local/seed.sh 2>&1 | tr -d '\r')"; then
    break
  fi
  if [[ "$attempt" == 5 ]]; then
    echo "$SEED_OUT" >&2
    exit 1
  fi
  sleep 2
done
AUTH_ISSUER="$(sed -n 's/^  export AUTH_ISSUER=//p' <<<"$SEED_OUT")"
AUTH_CLIENT_IDS="$(sed -n 's/^  export AUTH_CLIENT_IDS=//p' <<<"$SEED_OUT")"
WEB_CLIENT_ID="${AUTH_CLIENT_IDS%%,*}"
AUTH_JWKS_URL="http://moto:5000/${AUTH_ISSUER##*/}/.well-known/jwks.json"
export AUTH_ISSUER AUTH_CLIENT_IDS AUTH_JWKS_URL
if [[ -z "$AUTH_ISSUER" || -z "$WEB_CLIENT_ID" ]]; then
  echo "Could not read the pool and the client from seed.sh" >&2
  exit 1
fi
token() { # <username>
  curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
    -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$WEB_CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"$1\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken'
}
subject() { # the "sub" claim of a token (base64url payload)
  local payload
  payload="$(cut -d. -f2 <<<"$1" | tr '_-' '/+')"
  while ((${#payload} % 4)); do payload="$payload="; done
  { base64 -d <<<"$payload" 2>/dev/null || base64 -D <<<"$payload"; } | jq -r .sub
}
OPS_TOKEN="$(token operaciones.local@example.com)"
ADVISOR_TOKEN="$(token asesor.local@example.com)"
INSURED_TOKEN="$(token asegurado.local@example.com)"
INSURED_SUB="$(subject "$INSURED_TOKEN")"

echo "== [$(stamp)] Starting audit"
$COMPOSE up -d --build audit
wait_healthy "$AUDIT"
$COMPOSE logs audit | grep -E "compliance list|compliance revocation" || true

echo "== [$(stamp)] The insured is linked to their document (test data by SQL, as run.sh does)"
psql_audit -q -c "INSERT INTO holder_link (subject, document_id, customer_id, linked_at, event_id)
  VALUES ('$INSURED_SUB', '$DOC_A', 'cus-list-$RUN_ID', now(), 'customer-linked-list-$RUN_ID')" >/dev/null

echo "== [$(stamp)] 13 consents through the partner route and one expired"
for i in $(seq 0 12); do
  document="$(printf '70000%05d' "$i")"
  [[ "$i" -lt 2 ]] && document="$DOC_A"
  partner="aliado-banco-1"
  ((i % 4 == 3)) && partner="aliado-coop"
  grant "$i" "$document" "${NAMES[$((i % 4))]}" "$partner"
done
# An expired consent: granted 13 months ago (the validity is 12). Only an
# insertion, as the append-only tables of audit demand.
psql_audit -q -c "INSERT INTO consent_record (consent_id, customer_id, holder_document_id, holder_full_name, terms_version, scope,
  purpose, channel, partner_id, application_id, user_agent, granted_at)
  SELECT 'cns-old-$RUN_ID', 'cust-old-$RUN_ID', '5566778899', 'Elena Vencida', terms_version, scope, purpose, channel,
    partner_id, 'app-old-$RUN_ID', 'seed', now() - interval '13 months' FROM consent_record WHERE consent_id = '${GRANTED[0]}'" >/dev/null
EXPIRED="cns-old-$RUN_ID"
NEWEST="${GRANTED[12]}"
TODAY="$(psql_audit -c "SELECT to_char(now() AT TIME ZONE 'America/Bogota', 'YYYY-MM-DD')")"
EXPIRED_DAY="$(psql_audit -c "SELECT to_char(granted_at AT TIME ZONE 'America/Bogota', 'YYYY-MM-DD') FROM consent_record WHERE consent_id = '$EXPIRED'")"

echo "== [$(stamp)] L1. CA-1: a paged table with the holder, the id, the grant, the expiry, the status and the scope"
list "page_size=6"
check "the analyst lists (200)" 200 "$STATUS"
capture list-page-1
check "14 consents, 6 per page, 3 pages" "14|6|3|1" "$(field '"\(.total_items)|\(.items | length)|\(.total_pages)|\(.page)"')"
check "most recently granted first" "$NEWEST" "$(field '.items[0].consent_id')"
check "the order is by grant, newest first" true "$(field '[.items[].granted_at] | . == (sort | reverse)')"
check "the first row: holder with the masked document, grant, expiry, status, channel and scope" \
  "Ana María Pérez|******0012|active|embedded|3|aliado-banco-1" \
  "$(field '.items[0] | "\(.holder.full_name)|\(.holder.document_masked)|\(.status)|\(.grant_channel)|\(.scope | length)|\(.entities[0].code)"')"
check "every row has its expiry 12 months after the grant" true \
  "$(field '[.items[] | ((.expires_at | sub("\\.[0-9]+";"") | fromdate) - (.granted_at | sub("\\.[0-9]+";"") | fromdate)) >= 365*86400] | all')"
check "the indicators" "13|1|0|0" \
  "$(field '.indicators | "\(.active)|\(.expired)|\(.revoked_last_30_days.total)|\(.revocations_out_of_deadline)"')"
check "the entities of the filter, by name" "aliado-coop:aliado-coop,aliado-banco-1:Banco Aliado" \
  "$(field '[.entities[] | "\(.code):\(.name)"] | sort | reverse | join(",")')"
list "page_size=6&page=3"
check "page 3 has the last two, the expired one at the end" "2|$EXPIRED|expired" \
  "$(field '"\(.items | length)|\(.items[-1].consent_id)|\(.items[-1].status)"')"

echo "== [$(stamp)] L2. CA-2: the filters by status and days, both, and the pages keep them"
list "status=expired"
check "status=expired: only the expired one" "1|$EXPIRED" "$(field '"\(.total_items)|\(.items[0].consent_id)"')"
list "granted_from=$EXPIRED_DAY&granted_to=$EXPIRED_DAY"
check "the day of the expired one: only it" "1|$EXPIRED" "$(field '"\(.total_items)|\(.items[0].consent_id)"')"
list "status=active&granted_from=$TODAY&granted_to=$TODAY&page_size=6"
check "active and granted today: 13 in 3 pages" "13|3|6" "$(field '"\(.total_items)|\(.total_pages)|\(.items | length)"')"
PAGE1="$(field '[.items[].consent_id] | join(",")')"
list "status=active&granted_from=$TODAY&granted_to=$TODAY&page_size=6&page=2"
capture list-filtered-page-2
check "page 2 keeps the filter: the same total, only active ones, other rows" "13|true|2|true" \
  "$(jq -r --arg p "$PAGE1" '"\(.total_items)|\([.items[].status] | all(. == "active"))|\(.page)|\([.items[].consent_id] | all(. as $id | ($p | split(",") | index($id)) == null))"' <<<"$BODY")"
list "status=expired&granted_from=$TODAY&granted_to=$TODAY"
check "expired and granted today: none" 0 "$(field .total_items)"
list "entity=aliado-coop&page_size=50"
check "the entity aliado-coop: 3 of them" 3 "$(field .total_items)"
list "q=nunez&page_size=50"
check "the name without capitals or accents (José Ñuñez)" "3|José Ñuñez" \
  "$(field '"\(.total_items)|\([.items[].holder.full_name] | unique | join(","))"')"
list "q=$DOC_A"
check "the full document finds the two of the insured" 2 "$(field .total_items)"
capture list-search-document
list "q=1.020.304.050"
check "also written with dots" 2 "$(field .total_items)"
list "q=102030"
check "never a prefix of a document" 0 "$(field .total_items)"
list "q=${GRANTED[5]}"
check "the id finds its consent" "1|${GRANTED[5]}" "$(field '"\(.total_items)|\(.items[0].consent_id)"')"
req GET "$AUDIT/v1/audit/consents?status=pending&granted_from=$TODAY&granted_to=2000-01-01&page=0&page_size=51" "$OPS_TOKEN"
check "invalid parameters: 400 naming all of them" "400|status:invalid_format,granted_to:out_of_range,page:out_of_range,page_size:out_of_range" \
  "$STATUS|$(field '[.fields[] | "\(.field):\(.code)"] | join(",")')"
capture list-400

echo "== [$(stamp)] L3. CA-3: a filter with nothing"
list "status=revoked&entity=aliado-coop"
check "200, no items, page 1 of 1" "200|0|0|1" "$STATUS|$(field '"\(.items | length)|\(.total_items)|\(.total_pages)"')"
check "the indicators do not depend on the filter" "13|1" "$(field '.indicators | "\(.active)|\(.expired)"')"
capture list-empty

echo "== [$(stamp)] L4. CA-4: the detail of a consent"
req GET "$AUDIT/v1/audit/consents/${GRANTED[6]}/revocation" "$OPS_TOKEN"
check "the state of the portal (200)" 200 "$STATUS"
check "grant, expiry, end, purpose, channel and terms" "true|true|null|risk_profiling_mortgage_life_offer|embedded|v1" \
  "$(field '"\(.granted_at != null)|\(.expires_at != null)|\(.ended_at)|\(.purpose)|\(.grant_channel)|\(.terms_version)"')"
check "the scope, the entity and the history with its time and channel" "3|aliado-banco-1|granted|embedded|true" \
  "$(field '"\(.scope | length)|\(.entities[0].code)|\(.history[0].status)|\(.history[0].channel)|\(.history[0].occurred_at == .granted_at)"')"
capture state-detail

echo "== [$(stamp)] L5. CA-5: a revocation from the app and one from the portal, in the next query"
sleep 1 # requested_at has whole seconds and may not be before the grant
req POST "$AUDIT/v1/consents/${GRANTED[0]}/revocations" "$INSURED_TOKEN" \
  "{\"requested_at\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"device\":\"e2e Pixel 8\",\"confirmation_method\":\"biometric\"}" "$(uuid)"
check "the insured revokes from the app (201)" 201 "$STATUS"
req POST "$AUDIT/v1/audit/consents/${GRANTED[4]}/revocations" "$OPS_TOKEN" \
  '{"reason":"customer_request","reference":"TCK-LIST-1","role_confirmed":true}' "$(uuid)"
check "the portal revokes (201)" 201 "$STATUS"
list "status=revoked&page_size=50"
check "the next list shows both revoked" "$(printf '%s\n' "${GRANTED[0]}" "${GRANTED[4]}" | sort | paste -sd, -)" \
  "$(field '[.items[].consent_id] | sort | join(",")')"
check "with their channels" "compliance_portal,mobile_app" "$(field '[.items[].revocation.channel] | sort | join(",")')"
check "the indicators: one more per channel" "2|1|1|11" \
  "$(field '.indicators | "\(.revoked_last_30_days.total)|\(.revoked_last_30_days.mobile_app)|\(.revoked_last_30_days.compliance_portal)|\(.active)"')"
capture list-after-revocations
req GET "$AUDIT/v1/audit/consents/${GRANTED[0]}/revocation" "$OPS_TOKEN"
check "the state shows the revocation of the app and its channel in the history" "revoked|mobile_app|mobile_app" \
  "$(field '"\(.status)|\(.revocation.channel)|\([.history[] | select(.status == "revoked")][0].channel)"')"

echo "== [$(stamp)] L6. CA-6: only the operations role; reading writes nothing"
ROWS="SELECT (SELECT count(*) FROM consent_record) || '|' || (SELECT count(*) FROM consent_event) || '|' ||
  (SELECT count(*) FROM consent_revocation) || '|' || (SELECT count(*) FROM revocation_notice) || '|' || (SELECT count(*) FROM holder_link)"
# The notices of L5 are still being written by the worker: wait until they settle.
sleep 2
ROWS_BEFORE="$(psql_audit -c "$ROWS")"
for who in none advisor insured; do
  case "$who" in
  none) tok="" want=401 ;;
  advisor) tok="$ADVISOR_TOKEN" want=403 ;;
  insured) tok="$INSURED_TOKEN" want=403 ;;
  esac
  req GET "$AUDIT/v1/audit/consents?status=active" "$tok"
  check "$who: GET /v1/audit/consents is $want" "$want" "$STATUS"
done
for q in "" "status=expired" "q=maria" "page=9"; do list "$q"; done
check "no row was written by the list" "$ROWS_BEFORE" "$(psql_audit -c "$ROWS")"

echo "== [$(stamp)] Rehearsal of list-cloud.sh against this environment"
if PORTAL_URL="$AUDIT" COGNITO_CLIENT_ID="$WEB_CLIENT_ID" COGNITO_ENDPOINT="http://localhost:$MOTO_PORT/" \
  OPS_EMAIL=operaciones.local@example.com OPS_PASSWORD="$MOTO_USER_PASSWORD" \
  ADVISOR_EMAIL=asesor.local@example.com ADVISOR_PASSWORD="$MOTO_USER_PASSWORD" \
  INSURED_EMAIL=asegurado.local@example.com INSURED_PASSWORD="$MOTO_USER_PASSWORD" INSURED_DOCUMENT="$DOC_A" \
  bash ./list-cloud.sh; then
  check "list-cloud.sh passes against the local environment" passed passed
else
  check "list-cloud.sh passes against the local environment" passed failed
fi

echo "== [$(stamp)] L7. Minimization: no full document, no token in the responses nor in the logs of audit"
$COMPOSE logs --no-log-prefix audit >"$WORK/audit.log" 2>&1 || true
check "no response carries the full document (even the search by it)" 0 "$(grep -c -- "$DOC_A" "$WORK/responses" || true)"
check "the audit logs never carry the document" 0 "$(grep -c -- "$DOC_A" "$WORK/audit.log" || true)"
check "the audit logs never carry a token" 0 "$(grep -c -- "$OPS_TOKEN" "$WORK/audit.log" || true)"
check "the access log of the list has the route, never its query" 0 "$(grep -c -- 'q=' "$WORK/audit.log" || true)"

echo
if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-W4.2-01 e2e: $FAILS check(s) failed" >&2
  exit 1
fi
echo "== HU-W4.2-01 e2e passed."
