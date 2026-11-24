#!/usr/bin/env bash
# Runs the HA-L02 local scenario with Docker, against the real claims service
# (its PostgreSQL behind Toxiproxy, the Redis of the read cache and Moto as
# Cognito). Phases:
#
#   1. normal     the database answers: the journey of EC-L02 (list, detail,
#                 timeline) is fresh, p95 <= 2 s                     [CA-1]
#   2. degradado  1500 ms of latency on the database: every read stops at
#                 900 ms and answers the cache, stale=true, p95 <= 2 s [CA-2, CA-3]
#   3. caida      the database is cut: the cache still answers         [CA-2]
#   4. aislamiento with the database cut, another insured never gets the
#                 claim from the cache                                [CA-4]
#   5. recuperada the database is back: the reads are fresh again
#
# The freshness of 5 minutes (TTL of the cache, CA-4) is checked by the unit
# tests of internal/adapters/redis, which move the clock of the cache.
#
# The script exits with a non-zero code when any check or threshold fails, and
# always removes the environment (down -v). Requirements: Docker (compose v2),
# curl, jq and the AWS CLI (only to seed the local Moto, as
# tests/security/ha-s01/local does). The secrets (PostgreSQL, Moto users) are
# random, generated here and never printed.
#
# Usage: ./run.sh
#   RATE / DURATION   arrival rate (journeys per second) and duration of each
#                     k6 phase (default 20 and 30s)
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
export CLAIMS_PORT="${CLAIMS_PORT:-8302}" TOXIPROXY_PORT="${TOXIPROXY_PORT:-8502}" MOTO_PORT="${MOTO_PORT:-5130}"
CLAIMS="http://localhost:$CLAIMS_PORT"
TOXI="http://localhost:$TOXIPROXY_PORT"
RATE="${RATE:-20}"
DURATION="${DURATION:-30s}"

secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
export PG_PASSWORD MOTO_USER_PASSWORD

cleanup() { $COMPOSE --profile tools down -v --remove-orphans >/dev/null 2>&1 || true; }
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

