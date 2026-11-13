#!/usr/bin/env bash
# Local backend for the guided rehearsal of HU-M2.4-01 on the Android emulator
# (consultation and follow-up of the claim): the claims service verifying the
# tokens of the permanent user pool (the public values of the mobile app,
# app/build.gradle.kts), with its PostgreSQL behind Toxiproxy, Redis as the
# read cache, the policy core and payment gateway stubs and LocalStack. It
# seeds two claims of the insured (SUBJECT): one just filed and one in
# evaluation with its triage (criteria 1 and 2), and prints how to build the
# debug APK against it.
#
# Usage (from the repository root, Git Bash):
#   SUBJECT=<sub of asegurado@example.com> bash tests/e2e/hu-m2.4/app-local.sh
#   SUBJECT=<same sub> bash tests/e2e/hu-m2.4/app-local.sh --payout [FLIGHT] [MINUTES]
#       # criterion 3: the delay of a flight of the travel policy of the insured
#       # reaches the parametric engine (ParametricTriggerObserved); claims
#       # files, resolves and pays the claim and publishes
#       # ParametricPayoutCompleted. Default AV-8421, 215 min (threshold 180).
#   bash tests/e2e/hu-m2.4/app-local.sh --slow-db   # criterion 4: the database answers in
#                                                   # 2 s, the reads answer from the cache (stale)
#   bash tests/e2e/hu-m2.4/app-local.sh --fast-db   # back to normal
#   docker compose -f tests/e2e/hu-m2.4/docker-compose.yml down -v   # stop
#
# SUBJECT is read with the read-only
#   aws cognito-idp admin-get-user --user-pool-id us-east-2_EWwg8NL2C --username asegurado@example.com
# (attribute "sub"). Options (environment): COGNITO_POOL_ID and
# COGNITO_CLIENT_IDS (default: the pool and the mobile client of
# app/build.gradle.kts). No password ever goes through this script: the
# insured signs in on the emulator.
set -euo pipefail
cd "$(dirname "$0")"

for tool in docker curl aws python; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done

COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -f docker-compose.yml"
export CLAIMS_PORT="${CLAIMS_PORT:-8394}" POLICY_CORE_PORT="${POLICY_CORE_PORT:-9394}"
export TOXIPROXY_PORT="${TOXIPROXY_PORT:-8494}" LOCALSTACK_PORT="${LOCALSTACK_PORT:-4578}"
CLAIMS="http://localhost:$CLAIMS_PORT"
TOXIPROXY="http://localhost:$TOXIPROXY_PORT"
LOCALSTACK="http://127.0.0.1:$LOCALSTACK_PORT"
local_aws() {
  env -u AWS_PROFILE -u AWS_DEFAULT_PROFILE AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_PAGER="" \
    MSYS_NO_PATHCONV=1 aws --endpoint-url "$LOCALSTACK" --region us-east-2 "$@"
}
psql_claims() { $COMPOSE exec -T postgres-claims psql -U claims -d claims -v ON_ERROR_STOP=1 -qAt "$@"; }
need_subject() {
  if [[ -z "${SUBJECT:-}" ]]; then
    echo "Set SUBJECT with the sub of the insured (aws cognito-idp admin-get-user ..., read-only)." >&2
    exit 2
  fi
}
# policy_of LINE: the id of the active policy of the line of the insured, from
# the policy core stub (deterministic by holder).
policy_of() {
  curl -sf "http://localhost:$POLICY_CORE_PORT/holders/$SUBJECT/policies" | python -c "
import json, sys
for p in json.load(sys.stdin)['policies']:
    if p['line'] == sys.argv[1] and p['status'] == 'active':
        print(p['policy_id']); break" "$1"
}

