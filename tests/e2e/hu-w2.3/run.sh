#!/usr/bin/env bash
# Runs the HU-W2.3-01 end-to-end scenarios with Docker, against the real claims
# service (its PostgreSQL, the registry of providers it seeds and its worker,
# the e-mail stub behind Toxiproxy, the policy core stub, Moto as Cognito and
# as the S3 of the supports and the evidence):
#
#   1. CA-1  a collision filed from the phone, approved by the advisor, takes
#            the location of its most precise trusted capture; a water damage
#            filed from the portal has none until the analyst records it
#   2. CA-2  the search lists only the providers with the specialty
#      CA-3  and suggests the nearest available one; a nearer one that is
#            unavailable is listed as such and is not suggested
#   3. CA-4  with the near providers unavailable the search widens to 10 km and
#            records both attempts; a stale search cannot be assigned
#   4. CA-5  the assignment keeps provider, time, distance and location, the
#            provider becomes busy and receives one e-mail with the filing
#            number and the coordinates; with the e-mail provider down the
#            assignment is still made and the notice leaves once it is back
#   5. CA-6  with every provider of the specialty unavailable the claim is
#            pending assignment with 3 attempts and the reason; a provider
#            that is not eligible is refused; when one is available again the
#            automatic search proposes it, and nobody is assigned
#   6.       operations registers a provider and changes its profile and its
#            availability; the policy, the batch and the roles
#   7.       the history tables cannot be changed with psql; the logs of
#            claims carry no contact of the providers nor coordinates
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
export CLAIMS_PORT="${CLAIMS_PORT:-8299}" POLICY_CORE_PORT="${POLICY_CORE_PORT:-9162}"
export EMAIL_PORT="${EMAIL_PORT:-9133}" TOXIPROXY_PORT="${TOXIPROXY_PORT:-8499}" MOTO_PORT="${MOTO_PORT:-5126}"
CLAIMS="http://localhost:$CLAIMS_PORT"
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

# call <method> <url> [json body] [bearer token] [idempotency key]: sets STATUS and BODY.
call() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -X "$1" "$2")
  [[ -n "${3:-}" ]] && args+=(-H 'Content-Type: application/json' -d "$3")
  [[ -n "${4:-}" ]] && args+=(-H "Authorization: Bearer $4")
  [[ -n "${5:-}" ]] && args+=(-H "Idempotency-Key: $5")
  STATUS="$(curl "${args[@]}")"
  BODY="$(cat "$WORK/body")"
}
field() { jq -r "$1" <<<"$BODY"; }

file_size() { wc -c <"$1" | tr -d '[:space:]'; }
file_sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}
content_type_of() {
  case "$1" in
    *.jpg) echo image/jpeg ;;
    *) echo application/pdf ;;
  esac
}

# put_signed <path>: PUTs the file to the signed URL and headers of BODY (the
# S3 of Moto, reached from the host); sets STATUS and BODY when it fails.
put_signed() {
  local url put_status h
  local headers=()
  while IFS= read -r h; do headers+=(-H "$h"); done < <(
    jq -r '.upload_headers | to_entries[] | select(.key | ascii_downcase != "content-length") | "\(.key): \(.value)"' <<<"$BODY")
  url="$(sed "s#//moto:5000#//localhost:$MOTO_PORT#" <<<"$(field .upload_url)")"
  put_status="$(curl -s -o "$WORK/put" -w '%{http_code}' -X PUT "$url" ${headers[@]+"${headers[@]}"} --data-binary "@$1")"
  if [[ "$put_status" != 200 ]]; then
    STATUS="$put_status"
    BODY="$(cat "$WORK/put")"
    return 1
  fi
}

