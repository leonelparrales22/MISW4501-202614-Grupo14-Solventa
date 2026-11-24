#!/usr/bin/env bash
# Runs the HU-M2.3-01 end-to-end scenarios with Docker, against the real claims
# service (its PostgreSQL, the policy core stub behind Toxiproxy and Moto as
# Cognito), as the app would call it:
#
#   1. CA-2  a request with the location of the phone is recorded with its
#            policy, the UTC instant of the server, the coordinates and the
#            accuracy; the read and the row in PostgreSQL say the same
#   2. CA-4  a request without location is recorded with each of the three
#            reasons and no coordinates; mixing both shapes is 422
#   3. CA-5  a policy of another insured is policy_not_found and one that is
#            not in force policy_not_active; nothing is recorded
#   4. CA-5  with the policy core slower than the timeout, or cut, the answer
#      D2    is 503 policy_directory_unavailable in less than 1.5 s and nothing
#            is recorded; when it is back, the same key records the request
#   5.       the same key and body answer 200 with the same request; the same
#            key with another body is 422 idempotency_key_reused
#   6.       without a token 401, operations 403, another insured reads 404
#   7.       what was sent cannot be changed with psql (only the status); the
#            logs of claims carry no coordinates, place nor subject
#
# With CAPTURE_DIR set, the answers of scenarios 1 to 4 are also written there:
# they are the examples of docs/contratos/HU-M2.3-01-api.md.
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
export CLAIMS_PORT="${CLAIMS_PORT:-8303}" POLICY_CORE_PORT="${POLICY_CORE_PORT:-9163}"
export TOXIPROXY_PORT="${TOXIPROXY_PORT:-8503}" MOTO_PORT="${MOTO_PORT:-5127}"
CLAIMS="http://localhost:$CLAIMS_PORT"
TOXIPROXY="http://localhost:$TOXIPROXY_PORT"
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
# STATUS, BODY, SECONDS_TAKEN and HEADERS.
call() {
  local args=(-s -o "$WORK/body" -D "$WORK/headers" -w '%{http_code} %{time_total}' -X "$1" "$2")
  [[ -n "${3:-}" ]] && args+=(-H 'Content-Type: application/json' -d "$3")
  [[ -n "${4:-}" ]] && args+=(-H "Authorization: Bearer $4")
  [[ -n "${5:-}" ]] && args+=(-H "Idempotency-Key: $5")
  local out
  out="$(curl "${args[@]}")"
  STATUS="${out%% *}"
  SECONDS_TAKEN="${out##* }"
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
requests() { psql_claims "SELECT count(*) FROM assistance_request" | tr -d '[:space:]'; }

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
with_fix() { # <policy> <captured_at>
  jq -nc --arg p "$1" --arg c "$2" --arg place "$PLACE" --argjson lat "$LATITUDE" --argjson lon "$LONGITUDE" \
    '{policy_id:$p, location:{status:"sent", latitude:$lat, longitude:$lon, accuracy_m:8, captured_at:$c}, place:$place}'
}
without_fix() { # <policy> <reason>
  jq -nc --arg p "$1" --arg r "$2" '{policy_id:$p, location:{status:"unavailable", unavailable_reason:$r}}'
}
request() { # <body> <token> <key>
  call POST "$CLAIMS/v1/claims/assistance-requests" "$1" "$2" "$3"
}

echo "== [$(stamp)] Starting PostgreSQL, the policy core stub behind Toxiproxy and Moto"
$COMPOSE up -d --build --wait postgres-claims policy-core toxiproxy moto
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
# A second insured, to show that a request is read only by its owner.
OTHER_USER="otro.asegurado.local@example.com"
idp admin-create-user --user-pool-id "$POOL_ID" --username "$OTHER_USER" --message-action SUPPRESS \
  --user-attributes Name=email,Value="$OTHER_USER" Name=email_verified,Value=true Name=name,Value="Andrés Gómez"
idp admin-set-user-password --user-pool-id "$POOL_ID" --username "$OTHER_USER" --password "$MOTO_USER_PASSWORD" --permanent
idp admin-add-user-to-group --user-pool-id "$POOL_ID" --username "$OTHER_USER" --group-name insured
token() { # <username>
  curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
    -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$WEB_CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"$1\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken'
}
INSURED_TOKEN="$(token asegurado.local@example.com)"
OTHER_TOKEN="$(token "$OTHER_USER")"
OPS_TOKEN="$(token operaciones.local@example.com)"
INSURED_SUB="$(jwt_sub "$INSURED_TOKEN")"
OTHER_SUB="$(jwt_sub "$OTHER_TOKEN")"

echo "== [$(stamp)] Starting claims (POLICY_CORE_URL through Toxiproxy)"
$COMPOSE up -d --build claims
wait_healthy
$COMPOSE logs claims | grep -E "request for assistance|database migration" || true

# What the policy core knows of each insured.
CORE="$(curl -sf "http://localhost:$POLICY_CORE_PORT/holders/$INSURED_SUB/policies")"
AUTO="$(jq -r '.policies[] | select(.line=="auto" and .status=="active") | .policy_id' <<<"$CORE")"
AUTO_OBJECT="$(jq -r '.policies[] | select(.line=="auto" and .status=="active") | .insured_object' <<<"$CORE")"
HOME_POLICY="$(jq -r '.policies[] | select(.line=="home") | .policy_id' <<<"$CORE")"
EXPIRED="$(jq -r '.policies[] | select(.status=="expired") | .policy_id' <<<"$CORE")"
OTHER_AUTO="$(curl -sf "http://localhost:$POLICY_CORE_PORT/holders/$OTHER_SUB/policies" |
  jq -r '.policies[] | select(.line=="auto" and .status=="active") | .policy_id')"