case "${1:-}" in
--slow-db)
  curl -sf -X POST "$TOXIPROXY/proxies/postgres/toxics" -H 'Content-Type: application/json' \
    -d '{"name":"slow","type":"latency","stream":"downstream","attributes":{"latency":2000}}' >/dev/null
  echo "== The database answers in 2 s: the reads of the app wait 900 ms and answer the last read of the cache (stale: true)."
  echo "   Open the claim once BEFORE this so its read is in the cache. Back to normal: $0 --fast-db"
  exit 0
  ;;
--fast-db)
  curl -sf -X DELETE "$TOXIPROXY/proxies/postgres/toxics/slow" >/dev/null || true
  echo "== The database answers normally again."
  exit 0
  ;;
--payout)
  need_subject
  FLIGHT="${2:-AV-8421}"
  MINUTES="${3:-215}"
  POLICY="$(policy_of travel)"
  NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  OBS="obs-$(echo "$FLIGHT" | tr -d '-' | tr '[:upper:]' '[:lower:]')-$(date -u +%Y%m%d%H%M%S)"
  local_aws sns publish --topic-arn arn:aws:sns:us-east-2:000000000000:solventa-local-iot \
    --message-attributes 'event_type={DataType=String,StringValue=ParametricTriggerObserved}' \
    --message "{\"event_id\":\"parametric-trigger-observed-$OBS\",\"event_type\":\"ParametricTriggerObserved\",
      \"event_version\":1,\"source\":\"iot\",\"occurred_at\":\"$NOW\",\"payload\":{\"observation_id\":\"$OBS\",
      \"subject\":\"$SUBJECT\",\"policy_id\":\"$POLICY\",\"parameter\":\"flight_delay\",\"event_reference\":\"$FLIGHT\",
      \"observed\":$MINUTES,\"unit\":\"minutes\",\"observed_at\":\"$NOW\"}}" >/dev/null
  echo "== Published the observation $OBS: flight $FLIGHT $MINUTES min late, policy $POLICY of $SUBJECT"
  for _ in $(seq 1 30); do
    STATUS="$(psql_claims -c "SELECT c.filing_number || ' ' || c.status FROM claim c
      JOIN claim_parametric_resolution p ON p.claim_id = c.id WHERE p.observation_id = '$OBS'")"
    [[ "$STATUS" == *" paid" ]] && break
    sleep 1
  done
  if [[ -z "${STATUS:-}" ]]; then
    echo "   No claim was filed (below the threshold of 180 min, or see: $COMPOSE logs claims)."
    exit 0
  fi
  echo "   Claim $STATUS (resolved automatically). The app shows it in \"Mis siniestros\"."
  local_aws sqs receive-message --queue-url "$LOCALSTACK/000000000000/solventa-local-claims-events" \
    --wait-time-seconds 5 --max-number-of-messages 10 --query 'Messages[].Body' --output text |
    python -c "
import json, sys
for line in sys.stdin.read().split('\t'):
    line = line.strip()
    if line:
        e = json.loads(line); p = e['payload']
        print('   Published', e['event_type'], 'for', p['claim_id'], p['amount_minor'] // 100, p['currency'],
              '(notifications sends the push from it).')" || true
  exit 0
  ;;
"") ;;
*)
  echo "Unknown option $1 (see the header of this script)." >&2
  exit 2
  ;;
esac

need_subject
POOL="${COGNITO_POOL_ID:-us-east-2_EWwg8NL2C}"
export AUTH_ISSUER="https://cognito-idp.us-east-2.amazonaws.com/$POOL"
export AUTH_JWKS_URL="$AUTH_ISSUER/.well-known/jwks.json"
export AUTH_CLIENT_IDS="${COGNITO_CLIENT_IDS:-6dri40bi3sk74cr0jb5djq43c0}"
PG_PASSWORD="$(head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-32)"
export PG_PASSWORD

