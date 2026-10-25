#!/usr/bin/env bash
# Runs the HA-D02 scenario end to end with Docker (EC-D02 locally):
#   1. baseline          10 acceptances -> charged, each with its contractual issuance
#   2. total outage      50 acceptances -> all registered, charges pending, no issuance
#   3. origination restart during the outage -> the charges survive in PostgreSQL
#   4. ambiguous answer  20 acceptances -> the gateway charges, origination never hears back
#   5. restore           100 % confirmed, exactly one real charge per key at the gateway,
#                        one issuance and one e-mail per policy (CA-4, CA-5)
#   6. expired window    origination relaunched with RETRY_WINDOW_SECONDS=10 and the
#                        gateway down: 5 acceptances -> issuance exceptions (CA-3);
#                        gateway restored, operations resume them -> confirmed once
#                        HU-W1.3-04: the monitor lists them as exceptions, with no action yet,
#                        and after the resumption every one has its action with author and note,
#                        ends as an issued policy, and retrying it again is a 409
#   7. contract          Schemathesis on the new routes, as an insured and as operations
# Every phase checks with k6 thresholds (checks == 1). The environment is always
# removed at the end (down -v).
#
# ./run.sh counterfactual runs the ambiguous phase against a gateway that ignores
# the idempotency key (SUPPORTS_IDEMPOTENCY=false) and reports the duplicates it
# produces: the risk of decision D-10. It does not count to approve HA-D02.
#
# Requirements: Docker (compose v2), curl and the AWS CLI (only to seed the local
# Moto, as tests/security/ha-s01/local does). In Git Bash the container paths
# are protected from rewriting by the script itself (MSYS_NO_PATHCONV).
# The secrets (PostgreSQL, document links, Moto users) are random, generated
# here and never printed.
set -euo pipefail
cd "$(dirname "$0")"

MODE="${1:-full}"
case "$MODE" in
  full | counterfactual) ;;
  *)
    echo "Usage: $0 [counterfactual]" >&2
    exit 1
    ;;
esac

# MSYS_NO_PATHCONV keeps Git Bash from rewriting the container paths (/scripts, /api).
COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -f docker-compose.yml"
export ORIGINATION_PORT="${ORIGINATION_PORT:-8190}" MOTO_PORT="${MOTO_PORT:-5110}" TOXIPROXY_PORT="${TOXIPROXY_PORT:-8484}"
export GATEWAY_PORT="${GATEWAY_PORT:-9140}" EMAIL_PORT="${EMAIL_PORT:-9130}"
API="http://localhost:$ORIGINATION_PORT"
RUN_ID="r$(date +%s)"

secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
DOCUMENT_LINK_SECRET="$(secret 48)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
export PG_PASSWORD DOCUMENT_LINK_SECRET MOTO_USER_PASSWORD
export RETRY_WINDOW_SECONDS=86400 SUPPORTS_IDEMPOTENCY=true
if [[ "$MODE" == counterfactual ]]; then
  SUPPORTS_IDEMPOTENCY=false
fi

