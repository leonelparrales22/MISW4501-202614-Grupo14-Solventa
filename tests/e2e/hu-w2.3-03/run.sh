#!/usr/bin/env bash
# Runs the HU-W2.3-03 end-to-end scenarios with Docker, against the real claims
# service (its PostgreSQL, the providers it seeds, the worker of the notices,
# the e-mail stub behind Toxiproxy, the policy core stub and Moto as Cognito),
# as the app of the insured and the portal of operations would call it:
#
#   1. CA-1  the insured requests with the location of the phone (A) and without
#            it (B, no_fix); both are in the inbox in less than 60 s with the
#            policy, the time and the status of the location, and coordinates
#            only in A; the indicators count 2 new
#   2. CA-2  A: attend, need towing, the nearest tow truck is suggested with its
#            distance and arrival; B: another analyst takes it over, the
#            address and the pin given by voice, the tow truck of Palmira
#   3. CA-3  A is dispatched: dispatched with its time and provider, and the
#            provider receives one e-mail with the number and the coordinates;
#            B is dispatched with the e-mail provider cut: 200, the notice
#            stays pending and leaves once it is back
#   4. CA-4  C is closed resolved_by_phone with no dispatch nor e-mail; other
#            without a note is 422; a dispatched request cannot be closed; of
#            two analysts closing D at once only one wins
#   5. CA-5  the history of each request carries the analyst and the time of
#            every decision; UPDATE, DELETE and TRUNCATE on the append-only
#            tables fail and what the app sent is not rewritten
#   6.       insured and advisor get 403 in the inbox; the insured reads
#            dispatched in its own request, without the provider; the logs of
#            claims carry no coordinates, addresses, names nor e-mails
#
# With CAPTURE_DIR set, the answers are also written there: they are the
# examples of docs/contratos/HU-W2.3-03-api.md.
#
# The script exits with a non-zero code when any check fails. The environment
# is always removed at the end (down -v). Requirements: Docker (compose v2),
# curl, jq and the AWS CLI (only to seed the local Moto, as
# tests/security/ha-s01/local does). The secrets (PostgreSQL, Moto users) are
# random, generated here and never printed.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl jq aws; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done

# MSYS_NO_PATHCONV keeps Git Bash from rewriting container paths.
COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -f docker-compose.yml"
export CLAIMS_PORT="${CLAIMS_PORT:-8304}" POLICY_CORE_PORT="${POLICY_CORE_PORT:-9164}"
export EMAIL_PORT="${EMAIL_PORT:-9134}" TOXIPROXY_PORT="${TOXIPROXY_PORT:-8504}" MOTO_PORT="${MOTO_PORT:-5128}"
CLAIMS="http://localhost:$CLAIMS_PORT"
INBOX="$CLAIMS/v1/claims/assistance-inbox"
MAILBOX="http://localhost:$EMAIL_PORT/messages"
RUN_ID="r$(date +%s)"

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
CHECKS=0
check() { # <description> <expected> <actual>
  CHECKS=$((CHECKS + 1))
  if [[ "$2" == "$3" ]]; then
    echo "  ok    $1"
  else
    echo "  FAIL  $1 (expected '$2', got '$3')" >&2
    FAILS=$((FAILS + 1))
  fi
}
yes_if() { if eval "$1"; then echo yes; else echo no; fi; }

# call <method> <url> [json body] [bearer token] [idempotency key]: sets
# STATUS, BODY and HEADERS.
call() {
  local args=(-s -o "$WORK/body" -D "$WORK/headers" -w '%{http_code}' -X "$1" "$2")
  [[ -n "${3:-}" ]] && args+=(-H 'Content-Type: application/json' -d "$3")
  [[ -n "${4:-}" ]] && args+=(-H "Authorization: Bearer $4")
  [[ -n "${5:-}" ]] && args+=(-H "Idempotency-Key: $5")
  STATUS="$(curl "${args[@]}")"
  BODY="$(cat "$WORK/body")"
  HEADERS="$(tr -d '\r' <"$WORK/headers")"
}
field() { jq -r "$1" <<<"$BODY"; }
# capture <name>: keeps the last answer in CAPTURE_DIR, when it is set.
capture() {
  [[ -n "${CAPTURE_DIR:-}" ]] || return 0
  mkdir -p "$CAPTURE_DIR"
  jq . <<<"$BODY" >"$CAPTURE_DIR/$1.json"
}