echo "== Starting PostgreSQL, Redis, Toxiproxy, LocalStack, the stubs and claims (tokens of the pool $POOL)"
$COMPOSE up -d --build --wait postgres-claims redis localstack policy-core payment-gateway toxiproxy
$COMPOSE up toxiproxy-init >/dev/null
$COMPOSE up -d --build claims
for _ in $(seq 1 90); do
  curl -sf "$CLAIMS/health" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "$CLAIMS/health" >/dev/null || {
  $COMPOSE logs claims | tail -30 >&2
  exit 1
}

# The seed: two claims of the insured on their active auto policy, filed from
# the portal. The status changes go through the triggers of the database, so
# the history (the timeline of the app) has every stage with its time.
AUTO="$(policy_of auto)"
echo "== Seeding the claims of $SUBJECT on $AUTO"
psql_claims -v sub="$SUBJECT" -v policy="$AUTO" <<'SQL'
INSERT INTO claim (id, filing_number, cognito_sub, filing_key, fingerprint, policy_id, incident_type, occurred_at,
    place, description, details, status, filed_at, channel, catalog_version, estimated_amount_minor)
SELECT v.id, 'SIN-' || extract(year FROM now())::int || '-' || lpad(nextval('claim_filing_number_seq')::text, 5, '0'),
    :'sub', v.key, v.key, :'policy', v.incident, now() - v.age - interval '2 hours', v.place, v.description, '{}'::jsonb,
    'active', now() - v.age, 'web', 'claim-catalog-v1', v.amount
FROM (VALUES
    (md5(:'sub' || 'hu-m2.4-evaluation')::uuid::text, 'hu-m2.4-evaluation', 'collision', interval '3 days',
     'Calle 80 con Avenida Boyacá, Bogotá', 'Un vehículo me golpeó por detrás en el semáforo.', 435000000::bigint),
    (md5(:'sub' || 'hu-m2.4-filed')::uuid::text, 'hu-m2.4-filed', 'theft', interval '20 minutes',
     'Parqueadero del centro comercial Andino, Bogotá', 'Robaron el espejo retrovisor y el radio del vehículo.', NULL)
) AS v(id, key, incident, age, place, description, amount)
WHERE NOT EXISTS (SELECT 1 FROM claim c WHERE c.cognito_sub = :'sub' AND c.filing_key = v.key);

-- The advisor approved the first one (HU-W2.1-03): it enters evaluation and
-- the triage of claims gives it its level and its estimated days.
INSERT INTO claim_review (claim_id, seq, outcome, reviewed_by, reviewer_name, reviewed_at)
SELECT c.id, 1, 'approved', 'asesor@example.com', 'Asesor de prueba', now() - interval '2 days'
FROM claim c
WHERE c.cognito_sub = :'sub' AND c.filing_key = 'hu-m2.4-evaluation'
  AND NOT EXISTS (SELECT 1 FROM claim_review r WHERE r.claim_id = c.id);
UPDATE claim SET status = 'in_evaluation'
WHERE cognito_sub = :'sub' AND filing_key = 'hu-m2.4-evaluation' AND status = 'active';
SQL

cat <<EOF

Ready. claims answers at $CLAIMS (from the emulator: http://10.0.2.2:$CLAIMS_PORT).
Build and install the debug APK against it (PowerShell, in the mobile worktree):
  \$env:URL_API_LOCAL = "http://10.0.2.2:$CLAIMS_PORT"
  .\\gradlew.bat installDebug
Sign in as asegurado@example.com on the emulator (the password never goes through this script).
"Mis siniestros" lists a claim just filed and one in evaluation (criteria 1 and 2).
Criterion 3 (automatic resolution):  SUBJECT=$SUBJECT bash tests/e2e/hu-m2.4/app-local.sh --payout [FLIGHT] [MINUTES]
Criterion 4 (stale reads):           bash tests/e2e/hu-m2.4/app-local.sh --slow-db   (then --fast-db)
Criterion 5 (offline):               turn the network of the emulator off
Stop:                                docker compose -f tests/e2e/hu-m2.4/docker-compose.yml down -v
EOF