# upload <token> <path>: declare the file, PUT it to the signed URL and
# confirm it, as the portal does (HU-W2.1-01).
upload() {
  local file="$2" declaration id
  declaration="$(jq -nc --arg n "$(basename "$file")" --arg t "$(content_type_of "$file")" \
    --argjson s "$(file_size "$file")" --arg h "$(file_sha256 "$file")" \
    '{file_name:$n, content_type:$t, byte_size:$s, sha256:$h}')"
  call POST "$CLAIMS/v1/claims/supports" "$declaration" "$1"
  [[ "$STATUS" == 201 ]] || return 0
  id="$(field .support_id)"
  put_signed "$file" || return 0
  call POST "$CLAIMS/v1/claims/supports/$id/confirmation" "" "$1"
}

# upload_ids <token> <path> <n>: uploads the file n times and prints the ids as a JSON array.
upload_ids() {
  local ids=() i
  for i in $(seq 1 "$3"); do
    upload "$1" "$2"
    [[ "$STATUS" == 200 ]] || {
      echo "upload failed: $STATUS $BODY" >&2
      return 1
    }
    ids+=("\"$(field .support_id)\"")
  done
  (IFS=,; echo "[${ids[*]}]")
}

# capture <token> <latitude> <longitude> <accuracy> <trusted>: takes a new
# photograph, uploads it as the app does (HU-M2.1-01) and prints the evidence
# item of the filing, with the place where it was captured.
capture() {
  local photo="$WORK/capture-$RANDOM$RANDOM.jpg" declaration
  printf '\xff\xd8\xff\xe0' >"$photo" && head -c 4096 /dev/urandom >>"$photo"
  declaration="$(jq -nc --argjson s "$(file_size "$photo")" --arg h "$(file_sha256 "$photo")" \
    '{kind:"photo", content_type:"image/jpeg", byte_size:$s, sha256:$h}')"
  call POST "$CLAIMS/v1/claims/evidence/upload-url" "$declaration" "$1"
  [[ "$STATUS" == 201 ]] || {
    echo "the upload of the evidence was not signed: $STATUS $BODY" >&2
    return 1
  }
  local key
  key="$(field .object_key)"
  put_signed "$photo" || {
    echo "the evidence was not uploaded: $STATUS $BODY" >&2
    return 1
  }
  jq -nc --arg k "$key" --arg c "$CAPTURED" --argjson lat "$2" --argjson lon "$3" --argjson acc "$4" \
    --argjson trusted "$5" --argjson s "$(file_size "$photo")" --arg h "$(file_sha256 "$photo")" \
    '{object_key:$k, kind:"photo", captured_at:$c, latitude:$lat, longitude:$lon, accuracy_m:$acc,
      location_trusted:$trusted, sha256:$h, byte_size:$s}'
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

# assignment_of <claim id>: reads the assignment of the claim as operations.
assignment_of() { call GET "$CLAIMS/v1/claims/$1/assignment" "" "$OPS_TOKEN"; }

# wait_for <seconds> <claim id> <jq condition>: waits until the assignment of
# the claim meets the condition; leaves it in BODY and sets WAITED (seconds).
wait_for() {
  local start i
  start="$(date +%s)"
  for i in $(seq 1 "$1"); do
    assignment_of "$2"
    if [[ "$STATUS" == 200 && "$(field "$3")" == true ]]; then
      WAITED=$(($(date +%s) - start))
      return 0
    fi
    sleep 1
  done
  WAITED=timeout
}

# mailbox <recipient>: what the e-mail stub received for the recipient.
mailbox() { curl -sf "$MAILBOX?to=$1"; }

# wait_mail <seconds> <recipient> <n>: waits until the recipient has n e-mails.
wait_mail() {
  local i
  for i in $(seq 1 "$1"); do
    [[ "$(mailbox "$2" | jq '.messages | length')" == "$3" ]] && return 0
    sleep 1
  done
}

set_availability() { # <provider id> <availability> [unavailable until]
  local body
  body="$(jq -nc --arg a "$2" '{availability:$a}')"
  [[ -n "${3:-}" ]] && body="$(jq -c --arg u "$3" '. + {unavailable_until:$u}' <<<"$body")"
  call POST "$CLAIMS/v1/claims/providers/$1/availability" "$body" "$OPS_TOKEN"
}