cleanup() { $COMPOSE --profile tools down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

stamp() { date -u +%H:%M:%S; }

# k6run <script> [KEY=VALUE ...]: runs a k6 script in the compose network.
k6run() {
  local script="$1"
  shift
  local args=()
  local kv
  for kv in "$@"; do args+=(-e "$kv"); done
  $COMPOSE --profile tools run --rm "${args[@]}" k6 run --quiet --log-format raw "/scripts/$script"
}

# create <phase> <count>: accepts <count> demo offers; prints their ids.
create() {
  local out
  if ! out="$(k6run create.js "RUN_ID=$RUN_ID" "PHASE=$1" "COUNT=$2" 2>&1)"; then
    echo "$out" >&2
    return 1
  fi
  echo "$out" | tr -d '\r' | sed -n "s/^ACCEPTANCES $1 //p"
}

# await <state> <ids> [KEY=VALUE ...]: waits for every charge to reach the state.
await_state() {
  local expect="$1" ids="$2"
  shift 2
  k6run await.js "EXPECT=$expect" "IDS=$ids" "$@" 2>&1 | tr -d '\r' | grep -E "CONVERGED|SAMPLE|AMBIGUOUS|✓|✗|checks|ERRO" || return 1
}

verify() { # <ids> [KEY=VALUE ...]
  local ids="$1"
  shift
  k6run verify.js "IDS=$ids" "$@" 2>&1 | tr -d '\r' | grep -E "VERIFY|✓|✗|checks|ERRO" || return 1
}

wait_healthy() {
  local i
  for i in $(seq 1 90); do
    if curl -sf "$API/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "origination did not become healthy" >&2
  $COMPOSE logs origination | tail -40 >&2
  return 1
}

count() { tr ',' '\n' <<<"$1" | grep -c .; }

echo "== [$(stamp)] Starting PostgreSQL, the payment gateway and e-mail stubs, Toxiproxy and Moto ($MODE)"
$COMPOSE up -d --build --wait postgres payment-gateway email toxiproxy moto

echo "== [$(stamp)] Seeding the local Cognito (Moto)"
# Right after the start the published port can refuse a request although the
# container is healthy; seed.sh stops before creating anything in that case,
# so it is simply tried again.
SEED_OUT=""
for attempt in 1 2 3 4 5; do
  # Without MSYS_NO_PATHCONV: seed.sh hands /dev/null to the native curl of Git Bash.
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
export AUTH_ISSUER AUTH_CLIENT_IDS WEB_CLIENT_ID AUTH_JWKS_URL
if [[ -z "$AUTH_ISSUER" || -z "$WEB_CLIENT_ID" ]]; then
  echo "Could not read the pool and the client from seed.sh" >&2
  exit 1
fi

echo "== [$(stamp)] Starting origination (24-hour retry window)"
$COMPOSE up -d --build --wait origination
wait_healthy
$COMPOSE logs origination | grep -E "premium charge|database migration" || true

if [[ "$MODE" == counterfactual ]]; then
  echo "== [$(stamp)] Counterfactual: ambiguous answers against a gateway that ignores the key"
  ./toxiproxy.sh ambiguous
  CF="$(create cf 10)"
  await_state pending "$CF" GATEWAY_CHARGED_MIN=1 COUNTERFACTUAL=1 TIMEOUT_SECONDS=300
  ./toxiproxy.sh up
  await_state confirmed "$CF"
  verify "$CF" COUNTERFACTUAL=1
  echo "== Counterfactual done: duplicated > 0 above is the risk of a provider that does not honor the key (D-10)."
  exit 0
fi

echo "== [$(stamp)] 1. Baseline"
BASE="$(create base 10)"
[[ "$(count "$BASE")" == 10 ]]
await_state confirmed "$BASE"
verify "$BASE" EXPECTED_TOTAL=10

echo "== [$(stamp)] 2. Total outage of the payment gateway"
./toxiproxy.sh down
DOWN="$(create down 50)"
[[ "$(count "$DOWN")" == 50 ]]
await_state pending "$DOWN"

echo "== [$(stamp)] 3. Origination restarts during the outage"
$COMPOSE restart origination
wait_healthy
await_state pending "$DOWN"

echo "== [$(stamp)] 4. Ambiguous answers (the gateway charges, origination never hears back)"
./toxiproxy.sh ambiguous
AMB="$(create ambiguous 20)"
[[ "$(count "$AMB")" == 20 ]]
# Also until the gateway has really charged at least one pending acceptance
# whose answer was lost. With the circuit breaker open between probes (5 s)
# only the probes reach the gateway during the fault.
await_state pending "$AMB" TIMEOUT_SECONDS=300
await_state pending "$DOWN,$AMB" GATEWAY_CHARGED_MIN=1 TIMEOUT_SECONDS=300

echo "== [$(stamp)] 5. Restore the payment gateway"
./toxiproxy.sh up
RESTORED_AT="$(date +%s)"
await_state confirmed "$DOWN,$AMB" TIMEOUT_SECONDS=240
echo "converged $(($(date +%s) - RESTORED_AT)) s after the restore"
verify "$BASE,$DOWN,$AMB" EXPECTED_TOTAL=80
curl -sf "http://localhost:$GATEWAY_PORT/debug/charges" | tr -d '\r' |
  sed -n 's/.*"total_real_charges":\([0-9]*\).*"total_requests":\([0-9]*\).*/gateway: real charges \1, requests \2/p;s/.*"total_requests":\([0-9]*\).*"total_real_charges":\([0-9]*\).*/gateway: requests \1, real charges \2/p'

token() { # username
  curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
    -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$WEB_CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"$1\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
    tr -d '\r' | sed -n 's/.*"IdToken": *"\([^"]*\)".*/\1/p'
}

# monitor <path>: GET of a monitoring route as an operations analyst.
monitor() { curl -sf -H "Authorization: Bearer $OPS_TOKEN" "$API$1" | tr -d '\r'; }
# expect <description> <condition...>: fails the scenario when the condition is false.
expect() {
  local what="$1"
  shift
  if ! "$@"; then
    echo "FAILED (HU-W1.3-04): $what" >&2
    exit 1
  fi
  echo "ok: $what"
}
has_all() { # <json> <ids> <fragment-with-%s-for-the-id>: every id appears in the json
  local json="$1" ids="$2" fragment="$3" id
  for id in ${ids//,/ }; do
    # shellcheck disable=SC2059
    grep -q "$(printf "$fragment" "$id")" <<<"$json" || return 1
  done
}
has_none() { # <json> <ids> <fragment>
  local json="$1" ids="$2" fragment="$3" id
  for id in ${ids//,/ }; do
    # shellcheck disable=SC2059
    ! grep -q "$(printf "$fragment" "$id")" <<<"$json" || return 1
  done
}

echo "== [$(stamp)] 6. Expired retry window (origination relaunched with RETRY_WINDOW_SECONDS=10)"
./toxiproxy.sh down
RETRY_WINDOW_SECONDS=10 $COMPOSE up -d --wait --force-recreate --no-deps origination
wait_healthy
EXP="$(create expired 5)"
[[ "$(count "$EXP")" == 5 ]]
await_state failed "$EXP" TIMEOUT_SECONDS=90

echo "-- HU-W1.3-04: the monitor shows the expired charges as exceptions with nobody having acted yet"
OPS_TOKEN="$(token operaciones.local@example.com)"
INSURED_TOKEN="$(token asegurado.local@example.com)"
EXCEPTIONS="$(monitor '/v1/issuances?status=exception&limit=200')"
expect "every expired charge is an exception of premium_charge with its reason" \
  has_all "$EXCEPTIONS" "$EXP" '"acceptance_id":"%s"[^}]*"status":"exception"[^}]*"step":"premium_charge"[^}]*"reason":"retry_window_expired"'
expect "a fresh exception is not flagged as unattended (30 min have not passed)" \
  has_none "$EXCEPTIONS" "$EXP" '"acceptance_id":"%s"[^}]*"unattended":true'
expect "the exceptions are not active issuances" \
  has_none "$(monitor '/v1/issuances?status=active&limit=200')" "$EXP" '"acceptance_id":"%s"'
expect "a date range in the past leaves the list empty, not an error" \
  grep -q '"items":\[\]' <<<"$(monitor '/v1/issuances?from=2020-01-01T00:00:00Z&to=2020-01-02T00:00:00Z')"
expect "nobody has acted on the first exception yet" \
  test "$(monitor "/v1/issuances/${EXP%%,*}/actions")" = '{"items":[]}'
expect "an insured is refused" \
  test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $INSURED_TOKEN" "$API/v1/issuances")" = 403
expect "a bad filter is a 400" \
  test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $OPS_TOKEN" "$API/v1/issuances?status=other")" = 400

./toxiproxy.sh up
await_state failed "$EXP" RESUME=1
await_state confirmed "$EXP" TIMEOUT_SECONDS=90

echo "-- HU-W1.3-04: every resumption left its action, and the issuances are active now"
for id in ${EXP//,/ }; do
  ACTIONS="$(monitor "/v1/issuances/$id/actions")"
  expect "acceptance $id has exactly one resume action with author and note" \
    test "$(grep -o '"action":"resume"' <<<"$ACTIONS" | wc -l)" = 1
  expect "the action of $id names its author and a note" \
    grep -qE '"actor":"[^"]+","note":"[^"]+"' <<<"$ACTIONS"
done
# Charged again, the e-mail stub answers, so the policies go on to notified.
expect "the resumed charges are issued policies and no longer exceptions" \
  has_all "$(monitor '/v1/issuances?limit=200')" "$EXP" '"acceptance_id":"%s"[^}]*"status":"\(active\|notified\)"[^}]*"resume_count":1'
expect "none of them is an exception any more" \
  has_none "$(monitor '/v1/issuances?status=exception&limit=200')" "$EXP" '"acceptance_id":"%s"'
expect "the counts cover every issuance and nothing is an exception" \
  grep -q '"exceptions":0' <<<"$(monitor '/v1/issuances?status=active&limit=1')"
expect "retrying something that is not failed is a 409 (idempotent: no double charge or notice)" \
  test "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $OPS_TOKEN" -H 'Content-Type: application/json' \
    -d '{"note":"again"}' "$API/v1/issuances/${EXP%%,*}/retry")" = 409
expect "an unknown acceptance is a 404" \
  test "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $OPS_TOKEN" -H 'Content-Type: application/json' \
    -d '{"note":"x"}' "$API/v1/issuances/acc-unknown/retry")" = 404
verify "$BASE,$DOWN,$AMB,$EXP" EXPECTED_TOTAL=85

echo "== [$(stamp)] 7. Contract (Schemathesis) on the HA-D02 routes"
ENDPOINTS='^/v1/(acceptances/\{acceptance_id\}/charge|issuance-exceptions|issuances)'
for user in asegurado.local@example.com operaciones.local@example.com; do
  echo "-- as $user"
  $COMPOSE --profile tools run --rm schemathesis run /api/openapi.yaml --base-url=http://origination:8080 \
    -H "Authorization: Bearer $(token "$user")" --include-path-regex="$ENDPOINTS" \
    --checks=all --hypothesis-max-examples=50 --hypothesis-seed=1
done

echo "== [$(stamp)] HA-D02 scenario passed (run $RUN_ID)."
