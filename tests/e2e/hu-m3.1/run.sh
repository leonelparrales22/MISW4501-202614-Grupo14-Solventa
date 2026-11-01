#!/usr/bin/env bash
# HU-M3.1-01 end-to-end scenario: the real notifications service with its
# PostgreSQL, LocalStack (claims topic -> notifications queue), the push
# provider stub and Moto as the Cognito of the human routes. The automatic
# payment is published to the claims topic as the parametric engine of claims
# will publish it (docs/events/README.md, ParametricPayoutCompleted).
#
# Usage (from the repository root):  bash tests/e2e/hu-m3.1/run.sh
# Requires Docker (compose v2), curl, python and the AWS CLI (only against the
# local LocalStack and Moto, always with --endpoint-url to 127.0.0.1). The
# secrets are random and never printed; the environment is removed at the end
# and the script exits non-zero if a check fails.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl python aws; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done

COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -f docker-compose.yml"
export NOTIFICATIONS_PORT="${NOTIFICATIONS_PORT:-8393}" LOCALSTACK_PORT="${LOCALSTACK_PORT:-4577}"
export MOTO_PORT="${MOTO_PORT:-5131}" PUSH_PORT="${PUSH_PORT:-9393}"
NOTIFICATIONS="http://localhost:$NOTIFICATIONS_PORT"
PUSH="http://localhost:$PUSH_PORT"
TOPIC="arn:aws:sns:us-east-2:000000000000:solventa-local-claims"

secret() { head -c 256 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 24)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
export PG_PASSWORD MOTO_USER_PASSWORD

