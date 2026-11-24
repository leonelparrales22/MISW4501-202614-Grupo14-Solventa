#!/usr/bin/env bash
# Local backend for the guided rehearsal of HU-M3.3-01 on the Android emulator
# (notification of every change of stage of a claim): claims and
# notifications behind one gateway, verifying the tokens of the permanent
# user pool (the public values of the mobile app, app/build.gradle.kts), with
# their PostgreSQL, LocalStack and the stubs. It files a claim of the insured
# (SUBJECT) and moves it on stage by stage, as the advisor and operations do
# from the portal, so each change reaches the app as a notification.
#
# Usage (from the repository root, Git Bash):
#   SUBJECT=<sub of asegurado@example.com> bash tests/e2e/hu-m3.3/app-local.sh            # start and file the claim
#   SUBJECT=<same sub> bash tests/e2e/hu-m3.3/app-local.sh --evaluate   # criterion 1: it enters evaluation
#   SUBJECT=<same sub> bash tests/e2e/hu-m3.3/app-local.sh --approve    # criterion 2: approved with its amount;
#                                                                       # 20 s later the gateway pays it (criterion 3)
#   SUBJECT=<same sub> bash tests/e2e/hu-m3.3/app-local.sh --status     # the notifications and pushes so far
#   docker compose -f tests/e2e/hu-m3.3/docker-compose.yml down -v       # stop
#
# SUBJECT is read with the read-only
#   aws cognito-idp admin-get-user --user-pool-id us-east-2_EWwg8NL2C --username asegurado@example.com
# (attribute "sub"). No password ever goes through this script: the insured
# signs in on the emulator.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl; do
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
export GATEWAY_PORT="${GATEWAY_PORT:-8395}" LOCALSTACK_PORT="${LOCALSTACK_PORT:-4579}" PUSH_PORT="${PUSH_PORT:-9395}"
GATEWAY="http://localhost:$GATEWAY_PORT"
KEY="hu-m3.3-claim"
claims_sql() { $COMPOSE exec -T postgres-claims psql -U claims -d claims -v ON_ERROR_STOP=1 -qAt -v sub="$SUBJECT" "$@"; }
notifications_sql() { $COMPOSE exec -T postgres-notifications psql -U notifications -d notifications -qAt "$@"; }

status() {
  echo "== Claim: $(echo "SELECT filing_number || ' ' || status FROM claim WHERE cognito_sub = :'sub' AND filing_key = '$KEY';" | claims_sql)"
  echo "== Notifications of the center (newest first):"
  notifications_sql -c "SELECT '   ' || to_char(occurred_at AT TIME ZONE 'America/Bogota', 'DD/MM/YYYY HH24:MI:SS') || '  ' ||
      (payload->>'stage') || COALESCE('  ' || (payload->>'amount_minor')::bigint / 100 || ' ' || (payload->>'currency'), '')
    FROM notification WHERE subject = '$SUBJECT' AND kind = 'claim_stage_changed' ORDER BY occurred_at DESC"
  echo "== Pushes the stub received: curl -s http://localhost:$PUSH_PORT/messages"
}

case "${1:-}" in
--evaluate)
  # The advisor approves the filing (HU-W2.1-03): it enters evaluation.
  claims_sql <<'SQL'
INSERT INTO claim_review (claim_id, seq, outcome, reviewed_by, reviewer_name, reviewed_at)
SELECT c.id, 1, 'approved', 'asesor@example.com', 'Asesor de prueba', now()
FROM claim c WHERE c.cognito_sub = :'sub' AND c.filing_key = 'hu-m3.3-claim'
  AND NOT EXISTS (SELECT 1 FROM claim_review r WHERE r.claim_id = c.id);
UPDATE claim SET status = 'in_evaluation' WHERE cognito_sub = :'sub' AND filing_key = 'hu-m3.3-claim' AND status = 'active';
SQL
  echo "== The claim entered evaluation; the notification arrives in a few seconds."
  exit 0
  ;;
--approve)
  # Operations approves $ 4.350.000 with 5 % of deductible (HU-W2.2-03): the
  # insured receives $ 4.132.500. The payout is due in 20 s, so the approval
  # and the payment arrive as two notifications apart.
  claims_sql <<'SQL'