email_proxy() { # <true|false>
  curl -sf -X POST "http://localhost:$TOXIPROXY_PORT/proxies/email" -d "{\"enabled\":$1}" >/dev/null
}

# Test files of the portal: images and PDFs are recognized by their first bytes.
printf '\xff\xd8\xff\xe0' >"$WORK/photo.jpg" && head -c 4096 /dev/urandom >>"$WORK/photo.jpg"
printf '%%PDF-1.7\n' >"$WORK/doc.pdf" && head -c 4096 /dev/urandom >>"$WORK/doc.pdf"

echo "== [$(stamp)] Starting PostgreSQL, the policy core stub, the e-mail stub behind Toxiproxy and Moto"
$COMPOSE up -d --build --wait postgres-claims policy-core email toxiproxy moto
$COMPOSE up toxiproxy-init >/dev/null
curl -sf -X PUT "http://localhost:$MOTO_PORT/claims-evidence" >/dev/null

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
INSURED_USER="asegurado.local@example.com"
OPS_USER="operaciones.local@example.com"
INSURED_TOKEN="$(token "$INSURED_USER")"
OPS_TOKEN="$(token "$OPS_USER")"
ADVISOR_TOKEN="$(token asesor.local@example.com)"
INSURED_SUB="$(cut -d. -f2 <<<"$INSURED_TOKEN" | tr '_-' '/+' | awk '{ while (length($0) % 4) $0 = $0 "="; print }' | base64 -d 2>/dev/null | jq -r .sub)"

echo "== [$(stamp)] Starting claims (automatic search every 10 s)"
$COMPOSE up -d --build claims
wait_healthy
$COMPOSE logs claims | grep -E "assignment|database migration" || true

CORE="$(curl -sf "http://localhost:$POLICY_CORE_PORT/holders/$INSURED_SUB/policies")"
AUTO="$(jq -r '.policies[] | select(.line=="auto" and .status=="active") | .policy_id' <<<"$CORE")"
HOME_POLICY="$(jq -r '.policies[] | select(.line=="home") | .policy_id' <<<"$CORE")"
OCCURRED="$(jq -rn 'now - 2 * 86400 | floor | todateiso8601')"
CAPTURED="$(jq -rn 'now - 86400 | floor | todateiso8601')"
# The day after today in Bogotá (UTC-5): a provider back tomorrow is not
# eligible today (P8).
TOMORROW="$(jq -rn 'now - 5 * 3600 + 86400 | strftime("%Y-%m-%d")')"

# The place of the incident of the mockup, and where the analyst puts the pin.
LAT=4.6771
LON=-74.0483
ADDRESS="Carrera 15 # 93-40, apto 402, Bogotá"

# web_water <key>: files a water damage from the portal (HU-W2.1-01); sets
# STATUS, BODY, CLAIM and FILING.
web_water() {
  local supports body
  supports="$(jq -nc --argjson a "$(upload_ids "$INSURED_TOKEN" "$WORK/photo.jpg" 2)" \
    --argjson b "$(upload_ids "$INSURED_TOKEN" "$WORK/doc.pdf" 1)" \
    '[{requirement_code:"damage_photos",support_ids:$a},{requirement_code:"repair_quote",support_ids:$b}]')"
  body="$(jq -nc --arg p "$HOME_POLICY" --arg o "$OCCURRED" --argjson s "$supports" \
    '{policy_id:$p, incident_type:"water_damage", occurred_at:$o, place:"Carrera 15 # 93-40, apto 402, Bogotá",
      description:"Fuga en el baño principal que mojó el techo del apartamento de abajo.", supports:$s}')"
  call POST "$CLAIMS/v1/claims/web-filings" "$body" "$INSURED_TOKEN" "$RUN_ID-$1"
  CLAIM="$(field '.claim_id // empty')"
  FILING="$(field '.filing_number // empty')"
}

