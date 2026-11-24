#!/usr/bin/env bash
# Local backend for the guided rehearsal of HU-M3.2-01 on the Android emulator
# (alerts 30, 15 and 5 days before a policy expires): origination and
# notifications behind one gateway, verifying the tokens of the permanent
# user pool (the public values of the mobile app, app/build.gradle.kts), with
# their PostgreSQL, LocalStack and the stubs. The policy core is seeded with
# the policies of the prototype for the insured (SUBJECT), as in
# tests/e2e/hu-m1.2, and the insured is linked in origination (a customer row
# with that cognito_sub), so the sweep looks at their policies.
#
# Usage (from the repository root, Git Bash):
#   SUBJECT=<sub of asegurado@example.com> bash tests/e2e/hu-m3.2/app-local.sh
#       # AU-2025-118204 ends in 16 days: the 30-day alert arrives within 10 s
#   SUBJECT=<same sub> bash tests/e2e/hu-m3.2/app-local.sh --dias-para-vencer 15   # the 15-day alert
#   SUBJECT=<same sub> bash tests/e2e/hu-m3.2/app-local.sh --dias-para-vencer 5    # the 5-day alert
#   SUBJECT=<same sub> bash tests/e2e/hu-m3.2/app-local.sh --status   # the alerts and pushes so far
#   docker compose -f tests/e2e/hu-m3.2/docker-compose.yml down -v     # stop
#
# --dias-para-vencer N makes AU-2025-118204 end at the last second of the day
# N days from today in Colombia. Every new date is a new period of the policy
# (as a renewal in the core would be), so it gets the alert of its last
# milestone crossed; running it again with the same N alerts nothing.
#
# SUBJECT is read with the read-only
#   aws cognito-idp admin-get-user --user-pool-id us-east-2_EWwg8NL2C --username asegurado@example.com
# (attribute "sub"). Options (environment): DOCUMENT (default 1020304050). No
# password ever goes through this script: the insured signs in on the
# emulator.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl date; do
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
export GATEWAY_PORT="${GATEWAY_PORT:-8396}" LOCALSTACK_PORT="${LOCALSTACK_PORT:-4580}" PUSH_PORT="${PUSH_PORT:-9396}"
GATEWAY="http://localhost:$GATEWAY_PORT"
DOCUMENT="${DOCUMENT:-1020304050}"
DAYS_TO_EXPIRY=16
origination_sql() { $COMPOSE exec -T postgres-origination psql -U origination -d origination -v ON_ERROR_STOP=1 -qAt -v sub="$SUBJECT" "$@"; }
notifications_sql() { $COMPOSE exec -T postgres-notifications psql -U notifications -d notifications -qAt "$@"; }

status() {
  echo "== Alerts recorded by origination (policy, end in Colombia, milestone, days left):"
  # psql only interpolates :'sub' in what it reads from stdin, not in -c.
  origination_sql <<'SQL'
SELECT '   ' || policy_id || '  ' || to_char(period_end AT TIME ZONE 'America/Bogota', 'DD/MM/YYYY') ||
    '  ' || milestone_days || ' d  (' || days_left || ' left)' FROM policy_expiry_notice WHERE subject = :'sub' ORDER BY detected_at;
SQL
  echo "== Notifications of the center (newest first):"
  notifications_sql -c "SELECT '   ' || id || '  read=' || (read_at IS NOT NULL) FROM notification
    WHERE subject = '$SUBJECT' AND kind = 'policy_expiry' ORDER BY occurred_at DESC"
  echo "== Pushes the stub received: curl -s http://localhost:$PUSH_PORT/messages"
}

case "${1:-}" in
--status)
  status
  exit 0
  ;;
--dias-para-vencer)
  DAYS_TO_EXPIRY="${2:-}"
  [[ "$DAYS_TO_EXPIRY" =~ ^[0-9]+$ ]] || {
    echo "--dias-para-vencer needs a number of days (0 or more)" >&2
    exit 2
  }
  ;;
"") ;;
*)
  echo "Unknown option $1 (see the header of this script)." >&2
  exit 2
  ;;
esac

day() { date -u -d "$1" +%Y-%m-%dT00:00:00Z; }
# end_of_day_cot is the last second of the day, in Colombia (UTC-5), that is
# the given offset away from today there.
end_of_day_cot() { date -u -d "-5 hours $1" +%Y-%m-%dT23:59:59-05:00; }

coverages() { # <insured value in pesos> <incident>...
  local value="$1" first=true
  shift
  printf '['
  for incident in "$@"; do
    $first || printf ','
    first=false
    printf '{"incident":"%s","insured_value_minor":%d,"currency":"COP","deductible_basis_points":1000}' "$incident" "$((value * 100))"
  done
  printf ']'
}