psql_claims() { $COMPOSE exec -T postgres-claims psql -U claims -d claims -tAc "$1"; }

wait_healthy() {
  local i
  for i in $(seq 1 90); do
    curl -sf "$CLAIMS/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "claims did not become healthy" >&2
  $COMPOSE logs claims | tail -40 >&2
  return 1
}

jwt_sub() {
  local payload
  payload="$(cut -d. -f2 <<<"$1" | tr '_-' '/+')"
  while (( ${#payload} % 4 )); do payload+="="; done
  base64 -d <<<"$payload" 2>/dev/null | jq -r .sub
}

# The bodies the app sends (docs/contratos/HU-M2.3-01-api.md).
LATITUDE=4.7712
LONGITUDE=-74.0452
PLACE="Autopista Norte km 12, Bogotá"
# What the insured without GPS says on the phone, and where the analyst puts
# the pin (docs/contratos/HU-W2.3-03-api.md).
VOICE_ADDRESS="Vía Cali–Palmira km 4"
VOICE_LATITUDE=3.4921
VOICE_LONGITUDE=-76.4203
with_fix() { # <policy> <captured_at>
  jq -nc --arg p "$1" --arg c "$2" --arg place "$PLACE" --argjson lat "$LATITUDE" --argjson lon "$LONGITUDE" \
    '{policy_id:$p, location:{status:"sent", latitude:$lat, longitude:$lon, accuracy_m:8, captured_at:$c}, place:$place}'
}
without_fix() { # <policy> <reason>
  jq -nc --arg p "$1" --arg r "$2" '{policy_id:$p, location:{status:"unavailable", unavailable_reason:$r}}'
}
request() { # <body> <key>: sets ID and NUMBER
  call POST "$CLAIMS/v1/claims/assistance-requests" "$1" "$INSURED_TOKEN" "$2"
  ID="$(field .request_id)"
  NUMBER="$(field .request_number)"
}

# The portal of operations.
inbox() { call GET "$INBOX${1:-}" "" "${2:-$OPS_TOKEN}"; }
detail() { call GET "$INBOX/$1" "" "${2:-$OPS_TOKEN}"; }
act() { # <request id> <action> [body] [token]
  call POST "$INBOX/$1/$2" "${3:-}" "${4:-$OPS_TOKEN}"
}

# wait_for <seconds> <request id> <jq condition>: re-reads the detail until the
# condition holds; sets WAITED (seconds, or "timeout").
wait_for() {
  local i
  for i in $(seq 1 "$1"); do
    detail "$2"
    if [[ "$STATUS" == 200 && "$(field "$3")" == true ]]; then
      WAITED="$i"
      return 0
    fi
    sleep 1
  done
  WAITED=timeout
}

# mailbox [recipient]: what the e-mail stub received (for the recipient).
mailbox() { curl -sf "$MAILBOX${1:+?to=$1}"; }
mails() { mailbox "${1:-}" | jq '.messages | length'; }

email_proxy() { # <true|false>
  curl -sf -X POST "http://localhost:$TOXIPROXY_PORT/proxies/email" -d "{\"enabled\":$1}" >/dev/null
}

epoch_of() { jq -rn --arg t "$1" '$t | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601'; }

echo "== [$(stamp)] Starting PostgreSQL, the policy core stub, the e-mail stub behind Toxiproxy and Moto"
$COMPOSE up -d --build --wait postgres-claims policy-core email toxiproxy moto
$COMPOSE up toxiproxy-init >/dev/null

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
POOL_ID="${AUTH_ISSUER##*/}"
AUTH_JWKS_URL="http://moto:5000/$POOL_ID/.well-known/jwks.json"
export AUTH_ISSUER AUTH_CLIENT_IDS AUTH_JWKS_URL
if [[ -z "$AUTH_ISSUER" || -z "$WEB_CLIENT_ID" ]]; then
  echo "Could not read the pool and the client from seed.sh" >&2
  exit 1
fi
idp() {
  env -u MSYS_NO_PATHCONV AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION="${POOL_ID%%_*}" AWS_PAGER="" \
    aws --endpoint-url "http://localhost:$MOTO_PORT" cognito-idp "$@" >/dev/null
}
# A second analyst of operations, who takes over a request in call.
OPS_USER="operaciones.local@example.com"
OTHER_OPS_USER="operaciones.turno2.local@example.com"
idp admin-create-user --user-pool-id "$POOL_ID" --username "$OTHER_OPS_USER" --message-action SUPPRESS \
  --user-attributes Name=email,Value="$OTHER_OPS_USER" Name=email_verified,Value=true Name=name,Value="Diana Torres"
idp admin-set-user-password --user-pool-id "$POOL_ID" --username "$OTHER_OPS_USER" --password "$MOTO_USER_PASSWORD" --permanent
idp admin-add-user-to-group --user-pool-id "$POOL_ID" --username "$OTHER_OPS_USER" --group-name operations
token() { # <username>
  curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
    -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$WEB_CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"$1\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken'
}
INSURED_TOKEN="$(token asegurado.local@example.com)"
ADVISOR_TOKEN="$(token asesor.local@example.com)"
OPS_TOKEN="$(token "$OPS_USER")"
OTHER_OPS_TOKEN="$(token "$OTHER_OPS_USER")"
INSURED_SUB="$(jwt_sub "$INSURED_TOKEN")"

echo "== [$(stamp)] Starting claims (EMAIL_URL through Toxiproxy)"
$COMPOSE up -d --build claims
wait_healthy
$COMPOSE logs claims | grep -E "assistance|database migration" || true

# What the policy core knows of the insured.
CORE="$(curl -sf "http://localhost:$POLICY_CORE_PORT/holders/$INSURED_SUB/policies")"
AUTO="$(jq -r '.policies[] | select(.line=="auto" and .status=="active") | .policy_id' <<<"$CORE")"
HOME_POLICY="$(jq -r '.policies[] | select(.line=="home" and .status=="active") | .policy_id' <<<"$CORE")"
# Six seconds ago, as a fresh fix of the phone. Date arithmetic with jq: the
# same on the date of macOS (BSD) and of Linux (GNU).
CAPTURED="$(jq -rn 'now - 6 | floor | todateiso8601')"

call GET "$CLAIMS/v1/claims/assistance-policy" "" "$OPS_TOKEN"
capture 200-policy
check "the policy of the inbox is assistance-policy-v1" "200/assistance-policy-v1/towing" \
  "$STATUS/$(field .version)/$(field '.needs[] | select(.code=="towing") | .specialty')"

echo "== [$(stamp)] 1. CA-1 the requests of the phone reach the inbox"
request "$(with_fix "$AUTO" "$CAPTURED")" "$RUN_ID-a"
check "A (with the location of the phone) is recorded" 201 "$STATUS"
A="$ID" A_NUMBER="$NUMBER"
request "$(without_fix "$AUTO" no_fix)" "$RUN_ID-b"
check "B (no_fix) is recorded" 201 "$STATUS"
B="$ID" B_NUMBER="$NUMBER"
SENT_EPOCH="$(date -u +%s)"
for i in $(seq 1 60); do
  inbox
  [[ "$(field "[.items[].request_id] | index(\"$A\") != null and index(\"$B\") != null")" == true ]] && break
  sleep 1
done
IN_INBOX="$(( $(date -u +%s) - SENT_EPOCH ))"
capture 200-inbox-new
check "both are in the inbox" "200/true" \
  "$STATUS/$(field "[.items[].request_id] | index(\"$A\") != null and index(\"$B\") != null")"
check "in less than 60 s (${IN_INBOX} s)" yes "$(yes_if '(( IN_INBOX < 60 ))')"
check "nothing is cached" "no-store" "$(sed -n 's/^[Cc]ache-[Cc]ontrol: //p' <<<"$HEADERS")"
check "the most recent first" "$B,$A" "$(field '[.items[].request_id] | join(",")')"
ROW_A="$(field ".items[] | select(.request_id==\"$A\")")"
check "A with its policy, status and time" "$AUTO/auto/new/true" \
  "$(jq -r '"\(.policy.policy_id)/\(.policy.line)/\(.status)/\(.requested_at | endswith("Z"))"' <<<"$ROW_A")"
check "A with its coordinates for the map" "sent/$LATITUDE/$LONGITUDE/8/$PLACE" \
  "$(jq -r '"\(.location.status)/\(.location.latitude)/\(.location.longitude)/\(.location.accuracy_m)/\(.location.place)"' <<<"$ROW_A")"
check "and the name of the insured" "Carolina Pérez" "$(jq -r .requester_name <<<"$ROW_A")"
check "B without coordinates, with its reason" "unavailable/no_fix/null/null/null" \
  "$(field ".items[] | select(.request_id==\"$B\") | \"\(.location.status)/\(.location.unavailable_reason)/\(.location.latitude)/\(.location.longitude)/\(.location.voice)\"")"
check "the indicators count 2 new" "2/0/0/null/300" \
  "$(field '"\(.summary.new_count)/\(.summary.in_call_count)/\(.summary.dispatched_today)/\(.summary.mean_dispatch_seconds_today)/\(.summary.dispatch_target_seconds)"')"
inbox "?status=in_call"
check "filtered by status" "200/0" "$STATUS/$(field '.items | length')"
inbox "?status=lost"
check "an unknown status is 400" "400/invalid_request" "$STATUS/$(field .error)"

echo "== [$(stamp)] 2. CA-2 the suggestion of the provider"
detail "$A"
check "opening the detail does not attend it" "200/new/need_missing" "$STATUS/$(field .status)/$(field .suggestion.state)"
act "$A" need '{"need":"towing"}'
check "a request new must be attended first: 409" "409/assistance_not_attended" "$STATUS/$(field .error)"
act "$A" attend
capture 200-attend
check "attend: in_call, handled by the analyst" "200/in_call/$OPS_USER/Andrés Ruiz" \
  "$STATUS/$(field .status)/$(field .handled_by.email)/$(field .handled_by.name)"
act "$A" need '{"need":"plumbing_emergency"}'
check "a need of another line is 422" "422/need_not_for_line" "$STATUS/$(field .error)"
act "$A" dispatch '{"provider_id":"prv-gruas-autopista-norte"}'
check "dispatch without a need is 422" "422/need_missing" "$STATUS/$(field .error)"
act "$A" need '{"need":"towing"}'
capture 200-need
check "the need towing" "200/towing/towing/$OPS_USER" \
  "$STATUS/$(field .need.code)/$(field .need.specialty)/$(field .need.recorded_by.email)"
SUGGESTED="$(field '.suggestion.candidates[] | select(.suggested) | "\(.provider_id)/\(.distance_km)/\(.eta_minutes)/\(.eligibility)"')"
check "the nearest tow truck is suggested, with its distance and arrival" \
  "found/app/5/prv-gruas-autopista-norte/0.9/12/eligible" \
  "$(field '"\(.suggestion.state)/\(.suggestion.location_source)/\(.suggestion.radius_km)"')/$SUGGESTED"
check "every candidate has the specialty" true \
  "$(field '[.suggestion.candidates[] | .specialties | index("towing") != null] | all')"
check "nearest first" true "$(field '[.suggestion.candidates[].distance_km] | . == sort')"
act "$A" voice-location "$(jq -nc --arg a "$VOICE_ADDRESS" --argjson lat "$VOICE_LATITUDE" --argjson lon "$VOICE_LONGITUDE" \
  '{address:$a, latitude:$lat, longitude:$lon}')"
check "A shared its location: no voice location (409)" "409/location_from_app" "$STATUS/$(field .error)"

act "$B" attend
check "B is attended by Andrés" "200/$OPS_USER" "$STATUS/$(field .handled_by.email)"
act "$B" attend "" "$OTHER_OPS_TOKEN"
check "and taken over by Diana" "200/in_call/$OTHER_OPS_USER/Diana Torres" \
  "$STATUS/$(field .status)/$(field .handled_by.email)/$(field .handled_by.name)"
check "without location there is no search" location_missing "$(field .suggestion.state)"
act "$B" need '{"need":"towing"}' "$OTHER_OPS_TOKEN"
check "not even with the need" "200/location_missing" "$STATUS/$(field .suggestion.state)"
act "$B" voice-location '{"address":"km","latitude":91,"longitude":-76.4}' "$OTHER_OPS_TOKEN"
check "an invalid voice location is 422 with every field" "422/validation_failed/address:out_of_range,latitude:out_of_range" \
  "$STATUS/$(field .error)/$(field '[.fields[] | "\(.field):\(.code)"] | sort | join(",")')"
act "$B" voice-location "$(jq -nc --arg a "$VOICE_ADDRESS" --argjson lat "$VOICE_LATITUDE" --argjson lon "$VOICE_LONGITUDE" \
  '{address:$a, latitude:$lat, longitude:$lon}')" "$OTHER_OPS_TOKEN"
capture 200-voice-location
check "the address and the pin given by voice" "200/$VOICE_ADDRESS/$VOICE_LATITUDE/$VOICE_LONGITUDE/$OTHER_OPS_USER" \
  "$STATUS/$(field '"\(.location.voice.address)/\(.location.voice.latitude)/\(.location.voice.longitude)/\(.location.voice.recorded_by.email)"')"
check "the search starts from the pin: the tow truck of Palmira" "found/voice/prv-gruas-del-valle" \
  "$(field '"\(.suggestion.state)/\(.suggestion.location_source)/\(.suggestion.candidates[] | select(.suggested) | .provider_id)"')"
check "with the arrival of its distance" true \
  "$(field '.suggestion.candidates[] | select(.suggested) | .eta_minutes == 10 + ((.distance_km * 2) | ceil) or .eta_minutes == 10 + ((.distance_km * 2) | ceil) + 1')"
inbox
check "the indicators count 2 in call" "0/2" "$(field '"\(.summary.new_count)/\(.summary.in_call_count)"')"

echo "== [$(stamp)] 3. CA-3 the dispatch and the notice to the provider"
act "$A" dispatch '{"provider_id":"prv-gruas-calle-80"}'
check "a provider out of the radius is 422" "422/provider_not_eligible" "$STATUS/$(field .error)"
act "$A" dispatch '{"provider_id":"prv-gruas-autopista-norte"}'
capture 200-dispatch
check "A dispatched" "200/dispatched/prv-gruas-autopista-norte/Grúas Autopista Norte" \
  "$STATUS/$(field .status)/$(field .dispatch.provider.id)/$(field .dispatch.provider.name)"
check "with its time, by the analyst" "true/$OPS_USER/true" \
  "$(field '(.dispatched_at | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T"))')/$(field .dispatch.dispatched_by.email)/$(field '.dispatched_at == .dispatch.dispatched_at')"
check "the location shared with the provider" "app/$LATITUDE/$LONGITUDE/8/0.9/12" \
  "$(field '"\(.dispatch.location_shared.source)/\(.dispatch.location_shared.latitude)/\(.dispatch.location_shared.longitude)/\(.dispatch.location_shared.accuracy_m)/\(.dispatch.distance_km)/\(.dispatch.eta_minutes)"')"
check "no more search" not_applicable "$(field .suggestion.state)"
wait_for 15 "$A" '.dispatch.notice.status == "sent"'
check "the notice is recorded as sent (waited ${WAITED}s)" "sent/1" "$(field '"\(.dispatch.notice.status)/\(.dispatch.notice.attempts)"')"
MAIL="$(mailbox despachos@gruasautopistanorte.example.com)"
check "the provider received one e-mail" 1 "$(jq '.messages | length' <<<"$MAIL")"
check "under the key of the request" "asistencia-$A" "$(jq -r '.messages[0].idempotency_key' <<<"$MAIL")"
check "with the number of the request" "Asistencia $A_NUMBER · Grúa" "$(jq -r '.messages[0].subject' <<<"$MAIL")"
MAIL_BODY="$(jq -r '.messages[0].body' <<<"$MAIL")"
check "and the coordinates with their accuracy" yes \
  "$(yes_if '[[ "$MAIL_BODY" == *"$LATITUDE, $LONGITUDE (precisión 8 m"* && "$MAIL_BODY" == *"$PLACE"* ]]')"
check "without the name of the insured" no "$(yes_if '[[ "$MAIL_BODY" == *Carolina* ]]')"
act "$A" dispatch '{"provider_id":"prv-auxilio-vial-norte"}'
check "a dispatched request is not dispatched again: 409" "409/assistance_not_open" "$STATUS/$(field .error)"

email_proxy false
act "$B" dispatch '{"provider_id":"prv-gruas-del-valle"}' "$OTHER_OPS_TOKEN"
check "with the e-mail provider cut, B is dispatched all the same" "200/dispatched/pending" \
  "$STATUS/$(field .status)/$(field .dispatch.notice.status)"
check "by Diana, from the voice location" "$OTHER_OPS_USER/voice/$VOICE_ADDRESS" \
  "$(field '"\(.dispatch.dispatched_by.email)/\(.dispatch.location_shared.source)/\(.dispatch.location_shared.address)"')"
wait_for 15 "$B" '.dispatch.notice.attempts >= 1'
check "the failed delivery is recorded and stays pending" "pending/true" \
  "$(field .dispatch.notice.status)/$(field '.dispatch.notice.attempts >= 1')"
check "nothing reached the provider" 0 "$(mails despachos@gruasdelvalle.example.com)"
email_proxy true
wait_for 60 "$B" '.dispatch.notice.status == "sent"'
check "once the e-mail provider is back the notice leaves (waited ${WAITED}s)" sent "$(field .dispatch.notice.status)"
MAIL="$(mailbox despachos@gruasdelvalle.example.com)"
check "exactly one e-mail" 1 "$(jq '.messages | length' <<<"$MAIL")"
MAIL_BODY="$(jq -r '.messages[0].body' <<<"$MAIL")"
check "with the voice location" yes \
  "$(yes_if '[[ "$MAIL_BODY" == *"$VOICE_LATITUDE, $VOICE_LONGITUDE (indicada por voz)"* && "$MAIL_BODY" == *"$VOICE_ADDRESS"* ]]')"
inbox
capture 200-inbox-dispatched
check "the indicators count 2 dispatched today, with their mean time" "0/0/2/true" \
  "$(field '"\(.summary.new_count)/\(.summary.in_call_count)/\(.summary.dispatched_today)/\(.summary.mean_dispatch_seconds_today >= 0)"')"

echo "== [$(stamp)] 4. CA-4 closing by other means"
MAILS_BEFORE="$(mails)"
request "$(without_fix "$HOME_POLICY" permission_denied)" "$RUN_ID-c"
check "C (home, no location) is recorded" 201 "$STATUS"
C="$ID"
act "$C" close '{"reason":"other"}'
capture 422-close-other-without-note
check "other without a note is 422" "422/validation_failed/note:required" \
  "$STATUS/$(field .error)/$(field '[.fields[] | "\(.field):\(.code)"] | join(",")')"
act "$C" close '{"reason":"resolved_by_phone"}'
capture 200-close
check "C closed from new, resolved by phone" "200/closed/resolved_by_phone/$OPS_USER/null" \
  "$STATUS/$(field .status)/$(field .closure.reason)/$(field .closure.closed_by.email)/$(field .dispatch)"
check "with its time" true "$(field '.closed_at == .closure.closed_at and (.closed_at | test("T"))')"
act "$C" attend
check "a closed request cannot be attended: 409" "409/assistance_not_open" "$STATUS/$(field .error)"
act "$A" close '{"reason":"duplicate"}'
check "nor can a dispatched one be closed: 409" "409/assistance_not_open" "$STATUS/$(field .error)"
sleep 2
check "closing sends no e-mail" "$MAILS_BEFORE" "$(mails)"
check "nor writes a dispatch" 0 "$(psql_claims "SELECT count(*) FROM assistance_dispatch WHERE request_id = '$C'" | tr -d '[:space:]')"

request "$(without_fix "$AUTO" location_disabled)" "$RUN_ID-d"
D="$ID"
# Both analysts close D at the same time: only one decision is recorded.
close_as() { # <token> <output>
  curl -s -o /dev/null -w '%{http_code}' -X POST "$INBOX/$D/close" -H "Authorization: Bearer $1" \
    -H 'Content-Type: application/json' -d '{"reason":"duplicate"}' >"$2"
}
close_as "$OPS_TOKEN" "$WORK/race1" &
close_as "$OTHER_OPS_TOKEN" "$WORK/race2" &
wait
check "of two analysts closing D at once, one wins and the other gets 409" "200 409" \
  "$(printf '%s\n%s\n' "$(cat "$WORK/race1")" "$(cat "$WORK/race2")" | sort | xargs)"
check "with a single closing decision" 1/1 \
  "$(psql_claims "SELECT (SELECT count(*) FROM assistance_action WHERE request_id = '$D' AND action = 'closed') || '/' ||
    (SELECT count(*) FROM assistance_closure WHERE request_id = '$D')" | tr -d '[:space:]')"

echo "== [$(stamp)] 5. CA-5 the analyst and the time of every decision"
detail "$A"
capture 200-detail-dispatched
check "A: attended, need and dispatch, by Andrés" "attended,need_recorded,dispatched/$OPS_USER" \
  "$(field '[.history[].action] | join(",")')/$(field '[.history[].by.email] | unique | join(",")')"
check "each one with its time, in order" true "$(field '[.history[].at] | . == sort and all(test("T"))')"
check "and its detail" "towing/prv-gruas-autopista-norte" \
  "$(field '"\(.history[1].detail.need)/\(.history[2].detail.provider_id)"')"
detail "$B"
check "B: attended by Andrés, then Diana took it over" \
  "attended:$OPS_USER,taken_over:$OTHER_OPS_USER,need_recorded:$OTHER_OPS_USER,voice_location_recorded:$OTHER_OPS_USER,dispatched:$OTHER_OPS_USER" \
  "$(field '[.history[] | "\(.action):\(.by.email)"] | join(",")')"
check "taking it over from Andrés" "$OPS_USER" "$(field '.history[1].detail.from')"
check "each one with its time, in order" true "$(field '[.history[].at] | . == sort')"
detail "$C"
check "C: closed by Andrés with its reason" "closed:$OPS_USER:resolved_by_phone" \
  "$(field '[.history[] | "\(.action):\(.by.email):\(.detail.reason)"] | join(",")')"
for table in assistance_action assistance_need assistance_voice_location assistance_dispatch assistance_closure; do
  for stmt in "UPDATE $table SET request_id = request_id" "DELETE FROM $table" "TRUNCATE $table"; do
    if psql_claims "$stmt" >/dev/null 2>&1; then
      check "$stmt fails" error ok
    else
      check "$stmt fails" error error
    fi
  done
done
check "what the app sent is not rewritten" "dispatched|sent|$LATITUDE|$LONGITUDE|unavailable|no_fix|" \
  "$(psql_claims "SELECT a.status, a.location_status, a.latitude, a.longitude, b.location_status, b.unavailable_reason,
    coalesce(b.latitude::text, '') FROM assistance_request a, assistance_request b WHERE a.id = '$A' AND b.id = '$B'")"

echo "== [$(stamp)] 6. Roles and privacy"
for who in insured advisor; do
  [[ "$who" == insured ]] && T="$INSURED_TOKEN" || T="$ADVISOR_TOKEN"
  inbox "" "$T"
  check "$who cannot read the inbox: 403" 403 "$STATUS"
  detail "$A" "$T"
  check "$who cannot read the detail: 403" 403 "$STATUS"
  act "$D" attend "" "$T"
  check "$who cannot attend: 403" 403 "$STATUS"
done
call GET "$INBOX" "" ""
check "without a token: 401" 401 "$STATUS"
detail "00000000-0000-4000-8000-000000000000"
capture 404-not-found
check "an unknown request is 404" "404/assistance_request_not_found" "$STATUS/$(field .error)"
call GET "$CLAIMS/v1/claims/assistance-requests/$A" "" "$INSURED_TOKEN"
check "the insured reads A dispatched" "200/dispatched" "$STATUS/$(field .status)"
check "without the provider" no "$(yes_if 'grep -qiE "prv-|grúas|despachos@" <<<"$BODY"')"
call GET "$CLAIMS/v1/claims/assistance-requests/$C" "" "$INSURED_TOKEN"
check "and C closed" "200/closed" "$STATUS/$(field .status)"
LOGS="$($COMPOSE logs claims 2>&1)"
check "no coordinates in the logs" 0 "$(grep -cE '4\.7712|74\.0452|3\.4921|76\.4203|4\.7793|3\.506' <<<"$LOGS" || true)"
check "no addresses" 0 "$(grep -cE 'Autopista Norte|Palmira|Cali' <<<"$LOGS" || true)"
check "no names" 0 "$(grep -cE 'Carolina|Andrés|Diana Torres|Grúas' <<<"$LOGS" || true)"
check "no e-mails" 0 "$(grep -cE '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[a-z]{2,}' <<<"$LOGS" || true)"
check "no subject of the insured" 0 "$(grep -cF "$INSURED_SUB" <<<"$LOGS" || true)"

echo
echo "== HU-W2.3-03 e2e: $CHECKS checks"
if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-W2.3-03 e2e: $FAILS check(s) FAILED" >&2
  exit 1
fi
echo "== HU-W2.3-03 e2e: every check passed."