# approve <claim id>: the advisor approves the filing (HU-W2.1-03); the claim
# enters evaluation.
approve() { call POST "$CLAIMS/v1/claims/$1/review-approval" "" "$ADVISOR_TOKEN"; }

# record_location <claim id> <latitude> <longitude> <address>
record_location() {
  call POST "$CLAIMS/v1/claims/$1/incident-location" \
    "$(jq -nc --arg a "$4" --argjson lat "$2" --argjson lon "$3" '{address:$a, latitude:$lat, longitude:$lon}')" "$OPS_TOKEN"
}

search() { call POST "$CLAIMS/v1/claims/$1/provider-searches" "" "$OPS_TOKEN"; }

assign() { # <claim id> <provider id> <search seq>
  call POST "$CLAIMS/v1/claims/$1/assignment" \
    "$(jq -nc --arg p "$2" --argjson s "$3" '{provider_id:$p, search_seq:$s}')" "$OPS_TOKEN"
}

candidate() { # <provider id> <jq expression on the candidate>
  field ".last_search.candidates[] | select(.provider_id==\"$1\") | $2"
}

echo "== [$(stamp)] 1. CA-1: the location of the incident"
EVIDENCE="$(jq -sc . <(
  capture "$INSURED_TOKEN" 4.6772 -74.0484 12 true
  capture "$INSURED_TOKEN" "$LAT" "$LON" 8 true
  capture "$INSURED_TOKEN" 4.6773 -74.0482 20 true
  capture "$INSURED_TOKEN" 4.7012 -74.1003 3 false
))"
check "four photographs captured at the place" 4 "$(jq length <<<"$EVIDENCE")"
call POST "$CLAIMS/v1/claims" "$(jq -nc --arg p "$AUTO" --arg o "$OCCURRED" --argjson e "$EVIDENCE" \
  '{policy_id:$p, incident_type:"collision", occurred_at:$o, place:"Calle 94 con Carrera 15, Bogotá",
    description:"Choque por alcance en la Calle 94 con Carrera 15.", evidence:$e, declaration_accepted:true}')" \
  "$INSURED_TOKEN" "$RUN_ID-phone"
check "a collision is filed from the phone (201)" 201 "$STATUS"
PHONE="$(field .claim_id)"
PHONE_FILING="$(field .filing_number)"
assignment_of "$PHONE"
check "before the review the assignment does not apply" not_applicable "$(field .state)"
approve "$PHONE"
check "the advisor approves it (200)" 200 "$STATUS"
assignment_of "$PHONE"
check "in evaluation it is ready to search" ready "$(field .state)"
check "for a vehicle assessment" "auto/vehicle_assessment" "$(field '"\(.line)/\(.specialty)"')"
check "the location comes from the app" app "$(field .location.source)"
check "from the most precise trusted capture" "4.6771/-74.0483/8" \
  "$(field '"\(.location.latitude)/\(.location.longitude)/\(.location.accuracy_m)"')"
record_location "$PHONE" 4.7 -74.05 "Calle 100 # 15-20, Bogotá"
check "the analyst cannot replace it (409)" "409/location_from_app" "$STATUS/$(field .error)"

web_water water
check "a water damage is filed from the portal (201)" 201 "$STATUS"
WATER="$CLAIM"
WATER_FILING="$FILING"
approve "$WATER"
check "the advisor approves it (200)" 200 "$STATUS"
assignment_of "$WATER"
check "without a location it cannot be searched yet" location_missing "$(field .state)"
check "for a plumber" "home/plumbing" "$(field '"\(.line)/\(.specialty)"')"
search "$WATER"
check "a search without the location is refused (422)" "422/location_missing" "$STATUS/$(field .error)"
record_location "$WATER" 4.6771 -74.0483 "Cl 1"
check "an address of 4 characters is a field (422)" "422/address" "$STATUS/$(field '[.fields[].field] | join(",")')"
record_location "$WATER" "$LAT" "$LON" "$ADDRESS"
check "the analyst records address and pin (201)" 201 "$STATUS"
check "now ready" ready "$(field .state)"
check "the location of the analyst, without a precision" "analyst/null" "$(field '"\(.location.source)/\(.location.accuracy_m)"')"
check "with the address and who recorded it" "$ADDRESS/$OPS_USER" "$(field '"\(.location.address)/\(.location.recorded_by.email)"')"