# Six seconds ago, as a fresh fix of the phone. Date arithmetic with jq: the
# same on the date of macOS (BSD) and of Linux (GNU).
CAPTURED="$(jq -rn 'now - 6 | floor | todateiso8601')"

echo "== [$(stamp)] 1. CA-2 a request with the location of the phone"
BEFORE_EPOCH="$(date -u +%s)"
request "$(with_fix "$AUTO" "$CAPTURED")" "$INSURED_TOKEN" "$RUN_ID-ca2"
check "the request is recorded (201)" 201 "$STATUS"
capture 201-sent
check "nothing is cached" "no-store" "$(sed -n 's/^[Cc]ache-[Cc]ontrol: //p' <<<"$HEADERS")"
REQUEST_ID="$(field .request_id)"
NUMBER="$(field .request_number)"
check "with its own number AST-<year>-<4 digits or more>" yes "$(yes_if '[[ "$NUMBER" =~ ^AST-[0-9]{4}-[0-9]{4,}$ ]]')"
check "status new" new "$(field .status)"
check "the policy with its line, product and insured object" "$AUTO/auto/Autos/$AUTO_OBJECT" \
  "$(field '"\(.policy.policy_id)/\(.policy.line)/\(.policy.product)/\(.policy.insured_object)"')"
check "the coordinates, the accuracy and the instant of the fix" "sent/$LATITUDE/$LONGITUDE/8/$CAPTURED/null" \
  "$(field '"\(.location.status)/\(.location.latitude)/\(.location.longitude)/\(.location.accuracy_m)/\(.location.captured_at)/\(.location.unavailable_reason)"')"
check "the place the app sent" "$PLACE" "$(field .place)"
REQUESTED_AT="$(field .requested_at)"
check "requested_at in UTC" yes "$(yes_if '[[ "$REQUESTED_AT" == *Z ]]')"
REQUESTED_EPOCH="$(jq -rn --arg t "$REQUESTED_AT" '$t | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601')"
check "by the clock of the server, now" yes \
  "$(yes_if '(( REQUESTED_EPOCH >= BEFORE_EPOCH - 2 && REQUESTED_EPOCH <= $(date -u +%s) + 2 ))')"
