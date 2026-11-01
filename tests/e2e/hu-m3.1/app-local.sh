#!/usr/bin/env bash
# Local backend for the guided rehearsal of HU-M3.1-01 on the Android emulator:
# the notifications service verifying the tokens of the permanent user pool
# (the public values of the mobile app, app/build.gradle.kts), its PostgreSQL,
# LocalStack and the push provider stub. It prints how to build the debug APK
# against it, and --payout publishes an automatic payment for the insured as
# the parametric engine of claims will publish it.
#
# Usage (from the repository root):
#   SUBJECT=<sub of asegurado@example.com> bash tests/e2e/hu-m3.1/app-local.sh
#   SUBJECT=<same sub> bash tests/e2e/hu-m3.1/app-local.sh --payout [FLIGHT] [MINUTES]
#       # default: the payment of the prototype, AV-8421 215 min late, $ 180.000 COP
#   docker compose -f tests/e2e/hu-m3.1/docker-compose.yml down -v   # stop
#
# SUBJECT is read with the read-only
#   aws cognito-idp admin-get-user --user-pool-id us-east-2_EWwg8NL2C --username asegurado@example.com
# (attribute "sub"). No password ever goes through this script: the insured
# signs in on the emulator.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl aws; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done
if [[ -z "${SUBJECT:-}" ]]; then
  echo "Set SUBJECT with the sub of the insured (aws cognito-idp admin-get-user ..., read-only)." >&2
  exit 2
fi

COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -f docker-compose.yml"
export NOTIFICATIONS_PORT="${NOTIFICATIONS_PORT:-8393}" LOCALSTACK_PORT="${LOCALSTACK_PORT:-4577}" PUSH_PORT="${PUSH_PORT:-9393}"
NOTIFICATIONS="http://localhost:$NOTIFICATIONS_PORT"

if [[ "${1:-}" == "--payout" ]]; then
  FLIGHT="${2:-AV-8421}"
  MINUTES="${3:-215}"
  NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  PAYOUT="ppo-$(echo "$FLIGHT" | tr -d '-' | tr '[:upper:]' '[:lower:]')-$(date -u +%Y%m%d%H%M%S)"
  env -u AWS_PROFILE -u AWS_DEFAULT_PROFILE AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_PAGER="" \
    aws --endpoint-url "http://127.0.0.1:$LOCALSTACK_PORT" --region us-east-2 sns publish \
    --topic-arn arn:aws:sns:us-east-2:000000000000:solventa-local-claims \
    --message-attributes 'event_type={DataType=String,StringValue=ParametricPayoutCompleted}' \
    --message "{\"event_id\":\"parametric-payout-completed-$PAYOUT\",\"event_type\":\"ParametricPayoutCompleted\",\"event_version\":1,
      \"source\":\"claims\",\"occurred_at\":\"$NOW\",\"payload\":{\"payout_id\":\"$PAYOUT\",\"claim_id\":\"SIN-2026-01702\",
      \"policy_id\":\"VJ-2026-000931\",\"subject\":\"$SUBJECT\",\"amount_minor\":18000000,\"currency\":\"COP\",
      \"trigger\":{\"parameter\":\"flight_delay\",\"event_reference\":\"$FLIGHT\",\"threshold\":180,\"observed\":$MINUTES,
      \"unit\":\"minutes\",\"observed_at\":\"$NOW\"},\"payment_method\":{\"brand\":\"visa\",\"last4\":\"1111\"},
      \"idempotency_key\":\"pay-$PAYOUT\",\"paid_at\":\"$NOW\"}}" >/dev/null
  echo "== Published the automatic payment $PAYOUT ($FLIGHT, $MINUTES min, \$ 180.000 COP) for $SUBJECT"
  echo "   pushes the stub received: curl -s http://localhost:$PUSH_PORT/messages"
  exit 0
fi

POOL="${COGNITO_POOL_ID:-us-east-2_EWwg8NL2C}"
export AUTH_ISSUER="https://cognito-idp.us-east-2.amazonaws.com/$POOL"
export AUTH_JWKS_URL="$AUTH_ISSUER/.well-known/jwks.json"
export AUTH_CLIENT_IDS="${COGNITO_CLIENT_IDS:-6dri40bi3sk74cr0jb5djq43c0}"
PG_PASSWORD="$(head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-32)"
export PG_PASSWORD

echo "== Starting PostgreSQL, LocalStack, the push stub and notifications (tokens of the pool $POOL)"
$COMPOSE up -d --build --wait postgres-notifications localstack
$COMPOSE up -d --build push notifications
for _ in $(seq 1 90); do
  curl -sf "$NOTIFICATIONS/health" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "$NOTIFICATIONS/health" >/dev/null || {
  $COMPOSE logs notifications | tail -30 >&2
  exit 1
}

cat <<EOF

Ready. notifications answers at $NOTIFICATIONS (from the emulator: http://10.0.2.2:$NOTIFICATIONS_PORT).
Build and install the debug APK against it (PowerShell, in the mobile worktree):
  \$env:URL_API_LOCAL = "http://10.0.2.2:$NOTIFICATIONS_PORT"
  .\\gradlew.bat installDebug
Sign in as asegurado@example.com on the emulator (the password never goes through this script).
A payment:          SUBJECT=$SUBJECT bash tests/e2e/hu-m3.1/app-local.sh --payout [FLIGHT] [MINUTES]
Stop:               docker compose -f tests/e2e/hu-m3.1/docker-compose.yml down -v
EOF