echo "== [$(stamp)] 2. CA-2 / CA-3: only the specialty, the nearest available one is suggested"
set_availability prv-servicios-hogar-norte unavailable "$TOMORROW"
check "Servicios Hogar Norte (2.3 km) is back only tomorrow" "200/unavailable/$TOMORROW" \
  "$STATUS/$(field .availability)/$(field .unavailable_until)"
search "$WATER"
check "the search is recorded (201)" 201 "$STATUS"
check "found within 5 km" "found/5/1" "$(field '"\(.last_search.outcome)/\(.last_search.radius_km)/\(.last_search.attempts | length)"')"
check "state proposed" proposed "$(field .state)"
check "every candidate is a plumber" 0 "$(field '[.last_search.candidates[] | select(.specialties | index("plumbing") | not)] | length')"
check "the three plumbers of the registry, nearest first" "prv-servicios-hogar-norte,prv-reparaciones-chapinero,prv-tecnicos-unidos" \
  "$(field '[.last_search.candidates[].provider_id] | join(",")')"
check "a damp proofer only is not a candidate" 0 "$(field '[.last_search.candidates[] | select(.provider_id=="prv-humedades-y-acabados")] | length')"
check "the nearest one is listed as unavailable, not suggested" "2.3/unavailable/false/$TOMORROW" \
  "$(candidate prv-servicios-hogar-norte '"\(.distance_km)/\(.eligibility)/\(.suggested)/\(.unavailable_until)"')"
check "Reparaciones Chapinero (3.8 km) is suggested" "3.8/eligible/true" \
  "$(candidate prv-reparaciones-chapinero '"\(.distance_km)/\(.eligibility)/\(.suggested)"')"
check "Técnicos Unidos (6.1 km) is out of the radius" "6.1/out_of_radius/false" \
  "$(candidate prv-tecnicos-unidos '"\(.distance_km)/\(.eligibility)/\(.suggested)"')"
check "exactly one suggestion" 1 "$(field '[.last_search.candidates[] | select(.suggested)] | length')"
FIRST_SEQ="$(field .last_search.seq)"

echo "== [$(stamp)] 3. CA-4: the radius widens to 10 km"
set_availability prv-reparaciones-chapinero unavailable
check "Reparaciones Chapinero becomes unavailable" "200/unavailable" "$STATUS/$(field .availability)"
search "$WATER"
check "found within 10 km" "found/10" "$(field '"\(.last_search.outcome)/\(.last_search.radius_km)"')"
check "both attempts are recorded" "5:0,10:1" "$(field '[.last_search.attempts[] | "\(.radius_km):\(.eligible_count)"] | join(",")')"
check "Técnicos Unidos (6.1 km) is suggested" "6.1/eligible/true" \
  "$(candidate prv-tecnicos-unidos '"\(.distance_km)/\(.eligibility)/\(.suggested)"')"
SEQ="$(field .last_search.seq)"
check "a new search, a new sequence" "$((FIRST_SEQ + 1))" "$SEQ"
assign "$WATER" prv-reparaciones-chapinero "$FIRST_SEQ"
check "an outdated search cannot be assigned (409)" "409/search_outdated" "$STATUS/$(field .error)"

echo "== [$(stamp)] 4. CA-5: the assignment and the notice to the provider"
assign "$WATER" prv-tecnicos-unidos "$SEQ"
check "the analyst assigns Técnicos Unidos (201)" 201 "$STATUS"
check "state assigned" assigned "$(field .state)"
check "the provider, its distance and the search" "prv-tecnicos-unidos/6.1/$SEQ" \
  "$(field '"\(.assignment.provider.id)/\(.assignment.distance_km)/\(.assignment.search_seq)"')"
