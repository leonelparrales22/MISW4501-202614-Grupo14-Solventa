#!/usr/bin/env bash
# Local backend for the guided rehearsal of HU-M1.2-01 on the Android emulator:
# origination verifying the tokens of the permanent user pool (bootstrap/
# identity; its public values are the ones of the mobile app,
# app/build.gradle.kts) and the policy core stub seeded with the policies of
# the prototype for the account of the insured (SUBJECT). It prints how to
# build the debug APK against it. The environment stays up until you stop it.
#
# Usage (from the repository root):
#   SUBJECT=<sub of asegurado@example.com> bash tests/e2e/hu-m1.2/app-local.sh
#   SUBJECT=<same sub> bash tests/e2e/hu-m1.2/app-local.sh --changes
#       # CA-4: renews AU-2025-118204 for a year and cancels HG-2025-880231 in
#       # the policy core; the app shows them after its next synchronization
#   docker compose -f tests/e2e/hu-m1.2/docker-compose.yml down -v   # stop
#
# SUBJECT is read with the read-only
#   aws cognito-idp admin-get-user --user-pool-id us-east-2_EWwg8NL2C --username asegurado@example.com
# (attribute "sub"). Options (environment): DOCUMENT (default 1020304050),
# COGNITO_POOL_ID and COGNITO_CLIENT_IDS (default: the pool and the mobile
# client of app/build.gradle.kts). No password ever goes through this script:
# the insured signs in on the emulator.
#
# The policies are the ones of the prototype (docs/pantallas.md of the mobile
# repository): their numbers, objects and insured values. Their dates are
# anchored to today so the states of the screens hold on any day: the auto
# policy ends in 16 days ("por vencer", as in the prototype's notice).
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
export ORIGINATION_PORT="${ORIGINATION_PORT:-8392}" POLICY_CORE_PORT="${POLICY_CORE_PORT:-9392}"
DOCUMENT="${DOCUMENT:-1020304050}"
CHANGES=false
[[ "${1:-}" == "--changes" ]] && CHANGES=true

day() { date -u -d "$1" +%Y-%m-%dT00:00:00Z; }

auto_to="$(day '+16 days')"
home_status="active"
if $CHANGES; then
  auto_to="$(day '+16 days +1 year')"
  home_status="cancelled"
fi

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

mkdir -p generated
cat >generated/policy-seed.json <<EOF
{
  "holders": {
    "$SUBJECT": [
      {"policy_id":"VH-2026-004512","line":"life","product":"Vida hipotecario","insured_object":"Crédito hipotecario",
       "status":"active","valid_from":"$(day '-45 days')","valid_to":"$(day '-45 days +20 years')",
       "covered_incidents":["death"],"coverages":$(coverages 250000000 death)},
      {"policy_id":"AU-2025-118204","line":"auto","product":"Autos","insured_object":"KJT-482",
       "status":"active","valid_from":"$(day '+16 days -1 year')","valid_to":"$auto_to",
       "covered_incidents":["collision","theft","third_party_damage","glass","other"],
       "coverages":$(coverages 72500000 collision theft third_party_damage glass other)},
      {"policy_id":"VJ-2026-000931","line":"travel","product":"Viaje","insured_object":"Retraso de vuelo",
       "status":"active","valid_from":"$(day '-2 days')","valid_to":"$(day '+8 days')",
       "covered_incidents":["flight_delay"],"coverages":$(coverages 180000 flight_delay)},
      {"policy_id":"HG-2025-880231","line":"home","product":"Hogar","insured_object":"Calle 93 # 14-20, Bogotá",
       "status":"$home_status","valid_from":"$(day '-240 days')","valid_to":"$(day '+125 days')",
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

if $CHANGES; then
  echo "== Renewing AU-2025-118204 until ${auto_to%%T*} and cancelling HG-2025-880231 in the policy core"
  # The stub reads its seed when it starts.
  $COMPOSE up -d --build --force-recreate policy-core
else
  echo "== Starting origination and the policy core (tokens of the pool $POOL)"
  $COMPOSE up -d --build --wait redis
  $COMPOSE up -d --build policy-core openfinance origination
fi

ORIGINATION="http://localhost:$ORIGINATION_PORT"
for _ in $(seq 1 90); do
  curl -sf "$ORIGINATION/health" >/dev/null 2>&1 && curl -sf "http://localhost:$POLICY_CORE_PORT/health" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "$ORIGINATION/health" >/dev/null || {
  $COMPOSE logs origination | tail -30 >&2
  exit 1
}
echo "   the policy core lists $(curl -sf "http://localhost:$POLICY_CORE_PORT/holders/$SUBJECT/policies" | grep -o '"policy_id"' | wc -l | tr -d ' ') policies for the insured"

if $CHANGES; then
  cat <<EOF

Done. Bring the network back on the emulator (or keep it on) with "Mis pólizas" open:
the synchronization shows "Sincronización completada · 2 cambios".
EOF
  exit 0
fi

cat <<EOF

Ready. origination answers at $ORIGINATION (from the emulator: http://10.0.2.2:$ORIGINATION_PORT).
Build and install the debug APK against it (PowerShell, in the mobile worktree):
  \$env:URL_API_LOCAL = "http://10.0.2.2:$ORIGINATION_PORT"
  .\\gradlew.bat installDebug
Sign in as asegurado@example.com on the emulator (the password never goes through this script).
CA-4 (changes):    SUBJECT=$SUBJECT bash tests/e2e/hu-m1.2/app-local.sh --changes
Stop:              docker compose -f tests/e2e/hu-m1.2/docker-compose.yml down -v
EOF