CREATED_BODY="$BODY"
call GET "$CLAIMS/v1/claims/assistance-requests/$REQUEST_ID" "" "$INSURED_TOKEN"
check "the owner reads it (200)" 200 "$STATUS"
check "the read says the same as the answer" "$(jq -S . <<<"$CREATED_BODY")" "$(jq -S . <<<"$BODY")"
ROW="$(psql_claims "SELECT policy_id, status, location_status, latitude, longitude, accuracy_m,
  to_char(requested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS'), cognito_sub, requester_name, place
  FROM assistance_request WHERE id = '$REQUEST_ID'")"
check "the row in PostgreSQL holds the same" \
  "$AUTO|new|sent|$LATITUDE|$LONGITUDE|8|${REQUESTED_AT:0:19}|$INSURED_SUB|Carolina Pérez|$PLACE" "$ROW"
check "a request is not a claim" 0 "$(psql_claims "SELECT count(*) FROM claim" | tr -d '[:space:]')"
call GET "$CLAIMS/v1/claims" "" "$INSURED_TOKEN"
check "nor is it listed with the claims" "200/0" "$STATUS/$(field '.items | length')"

echo "== [$(stamp)] 2. CA-4 a request without location"
for reason in permission_denied location_disabled no_fix; do
  request "$(without_fix "$AUTO" "$reason")" "$INSURED_TOKEN" "$RUN_ID-ca4-$reason"
  capture "201-unavailable-$reason"
  check "$reason is recorded (201) without coordinates" "201/unavailable/$reason/null/null/null/null/null" \
    "$STATUS/$(field '"\(.location.status)/\(.location.unavailable_reason)/\(.location.latitude)/\(.location.longitude)/\(.location.accuracy_m)/\(.location.captured_at)/\(.place)"')"
done
check "the three rows keep their reason" 3 \
  "$(psql_claims "SELECT count(*) FROM assistance_request WHERE location_status = 'unavailable' AND latitude IS NULL
    AND unavailable_reason IN ('permission_denied', 'location_disabled', 'no_fix')" | tr -d '[:space:]')"
BEFORE="$(requests)"
request "$(without_fix "$AUTO" no_fix | jq -c --argjson lat "$LATITUDE" '.location.latitude = $lat')" "$INSURED_TOKEN" "$RUN_ID-ca4-mixed"
capture 422-validation-failed
check "unavailable with coordinates is 422" "422/validation_failed/location.latitude:not_applicable" \
  "$STATUS/$(field .error)/$(field '[.fields[] | "\(.field):\(.code)"] | join(",")')"
request "$(without_fix "$HOME_POLICY" no_fix)" "$INSURED_TOKEN" "$RUN_ID-ca4-home"
check "any line takes a request (home)" "201/home" "$STATUS/$(field .policy.line)"
check "only the valid ones were recorded" "$((BEFORE + 1))" "$(requests)"

echo "== [$(stamp)] 3. CA-5 only an active policy of the insured"
BEFORE="$(requests)"
request "$(with_fix "$OTHER_AUTO" "$CAPTURED")" "$INSURED_TOKEN" "$RUN_ID-ca5-other"
capture 422-policy-not-found
check "a policy of another insured is 422 policy_not_found" "422/policy_not_eligible/policy_not_found/null" \
  "$STATUS/$(field .error)/$(field .reason)/$(field .valid_from)"
request "$(with_fix "$EXPIRED" "$CAPTURED")" "$INSURED_TOKEN" "$RUN_ID-ca5-expired"
capture 422-policy-not-active
check "a policy not in force is 422 policy_not_active" "422/policy_not_eligible/policy_not_active" \
  "$STATUS/$(field .error)/$(field .reason)"
check "with its validity" yes "$(yes_if '[[ "$(field .valid_to)" != null ]]')"
check "nothing was recorded" "$BEFORE" "$(requests)"

echo "== [$(stamp)] 4. CA-5 / D2 the policy core down"
BEFORE="$(requests)"
DOWN_BODY="$(with_fix "$AUTO" "$CAPTURED")"
curl -sf -X POST "$TOXIPROXY/proxies/policycore/toxics" -H 'Content-Type: application/json' \
  -d '{"name":"slow","type":"latency","stream":"downstream","attributes":{"latency":3000}}' >/dev/null
request "$DOWN_BODY" "$INSURED_TOKEN" "$RUN_ID-ca5-down"
capture 503-policy-directory-unavailable
check "slower than the timeout: 503 policy_directory_unavailable" "503/policy_directory_unavailable" \
  "$STATUS/$(field .error)"
check "in less than 1.5 s ($SECONDS_TAKEN s)" yes "$(yes_if 'awk "BEGIN { exit !($SECONDS_TAKEN < 1.5) }"')"
curl -sf -X DELETE "$TOXIPROXY/proxies/policycore/toxics/slow" >/dev/null
curl -sf -X POST "$TOXIPROXY/proxies/policycore" -d '{"enabled":false}' >/dev/null
request "$DOWN_BODY" "$INSURED_TOKEN" "$RUN_ID-ca5-down"
check "cut: 503 policy_directory_unavailable" "503/policy_directory_unavailable" "$STATUS/$(field .error)"
check "in less than 1.5 s ($SECONDS_TAKEN s)" yes "$(yes_if 'awk "BEGIN { exit !($SECONDS_TAKEN < 1.5) }"')"
check "nothing was recorded while it was down" "$BEFORE" "$(requests)"
curl -sf -X POST "$TOXIPROXY/proxies/policycore" -d '{"enabled":true}' >/dev/null
# The breaker may be open: the same key is retried until it closes again.
for i in $(seq 1 20); do
  request "$DOWN_BODY" "$INSURED_TOKEN" "$RUN_ID-ca5-down"
  [[ "$STATUS" == 201 ]] && break
  sleep 1
done
check "back up, the same key records the request (201)" 201 "$STATUS"
check "once" "$((BEFORE + 1))" "$(requests)"

echo "== [$(stamp)] 5. Idempotency"
request "$(with_fix "$AUTO" "$CAPTURED")" "$INSURED_TOKEN" "$RUN_ID-ca2"
check "the same key and body answer 200" 200 "$STATUS"
check "with the same request" "$REQUEST_ID/$NUMBER/$REQUESTED_AT" \
  "$(field '"\(.request_id)/\(.request_number)/\(.requested_at)"')"
request "$(without_fix "$AUTO" no_fix)" "$INSURED_TOKEN" "$RUN_ID-ca2"
capture 422-idempotency-key-reused
check "the same key with another body is 422 idempotency_key_reused" "422/idempotency_key_reused" \
  "$STATUS/$(field .error)"
request "$(without_fix "$AUTO" no_fix)" "$INSURED_TOKEN" ""
check "without a key: 400" "400/invalid_request" "$STATUS/$(field .error)"

echo "== [$(stamp)] 6. Access"
request "$(without_fix "$AUTO" no_fix)" "" "$RUN_ID-access"
check "without a token: 401" 401 "$STATUS"
request "$(without_fix "$AUTO" no_fix)" "$OPS_TOKEN" "$RUN_ID-access"
check "operations cannot request: 403" 403 "$STATUS"
call GET "$CLAIMS/v1/claims/assistance-requests/$REQUEST_ID" "" "$OPS_TOKEN"
check "nor read: 403" 403 "$STATUS"
call GET "$CLAIMS/v1/claims/assistance-requests/$REQUEST_ID" "" "$OTHER_TOKEN"
NOT_YOURS="$STATUS/$BODY"
call GET "$CLAIMS/v1/claims/assistance-requests/00000000-0000-4000-8000-000000000000" "" "$OTHER_TOKEN"
check "another insured does not read it: 404 as one that does not exist" "$STATUS/$BODY" "$NOT_YOURS"
capture 404-not-found
check "with assistance_request_not_found" 404/assistance_request_not_found "$STATUS/$(field .error)"
request "$(without_fix "$AUTO" no_fix | jq -c --arg s "$OTHER_SUB" '. + {sub:$s}')" "$INSURED_TOKEN" "$RUN_ID-sub"
check "the owner never comes in the body: 400" 400 "$STATUS"

echo "== [$(stamp)] 7. Immutability and minimization"
for stmt in "UPDATE assistance_request SET latitude = 4.6, longitude = -74.1" \
  "UPDATE assistance_request SET policy_id = 'AU-0000-000000'" \
  "UPDATE assistance_request SET requested_at = now()" \
  "DELETE FROM assistance_request" "TRUNCATE assistance_request"; do
  if psql_claims "$stmt" >/dev/null 2>&1; then
    check "$stmt fails" error ok
  else
    check "$stmt fails" error error
  fi
done
check "only the status moves (what HU-W2.3-03 will write)" "UPDATE 1" \
  "$(psql_claims "UPDATE assistance_request SET status = 'in_call' WHERE id = '$REQUEST_ID'" 2>&1)"
call GET "$CLAIMS/v1/claims/assistance-requests/$REQUEST_ID" "" "$INSURED_TOKEN"
check "the request reads with its new status and what was sent" "in_call/$LATITUDE/$LONGITUDE/$NUMBER" \
  "$(field '"\(.status)/\(.location.latitude)/\(.location.longitude)/\(.request_number)"')"
LOGS="$($COMPOSE logs claims 2>&1)"
check "the logs carry the requests" yes "$(yes_if 'grep -q "request for assistance recorded" <<<"$LOGS"')"
check "with the number and the result of the location" yes \
  "$(yes_if 'grep -qF "\"request_number\":\"$NUMBER\"" <<<"$LOGS" && grep -qF "\"location_status\":\"sent\"" <<<"$LOGS"')"
check "no coordinates" 0 "$(grep -cE '4\.7712|74\.0452' <<<"$LOGS" || true)"
check "no place" 0 "$(grep -c 'Autopista Norte' <<<"$LOGS" || true)"
check "no subject of the insured" 0 "$(grep -cF -e "$INSURED_SUB" -e "$OTHER_SUB" <<<"$LOGS" || true)"
check "no name" 0 "$(grep -cE 'Carolina|Andrés Gómez' <<<"$LOGS" || true)"

echo
echo "== HU-M2.3-01 e2e: $CHECKS checks"
if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-M2.3-01 e2e: $FAILS check(s) FAILED" >&2
  exit 1
fi
echo "== HU-M2.3-01 e2e: every check passed."