check "with the time and who assigned it" "yes/$OPS_USER" \
  "$(yes_if '[[ "$(field .assignment.assigned_at)" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T ]]')/$(field .assignment.assigned_by.email)"
check "the location shared with the provider" "analyst/$LAT/$LON/$ADDRESS" \
  "$(field '"\(.assignment.location_shared.source)/\(.assignment.location_shared.latitude)/\(.assignment.location_shared.longitude)/\(.assignment.location_shared.address)"')"
check "the status of the claim does not change" in_evaluation "$(field .claim_status)"
call GET "$CLAIMS/v1/claims/providers/prv-tecnicos-unidos" "" "$OPS_TOKEN"
check "the provider is busy with this claim" "busy/$WATER" "$(field '"\(.availability)/\(.busy_claim_id)"')"
TECNICOS_MAIL=operaciones@tecnicosunidos.example.com
wait_mail 20 "$TECNICOS_MAIL" 1
MAIL="$(mailbox "$TECNICOS_MAIL")"
check "the provider received one e-mail" 1 "$(jq '.messages | length' <<<"$MAIL")"
check "under the key of the assignment" "asignacion-$WATER" "$(jq -r '.messages[0].idempotency_key' <<<"$MAIL")"
check "with the filing number" "Asignación de siniestro $WATER_FILING" "$(jq -r '.messages[0].subject' <<<"$MAIL")"
MAIL_BODY="$(jq -r '.messages[0].body' <<<"$MAIL")"
check "with the type, the coordinates, the address and the map" yes \
  "$(yes_if '[[ "$MAIL_BODY" == *"Hogar · "* && "$MAIL_BODY" == *"4.677100, -74.048300"* && "$MAIL_BODY" == *"$ADDRESS"* && "$MAIL_BODY" == *"google.com/maps"* ]]')"
check "nothing of the insured" no "$(yes_if '[[ "$MAIL_BODY" == *"$INSURED_USER"* || "$MAIL_BODY" == *"$INSURED_SUB"* ]]')"
wait_for 10 "$WATER" '.assignment.notice.status == "sent"'
check "the notice is recorded as sent" "sent/1" "$(field '"\(.assignment.notice.status)/\(.assignment.notice.attempts)"')"
assign "$WATER" prv-tecnicos-unidos "$SEQ"
check "a second assignment is refused (409)" "409/assignment_exists" "$STATUS/$(field .error)"

search "$PHONE"
check "the collision finds Peritos Autos Andinos" "found/5/prv-peritos-autos-andinos" \
  "$(field '"\(.last_search.outcome)/\(.last_search.radius_km)/\(.last_search.candidates[] | select(.suggested) | .provider_id)"')"
PHONE_SEQ="$(field .last_search.seq)"
email_proxy false
assign "$PHONE" prv-peritos-autos-andinos "$PHONE_SEQ"
check "with the e-mail provider down the assignment is made (201)" "201/assigned" "$STATUS/$(field .state)"
check "and its notice is pending" pending "$(field .assignment.notice.status)"
wait_for 15 "$PHONE" '.assignment.notice.attempts >= 1'
check "the failed delivery is recorded and stays pending" pending "$(field .assignment.notice.status)"
PERITOS_MAIL=peritajes@autosandinos.example.com
check "nothing reached the provider" 0 "$(mailbox "$PERITOS_MAIL" | jq '.messages | length')"
email_proxy true
wait_for 60 "$PHONE" '.assignment.notice.status == "sent"'
check "once the e-mail provider is back the notice leaves (waited ${WAITED}s)" sent "$(field .assignment.notice.status)"
wait_mail 10 "$PERITOS_MAIL" 1
MAIL="$(mailbox "$PERITOS_MAIL")"
check "exactly one e-mail" 1 "$(jq '.messages | length' <<<"$MAIL")"
check "with the filing number and the precision of the capture" yes \
  "$(yes_if '[[ "$(jq -r ".messages[0].subject" <<<"$MAIL")" == *"$PHONE_FILING" && "$(jq -r ".messages[0].body" <<<"$MAIL")" == *"(precisión 8 m)"* ]]')"