FAILED=0
cleanup() {
  if [[ "$FAILED" != 0 ]]; then
    $COMPOSE logs notifications push 2>/dev/null | tail -40 >&2 || true
  fi
  $COMPOSE down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

stamp() { date -u +%H:%M:%S; }
check() { # <description> <expected> <actual>
  if [[ "$2" == "$3" ]]; then
    echo "   ok  $1"
  else
    echo "   FAIL $1 (expected '$2', got '$3')" >&2
    FAILED=1
  fi
}
json() { # <python expression over d> reads JSON from stdin
  python -c "import json,sys; d=json.load(sys.stdin); print($1)"
}
aws_local() {
  env -u AWS_PROFILE -u AWS_DEFAULT_PROFILE AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_PAGER="" \
    aws --endpoint-url "http://127.0.0.1:$LOCALSTACK_PORT" --region us-east-2 "$@"
}
wait_healthy() {
  for _ in $(seq 1 90); do
    curl -sf "$1/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "$1 did not become healthy" >&2
  exit 1
}

echo "== [$(stamp)] Starting PostgreSQL, LocalStack, Moto and the push stub"
$COMPOSE up -d --build --wait postgres-notifications localstack moto
$COMPOSE up -d --build push

echo "== [$(stamp)] Seeding the local Cognito (Moto)"
SEED_OUT=""
for attempt in 1 2 3 4 5; do
  if SEED_OUT="$(env -u MSYS_NO_PATHCONV MOTO_URL="http://localhost:$MOTO_PORT" bash ../../security/ha-s01/local/seed.sh 2>&1 | tr -d '\r')"; then
    break
  fi
  [[ "$attempt" == 5 ]] && { echo "$SEED_OUT" >&2; exit 1; }
  sleep 2
done
AUTH_ISSUER="$(sed -n 's/^  export AUTH_ISSUER=//p' <<<"$SEED_OUT")"
AUTH_CLIENT_IDS="$(sed -n 's/^  export AUTH_CLIENT_IDS=//p' <<<"$SEED_OUT")"
CLIENT_ID="${AUTH_CLIENT_IDS%%,*}"
POOL_ID="${AUTH_ISSUER##*/}"
AUTH_JWKS_URL="http://moto:5000/$POOL_ID/.well-known/jwks.json"
export AUTH_ISSUER AUTH_CLIENT_IDS AUTH_JWKS_URL
token() { # <username>
  curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
    -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"$1\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
    tr -d '\r' | json "d['AuthenticationResult']['IdToken']"
}
INSURED="$(token asegurado.local@example.com)"
OPERATIONS="$(token operaciones.local@example.com)"
SUBJECT="$(python -c "import base64,json,sys; p=sys.argv[1].split('.')[1]; print(json.loads(base64.urlsafe_b64decode(p+'='*(-len(p)%4)))['sub'])" "$INSURED")"

echo "== [$(stamp)] Starting notifications"
$COMPOSE up -d --build notifications
wait_healthy "$NOTIFICATIONS"

api() { # <method> <path> [body] -> body in $BODY, status in $STATUS
  local out
  out="$(curl -s -w '\n%{http_code}' -X "$1" "$NOTIFICATIONS$2" -H "Authorization: Bearer ${TOKEN:-$INSURED}" \
    -H 'Content-Type: application/json' ${3:+-d "$3"})"
  STATUS="${out##*$'\n'}"
  BODY="${out%$'\n'*}"
}
publish() { # <payout_id> <flight> <minutes>
  local now
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  aws_local sns publish --topic-arn "$TOPIC" --message-attributes 'event_type={DataType=String,StringValue=ParametricPayoutCompleted}' \
    --message "{\"event_id\":\"parametric-payout-completed-$1\",\"event_type\":\"ParametricPayoutCompleted\",\"event_version\":1,
      \"source\":\"claims\",\"occurred_at\":\"$now\",\"payload\":{\"payout_id\":\"$1\",\"claim_id\":\"SIN-2026-01702\",
      \"policy_id\":\"VJ-2026-000931\",\"subject\":\"$SUBJECT\",\"amount_minor\":18000000,\"currency\":\"COP\",
      \"trigger\":{\"parameter\":\"flight_delay\",\"event_reference\":\"$2\",\"threshold\":180,\"observed\":$3,\"unit\":\"minutes\",
      \"observed_at\":\"$now\"},\"payment_method\":{\"brand\":\"visa\",\"last4\":\"1111\"},\"idempotency_key\":\"pay-$1\",
      \"paid_at\":\"$now\"}}" >/dev/null
}
pushes_for() { curl -sf "$PUSH/messages?token=$1" | json "len(d['messages'])"; }
wait_pushes() { # <token> <count> <seconds> -> elapsed seconds in $ELAPSED
  local start=$SECONDS
  for _ in $(seq 1 $(($3 * 4))); do
    [[ "$(pushes_for "$1")" == "$2" ]] && { ELAPSED=$((SECONDS - start)); return 0; }
    sleep 0.25
  done
  ELAPSED=-1
}
wait_listed() { # <count> <seconds>
  for _ in $(seq 1 $(($2 * 4))); do
    api GET /v1/notifications
    [[ "$(json "len(d['items'])" <<<"$BODY")" == "$1" ]] && return 0
    sleep 0.25
  done
}

echo "== [$(stamp)] P1. Criteria 1-3: a device with the permission gets the push within 60 s, with amount, currency and event"
api PUT /v1/devices/inst-a '{"platform":"android","token":"inst-a","push_enabled":true,"locale":"es"}'
check "the device is registered (200)" 200 "$STATUS"
check "the token is not answered back" False "$(json "'token' in d" <<<"$BODY")"
publish ppo-av8421 AV-8421 215
wait_pushes inst-a 1 60
check "the push arrived within 60 s of the payment" true "$([[ $ELAPSED -ge 0 && $ELAPSED -le 60 ]] && echo true || echo false)"
echo "        push after ~${ELAPSED} s"
curl -sf "$PUSH/messages?token=inst-a" >/tmp/hu-m3.1-push.json
check "title with amount and currency" "Pago automático recibido: \$ 180.000 COP" "$(json "d['messages'][0]['title']" </tmp/hu-m3.1-push.json)"
check "body with the event" True "$(json "'AV-8421' in d['messages'][0]['body'] and '215 min' in d['messages'][0]['body']" </tmp/hu-m3.1-push.json)"
check "the push says what to open (criterion 4)" ntf-ppo-av8421 "$(json "d['messages'][0]['data']['notification_id']" </tmp/hu-m3.1-push.json)"
wait_listed 1 10
check "it stays in the center (criterion 1)" ntf-ppo-av8421 "$(json "d['items'][0]['notification_id']" <<<"$BODY")"
check "unread" 1 "$(json "d['unread_count']" <<<"$BODY")"
api GET /v1/notifications/ntf-ppo-av8421
check "the detail of M10b: threshold, observed and idempotency key" "180 215 pay-ppo-av8421" \
  "$(json "' '.join([str(int(d['payout']['trigger']['threshold'])), str(int(d['payout']['trigger']['observed'])), d['payout']['idempotency_key']])" <<<"$BODY")"

echo "== [$(stamp)] P2. Criterion 6: three payments the same day are all kept and pushed; a redelivery changes nothing"
publish ppo-la4012 LA-4012 190
publish ppo-av9310 AV-9310 240
publish ppo-av8421 AV-8421 215
wait_pushes inst-a 3 60
check "three pushes in total" 3 "$(pushes_for inst-a)"
check "all in the same group" "parametric_payouts" "$(curl -sf "$PUSH/messages?token=inst-a" | json "','.join(sorted(set(m['group'] for m in d['messages'])))")"
wait_listed 3 10
check "three notifications in the center" 3 "$(json "len(d['items'])" <<<"$BODY")"
sleep 2
check "the redelivered payment sent no second push" 3 "$(pushes_for inst-a)"

echo "== [$(stamp)] P3. Criterion 5: without the permission no push goes out and the notification waits in the center"
api PUT /v1/devices/inst-a '{"platform":"android","push_enabled":false,"locale":"es"}'
check "the permission is withdrawn (200)" 200 "$STATUS"
publish ppo-av1111 AV-1111 200
wait_listed 4 15
check "the fourth notification is in the center" 4 "$(json "len(d['items'])" <<<"$BODY")"
sleep 2
check "no push for it" 3 "$(pushes_for inst-a)"

echo "== [$(stamp)] P4. Criterion 7: the provider fails, the push is retried and arrives; the center has it meanwhile"
$COMPOSE stop push >/dev/null
PUSH_FAIL_FIRST=3 $COMPOSE up -d push >/dev/null
for _ in $(seq 1 40); do curl -sf "$PUSH/health" >/dev/null 2>&1 && break; sleep 0.5; done
api PUT /v1/devices/inst-b '{"platform":"android","token":"inst-b","push_enabled":true,"locale":"en"}'
publish ppo-av2222 AV-2222 230
wait_listed 5 15
check "in the center before the push arrives" ntf-ppo-av2222 "$(json "d['items'][0]['notification_id']" <<<"$BODY")"
wait_pushes inst-b 1 60
check "delivered after the failures, within 60 s" true "$([[ $ELAPSED -ge 0 ]] && echo true || echo false)"
check "the provider got the failed attempts too" true "$(curl -sf "$PUSH/messages" | json "str(d['requests'] >= 4).lower()")"
check "written in the language of the device" "Automatic payment received: \$ 180,000 COP" \
  "$(curl -sf "$PUSH/messages?token=inst-b" | json "d['messages'][0]['title']")"

echo "== [$(stamp)] P5. Reading and isolation"
api POST /v1/notifications/read-all
check "mark all as read (204)" 204 "$STATUS"
api GET /v1/notifications
check "nothing unread" 0 "$(json "d['unread_count']" <<<"$BODY")"
api GET /v1/notifications/ntf-unknown
check "an unknown notification is 404" 404 "$STATUS"
TOKEN="$OPERATIONS" api GET /v1/notifications
check "operations cannot read the center (403)" 403 "$STATUS"
TOKEN="x" api GET /v1/notifications
check "without a valid token (401)" 401 "$STATUS"

if [[ "$FAILED" != 0 ]]; then
  echo "== [$(stamp)] Some checks FAILED" >&2
  exit 1
fi
echo "== [$(stamp)] All checks passed"