# The policies of the prototype (tests/e2e/hu-m1.2). The travel policy ends in
# 8 days, so it gets its 15-day alert too.
auto_to="$(end_of_day_cot "+$DAYS_TO_EXPIRY days")"
mkdir -p generated
cat >generated/policy-seed.json <<EOF
{
  "holders": {
    "$SUBJECT": [
      {"policy_id":"VH-2026-004512","line":"life","product":"Vida hipotecario","insured_object":"Crédito hipotecario",
       "status":"active","valid_from":"$(day '-45 days')","valid_to":"$(day '-45 days +20 years')",
       "covered_incidents":["death"],"coverages":$(coverages 250000000 death)},
      {"policy_id":"AU-2025-118204","line":"auto","product":"Autos","insured_object":"KJT-482",
       "status":"active","valid_from":"$(end_of_day_cot "+$DAYS_TO_EXPIRY days -1 year")","valid_to":"$auto_to",
       "covered_incidents":["collision","theft","third_party_damage","glass","other"],
       "coverages":$(coverages 72500000 collision theft third_party_damage glass other)},
      {"policy_id":"VJ-2026-000931","line":"travel","product":"Viaje","insured_object":"Retraso de vuelo",
       "status":"active","valid_from":"$(day '-2 days')","valid_to":"$(day '+8 days')",
       "covered_incidents":["flight_delay"],"coverages":$(coverages 180000 flight_delay)},
      {"policy_id":"HG-2025-880231","line":"home","product":"Hogar","insured_object":"Calle 93 # 14-20, Bogotá",
       "status":"active","valid_from":"$(day '-240 days')","valid_to":"$(day '+125 days')",
       "covered_incidents":["water_damage"],"coverages":$(coverages 320000000 water_damage)},
      {"policy_id":"AU-2024-093377","line":"auto","product":"Autos","insured_object":"KJT-482",
       "status":"cancelled","valid_from":"$(day '-400 days')","valid_to":"$(day '-35 days')",
       "covered_incidents":["collision","theft"],"coverages":$(coverages 60000000 collision theft)}
    ]
  },
  "profiles": {
    "$SUBJECT": {"full_name":"Carolina Pérez Ruiz","document_type":"CC","document_id":"$DOCUMENT",
                 "email":"asegurado@example.com","phone":"+57 300 000 0000",
                 "payout_account":{"bank_code":"1007","bank_name":"Banco Aliado","account_type":"savings","account_number":"0001-0002-03"}}
  }
}
EOF

POOL="${COGNITO_POOL_ID:-us-east-2_EWwg8NL2C}"
export AUTH_ISSUER="https://cognito-idp.us-east-2.amazonaws.com/$POOL"
export AUTH_JWKS_URL="$AUTH_ISSUER/.well-known/jwks.json"
export AUTH_CLIENT_IDS="${COGNITO_CLIENT_IDS:-6dri40bi3sk74cr0jb5djq43c0}"
# The password of a running environment is kept: a new one would recreate the
# services with it while the databases still have the old one.
PG_PASSWORD="$($COMPOSE exec -T postgres-origination printenv POSTGRES_PASSWORD 2>/dev/null | tr -d '\r' || true)"
if [[ -z "$PG_PASSWORD" ]]; then
  PG_PASSWORD="$(head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-32)"
fi
export PG_PASSWORD

echo "== Starting origination and notifications behind the gateway (tokens of the pool $POOL)"
$COMPOSE up -d --build --wait postgres-origination postgres-notifications redis localstack
$COMPOSE up -d --build openfinance policy-core push origination notifications gateway
# The stub reads its seed when it starts: a running one gets the new date.
$COMPOSE up -d --force-recreate policy-core
for _ in $(seq 1 90); do
  curl -sf "$GATEWAY/health" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "$GATEWAY/health" >/dev/null || {
  $COMPOSE logs origination notifications | tail -30 >&2
  exit 1
}

# The sweep asks the policy core for the holders linked in origination: the
# insured registers from the app in AWS; here the customer row is written
# directly (origination applied its migrations when it started).
echo "== Linking $SUBJECT in origination"
for _ in $(seq 1 30); do
  origination_sql -c "SELECT 1 FROM customer LIMIT 0" >/dev/null 2>&1 && break
  sleep 1
done
origination_sql <<SQL
INSERT INTO customer (id, document_id, full_name, email, birth_date, cognito_sub, created_at, updated_at)
VALUES ('cus-hu-m3.2', '$DOCUMENT', 'Carolina Pérez Ruiz', 'asegurado@example.com', DATE '1992-04-18', :'sub', now(), now())
ON CONFLICT DO NOTHING;
SQL

cat <<EOF

Ready. AU-2025-118204 ends on ${auto_to%%T*} ($DAYS_TO_EXPIRY days left in Colombia); the sweep runs every 10 s.
The gateway answers at $GATEWAY (from the emulator: http://10.0.2.2:$GATEWAY_PORT).
Build and install the debug APK against it (PowerShell, in the mobile worktree):
  \$env:URL_API_LOCAL = "http://10.0.2.2:$GATEWAY_PORT"
  .\\gradlew.bat installDebug
Sign in as asegurado@example.com and allow the notifications. Then:
  Next milestone: SUBJECT=$SUBJECT bash tests/e2e/hu-m3.2/app-local.sh --dias-para-vencer 15
  What arrived:   SUBJECT=$SUBJECT bash tests/e2e/hu-m3.2/app-local.sh --status
Stop:             docker compose -f tests/e2e/hu-m3.2/docker-compose.yml down -v
EOF