jwt_sub() {
  local payload
  payload="$(cut -d. -f2 <<<"$1" | tr '_-' '/+')"
  while ((${#payload} % 4)); do payload+="="; done
  base64 -d <<<"$payload" 2>/dev/null | jq -r .sub
}

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

# Faults on the database (Toxiproxy proxy "postgres").
db_latency() { # <ms>
  curl -s -X DELETE "$TOXI/proxies/postgres/toxics/slow" >/dev/null || true
  curl -sf -X POST "$TOXI/proxies/postgres/toxics" -H 'Content-Type: application/json' \
    -d "{\"name\":\"slow\",\"type\":\"latency\",\"stream\":\"downstream\",\"attributes\":{\"latency\":$1,\"jitter\":0}}" >/dev/null
}
db_enabled() { curl -sf -X POST "$TOXI/proxies/postgres" -H 'Content-Type: application/json' -d "{\"enabled\": $1}" >/dev/null; }
db_restore() {
  curl -s -X DELETE "$TOXI/proxies/postgres/toxics/slow" >/dev/null || true
  db_enabled true
}

journey() { # <fresh|stale>
  $COMPOSE --profile tools run --rm -e TOKEN="$INSURED_TOKEN" -e CLAIM_ID="$CLAIM_ID" -e EXPECT="$1" \
    -e RATE="$RATE" -e DURATION="$DURATION" k6 run --quiet /scripts/consulta.js
}

# get <path> <token>: sets STATUS and BODY.
get() {
  STATUS="$(curl -s -o /tmp/ha-l02-body.$$ -w '%{http_code}' -H "Authorization: Bearer $2" "$CLAIMS$1")"
  BODY="$(cat /tmp/ha-l02-body.$$)"
  rm -f /tmp/ha-l02-body.$$
}

echo "== [$(stamp)] Starting PostgreSQL behind Toxiproxy, Redis and Moto"
$COMPOSE up -d --wait postgres-claims redis toxiproxy moto
$COMPOSE up toxiproxy-init >/dev/null

echo "== [$(stamp)] Seeding the local Cognito (Moto) and a second insured"
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
CLIENT_ID="${AUTH_CLIENT_IDS%%,*}"
POOL_ID="${AUTH_ISSUER##*/}"
AUTH_JWKS_URL="http://moto:5000/$POOL_ID/.well-known/jwks.json"
export AUTH_ISSUER AUTH_CLIENT_IDS AUTH_JWKS_URL
if [[ -z "$AUTH_ISSUER" || -z "$CLIENT_ID" ]]; then
  echo "Could not read the pool and the client from seed.sh" >&2
  exit 1
fi
idp() {
  env -u MSYS_NO_PATHCONV AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION="${POOL_ID%%_*}" AWS_PAGER="" \
    aws --endpoint-url "http://localhost:$MOTO_PORT" cognito-idp "$@" >/dev/null
}
OTHER_USER="otro.asegurado.local@example.com"
idp admin-create-user --user-pool-id "$POOL_ID" --username "$OTHER_USER" --message-action SUPPRESS \
  --user-attributes Name=email,Value="$OTHER_USER" Name=email_verified,Value=true Name=name,Value="Andrés Gómez"
idp admin-set-user-password --user-pool-id "$POOL_ID" --username "$OTHER_USER" --password "$MOTO_USER_PASSWORD" --permanent
idp admin-add-user-to-group --user-pool-id "$POOL_ID" --username "$OTHER_USER" --group-name insured
token() { # <username>
  curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
    -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"$1\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken'
}
INSURED_TOKEN="$(token asegurado.local@example.com)"
OTHER_TOKEN="$(token "$OTHER_USER")"
INSURED_SUB="$(jwt_sub "$INSURED_TOKEN")"

echo "== [$(stamp)] Starting claims (database through Toxiproxy, read cache on Redis)"
$COMPOSE up -d --build claims
wait_healthy
$COMPOSE logs claims | grep -E "read cache|database migration" || true

echo "== [$(stamp)] Seeding a claim of the insured in evaluation (filed, then reviewed)"
CLAIM_ID="$($COMPOSE exec -T postgres-claims psql -U claims -d claims -qtAX -v sub="$INSURED_SUB" <<'SQL'
INSERT INTO claim (id, filing_number, cognito_sub, filing_key, fingerprint, policy_id, incident_type, occurred_at,
                   place, description, status, filed_at)
VALUES (gen_random_uuid()::text, 'SIN-2026-01655', :'sub', 'ha-l02-seed', 'ha-l02', 'HG-2025-880231', 'other',
        now() - interval '6 days', 'Carrera 15 # 93-40, apto 402', 'Daño por agua en la cocina.', 'active',
        now() - interval '5 days')
RETURNING id;
SQL
)"
CLAIM_ID="$(tr -d '[:space:]' <<<"$CLAIM_ID")"
# psql only substitutes variables in what it reads, not in -c.
$COMPOSE exec -T postgres-claims psql -U claims -d claims -qtAX -v id="$CLAIM_ID" >/dev/null <<'SQL'
UPDATE claim SET status = 'in_evaluation' WHERE id = :'id';
SQL
[[ "$CLAIM_ID" =~ ^[0-9a-f-]{36}$ ]] || {
  echo "Could not seed the claim" >&2
  exit 1
}

echo "== [$(stamp)] 1. normal: the database answers"
db_restore
journey fresh

echo "== [$(stamp)] 2. degradado: 1500 ms of latency on the database (budget 900 ms)"
db_latency 1500
journey stale

echo "== [$(stamp)] 3. caida: the database is cut"
curl -s -X DELETE "$TOXI/proxies/postgres/toxics/slow" >/dev/null || true
db_enabled false
journey stale

echo "== [$(stamp)] 4. aislamiento: another insured, with the database cut"
get "/v1/claims/$CLAIM_ID" "$OTHER_TOKEN"
check "the claim of another insured is never served from the cache" "false" "$([[ "$STATUS" == 200 ]] && echo true || echo false)"
check "and it does not leak its filing number" "false" "$(grep -q 'SIN-2026-01655' <<<"$BODY" && echo true || echo false)"
get "/v1/claims/$CLAIM_ID/history" "$OTHER_TOKEN"
check "nor its history" "false" "$([[ "$STATUS" == 200 ]] && echo true || echo false)"
get "/v1/claims" "$OTHER_TOKEN"
check "the list of the other insured never carries it" "false" "$(grep -q "$CLAIM_ID" <<<"$BODY" && echo true || echo false)"

echo "== [$(stamp)] 5. recuperada: the database is back"
db_restore
journey fresh

echo "== [$(stamp)] What claims logged when it served the cache"
$COMPOSE logs --no-log-prefix claims 2>/dev/null | grep -c "served from the read cache" | sed 's/^/   reads served from the cache: /' || true

if ((FAILS > 0)); then
  echo "== HA-L02: $FAILS check(s) failed" >&2
  exit 1
fi
echo "== HA-L02 scenario passed"