WITH target AS (
    UPDATE claim SET status = 'approved'
    WHERE cognito_sub = :'sub' AND filing_key = 'hu-m3.3-claim' AND status = 'in_evaluation'
    RETURNING id, cognito_sub
)
INSERT INTO claim_decision (claim_id, outcome, decided_by, decided_at, gross_amount_minor, insured_value_minor,
    deductible_basis_points, currency, payout_key, beneficiary_id, payout_amount_minor, bank_code, bank_name,
    account_type, account_number, payout_status, next_attempt_at)
SELECT id, 'approved', 'analista@solventa.example', now(), 435000000, 32000000000, 500, 'COP',
    'indemnizacion-' || id, cognito_sub, 413250000, '1007', 'Banco Aliado', 'savings', '0123-4567-12', 'pending',
    now() + interval '20 seconds'
FROM target;
SQL
  echo "== The claim was approved; the notification arrives in a few seconds, and the payment about 20 s later."
  exit 0
  ;;
--status)
  status
  exit 0
  ;;
"") ;;
*)
  echo "Unknown option $1 (see the header of this script)." >&2
  exit 2
  ;;
esac

POOL="${COGNITO_POOL_ID:-us-east-2_EWwg8NL2C}"
export AUTH_ISSUER="https://cognito-idp.us-east-2.amazonaws.com/$POOL"
export AUTH_JWKS_URL="$AUTH_ISSUER/.well-known/jwks.json"
export AUTH_CLIENT_IDS="${COGNITO_CLIENT_IDS:-6dri40bi3sk74cr0jb5djq43c0}"
# The password of a running environment is kept: a new one would recreate the
# services with it while the databases still have the old one.
PG_PASSWORD="$($COMPOSE exec -T postgres-claims printenv POSTGRES_PASSWORD 2>/dev/null | tr -d '' || true)"
if [[ -z "$PG_PASSWORD" ]]; then
  PG_PASSWORD="$(head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-32)"
fi
export PG_PASSWORD

echo "== Starting claims and notifications behind the gateway (tokens of the pool $POOL)"
$COMPOSE up -d --build --wait postgres-claims postgres-notifications localstack
$COMPOSE up -d --build policy-core payment-gateway push claims notifications gateway
for _ in $(seq 1 90); do
  curl -sf "$GATEWAY/health" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "$GATEWAY/health" >/dev/null || {
  $COMPOSE logs claims notifications | tail -30 >&2
  exit 1
}

echo "== Filing a home claim of $SUBJECT"
claims_sql <<'SQL'
INSERT INTO claim (id, filing_number, cognito_sub, filing_key, fingerprint, policy_id, incident_type, occurred_at,
    place, description, details, status, filed_at, channel, catalog_version, estimated_amount_minor)
SELECT md5(:'sub' || 'hu-m3.3-claim')::uuid::text,
    'SIN-' || extract(year FROM now())::int || '-' || lpad(nextval('claim_filing_number_seq')::text, 5, '0'),
    :'sub', 'hu-m3.3-claim', 'hu-m3.3-claim', 'HG-2025-880231', 'water_damage', now() - interval '2 days',
    'Carrera 15 # 93-40, apto 402', 'Fuga de agua en la cocina que dañó el piso.', '{}'::jsonb, 'active', now(),
    'web', 'claim-catalog-v1', 435000000
WHERE NOT EXISTS (SELECT 1 FROM claim WHERE cognito_sub = :'sub' AND filing_key = 'hu-m3.3-claim');
SQL

cat <<EOF

Ready. The gateway answers at $GATEWAY (from the emulator: http://10.0.2.2:$GATEWAY_PORT).
Build and install the debug APK against it (PowerShell, in the mobile worktree):
  \$env:URL_API_LOCAL = "http://10.0.2.2:$GATEWAY_PORT"
  .\\gradlew.bat installDebug
Sign in as asegurado@example.com and allow the notifications. Then, one by one:
  Criterion 1:  SUBJECT=$SUBJECT bash tests/e2e/hu-m3.3/app-local.sh --evaluate
  Criteria 2-3: SUBJECT=$SUBJECT bash tests/e2e/hu-m3.3/app-local.sh --approve
  What arrived: SUBJECT=$SUBJECT bash tests/e2e/hu-m3.3/app-local.sh --status
Stop:           docker compose -f tests/e2e/hu-m3.3/docker-compose.yml down -v
EOF
