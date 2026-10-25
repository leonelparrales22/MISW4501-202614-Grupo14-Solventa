#!/usr/bin/env bash
# Local backend for the guided rehearsal of HU-M1.3-01 on the Android emulator:
# PostgreSQL, LocalStack and the real audit service verifying the tokens of the
# permanent user pool (bootstrap/identity; its public values are the ones of the
# mobile app, app/build.gradle.kts). It links the account of the insured
# (SUBJECT) to a document with a CustomerLinked event published to the local
# topic, creates two active consents of that document and the seed history (one
# expired, one revoked with its receipt), and prints how to build the debug APK
# against it. The environment stays up until you stop it.
#
# Usage (from the repository root):
#   SUBJECT=<sub of asegurado@example.com> bash tests/e2e/hu-m1.3/app-local.sh
#   bash tests/e2e/hu-m1.3/app-local.sh --another   # one more consent (CA-5: it
#                                                   # must appear on its own when
#                                                   # the network comes back)
#   docker compose -f tests/e2e/hu-m1.3/docker-compose.yml down -v   # stop
#
# SUBJECT is read with the read-only
#   aws cognito-idp admin-get-user --user-pool-id us-east-2_EWwg8NL2C --username asegurado@example.com
# (attribute "sub"). Options (environment): DOCUMENT (default 1020304050),
# COGNITO_POOL_ID and COGNITO_CLIENT_IDS (default: the pool and the mobile
# client of app/build.gradle.kts). No password ever goes through this script:
# the insured signs in on the emulator.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl aws; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done

COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -f docker-compose.yml"
export AUDIT_PORT="${AUDIT_PORT:-8390}" LOCALSTACK_PORT="${LOCALSTACK_PORT:-4576}" MOTO_PORT="${MOTO_PORT:-5130}"
AUDIT="http://localhost:$AUDIT_PORT"
DOCUMENT="${DOCUMENT:-1020304050}"
STAMP="$(date +%s)"

grant() { # <application>
  curl -sf -o /dev/null -w '%{http_code}' -X POST "$AUDIT/v1/audit/consents" -H 'Content-Type: application/json' \
    -d "{\"customer_id\":\"cust-app-local\",\"document_id\":\"$DOCUMENT\",\"full_name\":\"Asegurado De Prueba\",\"terms_version\":\"v1\",
      \"accepted_sources\":[\"open_finance\",\"open_data\",\"kyc\"],\"accepted_data_policy\":true,\"channel\":\"embedded\",
      \"partner_id\":\"aliado-banco-1\",\"application_id\":\"$1\"}"
}

if [[ "${1:-}" == "--another" ]]; then
  echo "== One more consent for $DOCUMENT: $(grant "app-local-$STAMP")"
  exit 0
fi

if [[ -z "${SUBJECT:-}" ]]; then
  echo "Set SUBJECT with the sub of the insured (aws cognito-idp admin-get-user ..., read-only)." >&2
  exit 2
fi

POOL="${COGNITO_POOL_ID:-us-east-2_EWwg8NL2C}"
export AUTH_ISSUER="https://cognito-idp.us-east-2.amazonaws.com/$POOL"
export AUTH_JWKS_URL="$AUTH_ISSUER/.well-known/jwks.json"
export AUTH_CLIENT_IDS="${COGNITO_CLIENT_IDS:-6dri40bi3sk74cr0jb5djq43c0}"
PG_PASSWORD="$(head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-32)"
export PG_PASSWORD

echo "== Starting PostgreSQL, LocalStack and audit (tokens of the pool $POOL)"
$COMPOSE up -d --build --wait postgres-audit localstack moto
$COMPOSE up -d --build audit
for _ in $(seq 1 90); do
  curl -sf "$AUDIT/health" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "$AUDIT/health" >/dev/null || {
  $COMPOSE logs audit | tail -30 >&2
  exit 1
}

echo "== Linking the account to $DOCUMENT (CustomerLinked on the local topic)"
NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ENVELOPE="{\"event_id\":\"customer-linked-app-local\",\"event_type\":\"CustomerLinked\",\"event_version\":1,\"source\":\"origination\",
  \"occurred_at\":\"$NOW\",\"payload\":{\"customer_id\":\"cus-app-local\",\"subject\":\"$SUBJECT\",\"document_id\":\"$DOCUMENT\",\"linked_at\":\"$NOW\"}}"
env -u AWS_PROFILE -u AWS_DEFAULT_PROFILE AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_PAGER="" \
  aws --endpoint-url "http://127.0.0.1:$LOCALSTACK_PORT" --region us-east-2 sns publish \
  --topic-arn arn:aws:sns:us-east-2:000000000000:solventa-local-origination --message "$ENVELOPE" >/dev/null
LINKED=""
for _ in $(seq 1 30); do
  LINKED="$($COMPOSE exec -T postgres-audit psql -U audit -d audit -tA -c "SELECT count(*) FROM holder_link WHERE subject = '$SUBJECT'" | tr -d '\r')"
  [[ "$LINKED" == 1 ]] && break
  sleep 1
done
if [[ "$LINKED" != 1 ]]; then
  echo "The link did not reach audit" >&2
  $COMPOSE logs audit | tail -30 >&2
  exit 1
fi

echo "== Two active consents and the history (expired, revoked)"
echo "   consent 1: $(grant "app-local-1-$STAMP")"
sleep 1
echo "   consent 2: $(grant "app-local-2-$STAMP")"
$COMPOSE exec -T postgres-audit psql -U audit -d audit -v ON_ERROR_STOP=1 -q \
  -v holder_document="$DOCUMENT" -v suffix="app$STAMP" -f - <seed-history.sql

cat <<EOF

Ready. audit answers at $AUDIT (from the emulator: http://10.0.2.2:$AUDIT_PORT).
Build and install the debug APK against it (PowerShell, in the mobile worktree):
  \$env:URL_API_LOCAL = "http://10.0.2.2:$AUDIT_PORT"
  .\\gradlew.bat installDebug
Sign in as asegurado@example.com on the emulator (the password never goes through this script).
One more consent:  bash tests/e2e/hu-m1.3/app-local.sh --another
Stop:              docker compose -f tests/e2e/hu-m1.3/docker-compose.yml down -v
EOF