echo "== [$(stamp)] 5. CA-6: no eligible provider, pending assignment and the automatic search"
web_water water2
WATER2="$CLAIM"
approve "$WATER2"
record_location "$WATER2" 4.6575 -74.056 "Calle 72 # 10-34, Bogotá"
check "a second water damage with its location" "201/ready" "$STATUS/$(field .state)"
search "$WATER2"
check "no plumber is available within 20 km" "exhausted/20/no_eligible_provider_within_max_radius" \
  "$(field '"\(.last_search.outcome)/\(.last_search.radius_km)/\(.last_search.reason)"')"
check "the three attempts" "5:0,10:0,20:0" "$(field '[.last_search.attempts[] | "\(.radius_km):\(.eligible_count)"] | join(",")')"
check "state pending_assignment" pending_assignment "$(field .state)"
check "no suggestion" 0 "$(field '[.last_search.candidates[] | select(.suggested)] | length')"
assign "$WATER2" prv-tecnicos-unidos "$(field .last_search.seq)"
check "a busy provider is not eligible (422)" "422/provider_not_eligible" "$STATUS/$(field .error)"
call GET "$CLAIMS/v1/claims/assignments?claim_id=$WATER2" "" "$OPS_TOKEN"
check "the queue shows the reason" "pending_assignment/no_eligible_provider_within_max_radius" \
  "$(field '"\(.items[0].state)/\(.items[0].reason)"')"
set_availability prv-servicios-hogar-norte available
check "Servicios Hogar Norte is available again" "200/available" "$STATUS/$(field .availability)"
wait_for 40 "$WATER2" '.state == "proposed"'
check "the automatic search proposes it (waited ${WAITED}s)" "proposed/automatic_retry" \
  "$(field '"\(.state)/\(.last_search.trigger)"')"
check "Servicios Hogar Norte is suggested" prv-servicios-hogar-norte \
  "$(field '.last_search.candidates[] | select(.suggested) | .provider_id')"
check "and nobody is assigned" null "$(field .assignment)"
call GET "$CLAIMS/v1/claims/providers/prv-servicios-hogar-norte" "" "$OPS_TOKEN"
check "the provider is still available" available "$(field .availability)"

echo "== [$(stamp)] 6. The registry of providers, the policy and the roles"
PROFILE='{"name": "Pinturas del Norte", "specialties": ["painting"], "latitude": 4.7021, "longitude": -74.0412,
  "address": "Calle 127 # 15-20, Bogotá", "contact_email": "citas@pinturasdelnorte.example.com"}'
call POST "$CLAIMS/v1/claims/providers" "$PROFILE" "$OPS_TOKEN"
check "operations registers a provider (201)" 201 "$STATUS"
NEW="$(field .id)"
check "with an id of the registry" yes "$(yes_if '[[ "$NEW" =~ ^prv-[0-9a-f]{12}$ ]]')"
check "available, without a phone, by the analyst" "available/null/$OPS_USER" \
  "$(field '"\(.availability)/\(.contact_phone)/\(.updated_by)"')"
call POST "$CLAIMS/v1/claims/providers/$NEW/profile" \
  "$(jq -c '.name = "Pinturas y Acabados del Norte" | .specialties += ["damp_proofing"] | .contact_phone = "+576015550199"' <<<"$PROFILE")" \
  "$OPS_TOKEN"
check "its profile changes (200)" "200/Pinturas y Acabados del Norte/painting,damp_proofing/+576015550199" \
  "$STATUS/$(field .name)/$(field '.specialties | join(",")')/$(field .contact_phone)"
set_availability "$NEW" unavailable "$TOMORROW"
check "and its availability" "200/unavailable/$TOMORROW" "$STATUS/$(field .availability)/$(field .unavailable_until)"
call GET "$CLAIMS/v1/claims/providers?specialty=damp_proofing&availability=unavailable" "" "$OPS_TOKEN"
check "the registry filters by specialty and availability" "$NEW" "$(field '[.items[].id] | join(",")')"
call POST "$CLAIMS/v1/claims/providers" '{"name": "", "specialties": ["towing"], "latitude": 91}' "$OPS_TOKEN"
check "an invalid profile names its fields at once (422)" "422/yes" \
  "$STATUS/$(field '[.fields[].field] as $f | ["name", "specialties", "latitude"] - $f | if length == 0 then "yes" else "no" end')"
call GET "$CLAIMS/v1/claims/assignment-policy" "" "$OPS_TOKEN"
check "the policy in force" "assignment-policy-v1/5,10,20/10" \
  "$(field '"\(.version)/\(.radii_km | map(tostring) | join(","))/\(.retry_interval_seconds)"')"
call GET "$CLAIMS/v1/claims/assignments?claim_id=$WATER&claim_id=$PHONE&claim_id=$WATER2&claim_id=00000000-0000-4000-8000-000000000000" "" "$OPS_TOKEN"
check "the queue of operations in one call" "assigned,assigned,proposed,not_found" "$(field '[.items[].state] | join(",")')"
check "with the provider and the distance" "Técnicos Unidos/6.1" "$(field '"\(.items[0].provider_name)/\(.items[0].distance_km)"')"
for who in insured advisor; do
  [[ "$who" == insured ]] && t="$INSURED_TOKEN" || t="$ADVISOR_TOKEN"
  call GET "$CLAIMS/v1/claims/providers" "" "$t"
  check "the $who cannot read the registry (403)" 403 "$STATUS"
  call POST "$CLAIMS/v1/claims/providers" "$PROFILE" "$t"
  check "nor register a provider (403)" 403 "$STATUS"
  call GET "$CLAIMS/v1/claims/$WATER/assignment" "" "$t"
  check "nor read the assignment (403)" 403 "$STATUS"
  call POST "$CLAIMS/v1/claims/$WATER2/provider-searches" "" "$t"
  check "nor search a provider (403)" 403 "$STATUS"
done
call GET "$CLAIMS/v1/claims/$WATER/assignment"
check "without a token (401)" 401 "$STATUS"

echo "== [$(stamp)] 7. Immutability and minimization"
for table in claim_incident_location claim_provider_search claim_provider_search_attempt claim_assignment; do
  for stmt in "UPDATE $table SET claim_id = claim_id" "DELETE FROM $table" "TRUNCATE $table CASCADE"; do
    if psql_claims "$stmt" >/dev/null 2>&1; then
      check "$stmt fails" error ok
    else
      check "$stmt fails" error error
    fi
  done
done
check "the history is still there" 2 "$(psql_claims "SELECT count(*) FROM claim_incident_location" | tr -d '[:space:]')"
LOGS="$($COMPOSE logs claims 2>&1)"
check "the logs carry no e-mail of a provider" 0 "$(grep -cE '@[a-z]+\.example\.com' <<<"$LOGS" || true)"
check "nor phones" 0 "$(grep -c '+57601555' <<<"$LOGS" || true)"
check "nor coordinates" 0 "$(grep -cE '4\.677|74\.048|4\.6575' <<<"$LOGS" || true)"
check "the logs carry the searches and the assignments" yes \
  "$(yes_if 'grep -q "provider search recorded" <<<"$LOGS" && grep -q "provider assigned" <<<"$LOGS"')"

echo
echo "== HU-W2.3-01 e2e: $CHECKS checks"
if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-W2.3-01 e2e: $FAILS check(s) FAILED" >&2
  exit 1
fi
echo "== HU-W2.3-01 e2e: every check passed."
