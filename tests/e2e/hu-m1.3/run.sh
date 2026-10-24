#!/usr/bin/env bash
# Runs the HU-M1.3-01 end-to-end scenarios with Docker, against the real
# origination and audit services (PostgreSQL of each, LocalStack with the SNS
# topic of origination and the SQS queue of audit with its DLQ, Moto as
# Cognito):
#
#   E1   the insureds A and B register (POST /v1/registrations) and their link
#        reaches audit by the outbox, SNS and SQS (GET /v1/consents stops
#        answering 403 holder_not_linked)
#   E2   CA-1  A lists their consents grouped: active and revoked or expired
#   E3   CA-2  the detail of a consent of A
#   E4   CA-3  the history: expired with its end, revoked with its receipt
#   E5   CA-4  read-only: no other method; UPDATE/DELETE/TRUNCATE rejected (psql)
#   E6   CA-6  B sees only theirs; A's id is 404 for B; 401; 403 for operations;
#              a Moto user who never registered gets 403 holder_not_linked
#   E7         idempotent consumer: the same event again and a conflicting one
#   E8         audit down: C registers, the message waits in SQS, audit comes
#              back and C is linked
#   E9         durable outbox: LocalStack down, D registers, the event stays in
#              the outbox with attempts; LocalStack back, D is linked
#   E10        minimization: neither the responses nor the logs of audit carry
#              a document
#   nube.sh    the API checks of the cloud window, rehearsed against audit
#
# The script exits with a non-zero code when any check fails. The environment
# is always removed at the end (down -v). Requirements: Docker (compose v2),
# curl, jq and the AWS CLI (to seed the local Moto and, in E7, to publish to
# the local LocalStack with --endpoint-url, decision #27). The secrets
# (PostgreSQL, Moto users, passwords of the insureds) are random, generated
# here and never printed.
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
export AUDIT_PORT="${AUDIT_PORT:-8390}" ORIGINATION_PORT="${ORIGINATION_PORT:-8391}"
export LOCALSTACK_PORT="${LOCALSTACK_PORT:-4576}" MOTO_PORT="${MOTO_PORT:-5130}"
AUDIT="http://localhost:$AUDIT_PORT"
ORIGINATION="http://localhost:$ORIGINATION_PORT"
RUN_ID="r$(date +%s)"

secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
INSURED_PASSWORD="Sv$(secret 16)7!"
export PG_PASSWORD MOTO_USER_PASSWORD

WORK="$(mktemp -d)"
cleanup() {
  $COMPOSE down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
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

# call <method> <url> [json body] [bearer token]: sets STATUS and BODY. The
# bodies of the holder's routes are kept for E10.
call() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -X "$1" "$2" -H 'User-Agent: hu-m1.3-e2e')
  [[ -n "${3:-}" ]] && args+=(-H 'Content-Type: application/json' -d "$3")
  [[ -n "${4:-}" ]] && args+=(-H "Authorization: Bearer $4")
  STATUS="$(curl "${args[@]}")"
  BODY="$(cat "$WORK/body")"
  if [[ "$2" == *"/v1/consents"* ]]; then
    printf '%s\n' "$BODY" >>"$WORK/holder-responses"
  fi
}
field() { jq -r "$1" <<<"$BODY"; }

wait_healthy() { # <url>
  local i
  for i in $(seq 1 90); do
    curl -sf "$1/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "$1 did not become healthy" >&2
  $COMPOSE logs audit origination | tail -40 >&2
  return 1
}

psql_audit() { $COMPOSE exec -T postgres-audit psql -U audit -d audit -v ON_ERROR_STOP=1 -tA "$@" 2>&1 || true; }
psql_orig() { $COMPOSE exec -T postgres-origination psql -U origination -d origination -v ON_ERROR_STOP=1 -tA -c "$1" 2>&1 | tr -d '\r' || true; }

# awsl: the AWS CLI against the local LocalStack only, with fake credentials.
awsl() {
  env -u AWS_PROFILE -u AWS_DEFAULT_PROFILE AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_PAGER="" \
    aws --endpoint-url "http://127.0.0.1:$LOCALSTACK_PORT" --region us-east-2 --output text "$@" | tr -d '\r'
}
QUEUE_URL="http://127.0.0.1:$LOCALSTACK_PORT/000000000000/solventa-local-audit"
DLQ_URL="http://127.0.0.1:$LOCALSTACK_PORT/000000000000/solventa-local-audit-dlq"
TOPIC_ARN="arn:aws:sns:us-east-2:000000000000:solventa-local-origination"
queue_depth() { # <queue url>: visible + in flight
  awsl sqs get-queue-attributes --queue-url "$1" \
    --attribute-names ApproximateNumberOfMessages ApproximateNumberOfMessagesNotVisible \
    --query 'sum([to_number(Attributes.ApproximateNumberOfMessages), to_number(Attributes.ApproximateNumberOfMessagesNotVisible)])'
}

# face <fill>: one face of the identity document, a JPEG signature and 2 KiB.
face() { { printf '\xff\xd8\xff\xe0'; head -c 2048 /dev/zero | tr '\0' "$1"; } | base64 | tr -d '\n\r'; }
FRONT="$(face A)"
BACK="$(face B)"

# register <document> <email>: signs the insured up; sets CUSTOMER_ID and SUBJECT.
register() {
  call POST "$ORIGINATION/v1/registrations" "{\"document_id\":\"$1\",\"full_name\":\"Asegurado De Prueba\",\"email\":\"$2\",
    \"birth_date\":\"1990-05-17\",\"password\":\"$INSURED_PASSWORD\",\"document_front\":\"$FRONT\",\"document_back\":\"$BACK\"}"
  CUSTOMER_ID="$(field .customer_id)"
  SUBJECT="$(field .subject)"
}

# wait_linked <token> <seconds>: waits until GET /v1/consents is no longer
# 403 holder_not_linked; sets LINK_STATUS and LINK_SECONDS.
wait_linked() {
  local i
  for i in $(seq 1 "$2"); do
    call GET "$AUDIT/v1/consents" "" "$1"
    if [[ "$STATUS" != 403 ]]; then
      LINK_STATUS="$STATUS"
      LINK_SECONDS="$i"
      return 0
    fi
    sleep 1
  done
  LINK_STATUS="$STATUS"
  LINK_SECONDS="$2"
}

grant_body() { # <customer> <application> <document>
  cat <<JSON
{"customer_id":"$1","document_id":"$3","full_name":"Asegurado De Prueba","terms_version":"v1",
 "accepted_sources":["open_finance","open_data","kyc"],"accepted_data_policy":true,"channel":"embedded",
 "partner_id":"aliado-banco-1","application_id":"$2"}
JSON
}

DOC_A="1020304050"
DOC_B="9988776655"
DOC_C="1122334455"
DOC_D="5566778899"
EMAIL_A="a.$RUN_ID@example.com"
EMAIL_B="b.$RUN_ID@example.com"
EMAIL_C="c.$RUN_ID@example.com"
EMAIL_D="d.$RUN_ID@example.com"

echo "== [$(stamp)] Starting PostgreSQL, Redis, LocalStack (SNS, SQS) and Moto"
$COMPOSE up -d --build --wait postgres-audit postgres-origination redis localstack moto

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
COGNITO_USER_POOL_ID="${AUTH_ISSUER##*/}"
AUTH_JWKS_URL="http://moto:5000/$COGNITO_USER_POOL_ID/.well-known/jwks.json"
export AUTH_ISSUER AUTH_CLIENT_IDS AUTH_JWKS_URL COGNITO_USER_POOL_ID
if [[ -z "$AUTH_ISSUER" || -z "$WEB_CLIENT_ID" ]]; then
  echo "Could not read the pool and the client from seed.sh" >&2
  exit 1
fi
token() { # <username> <password>
  curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
    -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$WEB_CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"$1\",\"PASSWORD\":\"$2\"}}" |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken'
}
OPERATIONS_TOKEN="$(token operaciones.local@example.com "$MOTO_USER_PASSWORD")"
UNLINKED_TOKEN="$(token asegurado.local@example.com "$MOTO_USER_PASSWORD")"

echo "== [$(stamp)] Starting origination and audit"
$COMPOSE up -d --build origination audit
wait_healthy "$ORIGINATION"
wait_healthy "$AUDIT"
$COMPOSE logs origination audit | grep -E "domain events|outbox|insured consents|EVENTS_" || true

echo "== [$(stamp)] E1. A and B register; the link reaches audit by the outbox, SNS and SQS"
register "$DOC_A" "$EMAIL_A"
check "A registers (201)" 201 "$STATUS"
CUSTOMER_A="$CUSTOMER_ID"
SUBJECT_A="$SUBJECT"
register "$DOC_B" "$EMAIL_B"
check "B registers (201)" 201 "$STATUS"
TOKEN_A="$(token "$EMAIL_A" "$INSURED_PASSWORD")"
TOKEN_B="$(token "$EMAIL_B" "$INSURED_PASSWORD")"
wait_linked "$TOKEN_A" 15
check "A is linked within 15 s (GET /v1/consents answers 200)" 200 "$LINK_STATUS"
echo "        linked after ~${LINK_SECONDS} s"
wait_linked "$TOKEN_B" 15
check "B is linked within 15 s" 200 "$LINK_STATUS"
check "the outbox of origination has both events published" 2 \
  "$(psql_orig "SELECT count(*) FROM outbox_event WHERE event_type = 'CustomerLinked' AND published_at IS NOT NULL")"
check "audit keeps the link of A with its document" "$DOC_A" \
  "$(psql_audit -c "SELECT document_id FROM holder_link WHERE subject = '$SUBJECT_A'" | tr -d '\r')"

echo "== [$(stamp)] E2. CA-1: A lists their consents, active and revoked or expired"
call POST "$AUDIT/v1/audit/consents" "$(grant_body "cust-a" "app-a1-$RUN_ID" "$DOC_A")"
check "a first consent of A (screen E1, partner route)" 201 "$STATUS"
ACTIVE_OLD="$(field .consent_id)"
sleep 1
call POST "$AUDIT/v1/audit/consents" "$(grant_body "cust-a" "app-a2-$RUN_ID" "$DOC_A")"
check "a second consent of A" 201 "$STATUS"
ACTIVE_NEW="$(field .consent_id)"
call POST "$AUDIT/v1/audit/consents" "$(grant_body "cust-b" "app-b1-$RUN_ID" "$DOC_B")"
check "a consent of B" 201 "$STATUS"
CONSENT_B="$(field .consent_id)"
psql_audit -v holder_document="$DOC_A" -v suffix="$RUN_ID" -f - <seed-history.sql >/dev/null
call GET "$AUDIT/v1/consents" "" "$TOKEN_A"
check "A lists (200)" 200 "$STATUS"
check "A has four consents" 4 "$(field '.items | length')"
check "two are current (active)" 2 "$(field '[.items[] | select(.current)] | length')"
check "two are history (revoked or expired)" 2 "$(field '[.items[] | select(.current | not)] | length')"
check "the current ones come first, newest first" "$ACTIVE_NEW,$ACTIVE_OLD" "$(field '[.items[0,1].consent_id] | join(",")')"
check "each one names the entity" "Banco Aliado" "$(field '[.items[].entities[0].name] | unique | join(",")')"
check "each one has granted_at, expires_at and status" 4 \
  "$(field '[.items[] | select(.granted_at != null and .expires_at != null and .status != null)] | length')"
check "no-store" "no-store" "$(curl -s -o /dev/null -D - -H "Authorization: Bearer $TOKEN_A" "$AUDIT/v1/consents" | tr -d '\r' | sed -n 's/^Cache-Control: //Ip')"

echo "== [$(stamp)] E3. CA-2: the detail of a consent"
call GET "$AUDIT/v1/consents/$ACTIVE_NEW" "" "$TOKEN_A"
check "A reads the detail (200)" 200 "$STATUS"
check "identifier" "$ACTIVE_NEW" "$(field .consent_id)"
check "authorized entity" "aliado-banco-1|Banco Aliado" "$(field '.entities[0] | "\(.code)|\(.name)"')"
check "scope: the three sources" "open_finance,open_data,kyc" "$(field '[.scope[].code] | join(",")')"
check "purpose" risk_profiling_mortgage_life_offer "$(field .purpose)"
check "channel of the grant" embedded "$(field .channel)"
check "status" active "$(field .status)"
check "history starts with the grant, with date and time" "granted|true" "$(field '.history[0] | "\(.status)|\(.occurred_at != null)"')"
check "validity (expires_at one year after granted_at)" true \
  "$(field '((.expires_at | sub("\\.[0-9]+";"") | fromdate) - (.granted_at | sub("\\.[0-9]+";"") | fromdate)) > 360*86400')"

echo "== [$(stamp)] E4. CA-3: the history, with its end and the receipt"
call GET "$AUDIT/v1/consents/cns-revoked-$RUN_ID" "" "$TOKEN_A"
check "the revoked one (200)" 200 "$STATUS"
check "status revoked, not current" "revoked|false" "$(field '"\(.status)|\(.current)"')"
check "its receipt" "REV-${RUN_ID^^}" "$(field .revocation_receipt_id)"
check "it ended at the revocation" true "$(field '.ended_at != null')"
check "the history shows the request and the revocation" "granted,revocation_requested,revoked" "$(field '[.history[].status] | join(",")')"
call GET "$AUDIT/v1/consents/cns-expired-$RUN_ID" "" "$TOKEN_A"
check "the expired one: status expired, ended at expires_at" "expired|true" "$(field '"\(.status)|\(.ended_at == .expires_at)"')"
check "its history ends with the expiry" expired "$(field '.history[-1].status')"
call GET "$AUDIT/v1/consents" "" "$TOKEN_A"
check "the history comes last, the latest end first" "cns-revoked-$RUN_ID,cns-expired-$RUN_ID" "$(field '[.items[2,3].consent_id] | join(",")')"

echo "== [$(stamp)] E5. CA-4: read-only"
for method in POST PUT PATCH DELETE; do
  call "$method" "$AUDIT/v1/consents" '{}' "$TOKEN_A"
  check "$method /v1/consents is 405" 405 "$STATUS"
  call "$method" "$AUDIT/v1/consents/$ACTIVE_NEW" '{"status":"revoked"}' "$TOKEN_A"
  check "$method /v1/consents/{id} is 405" 405 "$STATUS"
done
for stmt in "UPDATE consent_event SET status = 'active'" "DELETE FROM consent_event" "TRUNCATE consent_event" \
  "UPDATE holder_link SET document_id = '0000000000'" "DELETE FROM holder_link" "TRUNCATE holder_link" \
  "UPDATE consent_record SET purpose = 'x'" "DELETE FROM consent_record" "TRUNCATE consent_record"; do
  OUT="$(psql_audit -c "$stmt")"
  if grep -q "append-only" <<<"$OUT"; then
    check "$stmt is rejected" rejected rejected
  else
    check "$stmt is rejected" rejected "accepted: $OUT"
  fi
done
call GET "$AUDIT/v1/consents/$ACTIVE_NEW" "" "$TOKEN_A"
check "the consent is intact" active "$(field .status)"

echo "== [$(stamp)] E6. CA-6: only the holder sees their consents"
call GET "$AUDIT/v1/consents" "" "$TOKEN_B"
check "B lists only their consent" "$CONSENT_B" "$(field '[.items[].consent_id] | join(",")')"
call GET "$AUDIT/v1/consents/$ACTIVE_NEW" "" "$TOKEN_B"
check "A's consent is 404 for B" 404 "$STATUS"
call GET "$AUDIT/v1/consents"
check "without a token: 401" 401 "$STATUS"
call GET "$AUDIT/v1/consents" "" "$OPERATIONS_TOKEN"
check "operations: 403" 403 "$STATUS"
call GET "$AUDIT/v1/consents" "" "$UNLINKED_TOKEN"
check "an insured who never registered: 403" 403 "$STATUS"
check "with holder_not_linked" holder_not_linked "$(field .error)"

echo "== [$(stamp)] E7. The consumer is idempotent"
LINKS_BEFORE="$(psql_audit -c "SELECT count(*) FROM holder_link" | tr -d '\r')"
ENVELOPE_A="$(psql_orig "SELECT payload::text FROM outbox_event WHERE event_id = 'customer-linked-$CUSTOMER_A'")"
awsl sns publish --topic-arn "$TOPIC_ARN" --message "$ENVELOPE_A" >/dev/null
CONFLICT="$(jq -c --arg id "customer-linked-conflict-$RUN_ID" '.event_id = $id | .payload.document_id = "7777777777"' <<<"$ENVELOPE_A")"
awsl sns publish --topic-arn "$TOPIC_ARN" --message "$CONFLICT" >/dev/null
for _ in $(seq 1 30); do
  [[ "$(queue_depth "$QUEUE_URL")" == 0 ]] && break
  sleep 1
done
check "the queue is drained" 0 "$(queue_depth "$QUEUE_URL")"
check "no new link row" "$LINKS_BEFORE" "$(psql_audit -c "SELECT count(*) FROM holder_link" | tr -d '\r')"
check "A keeps the first document" "$DOC_A" "$(psql_audit -c "SELECT document_id FROM holder_link WHERE subject = '$SUBJECT_A'" | tr -d '\r')"
if $COMPOSE logs audit 2>/dev/null | grep -q "holder link conflict"; then
  check "the conflict is logged" yes yes
else
  check "the conflict is logged" yes no
fi
check "nothing went to the DLQ" 0 "$(queue_depth "$DLQ_URL")"

echo "== [$(stamp)] E8. audit down: the message waits in SQS"
$COMPOSE stop audit >/dev/null
register "$DOC_C" "$EMAIL_C"
check "C registers while audit is down" 201 "$STATUS"
CUSTOMER_C="$CUSTOMER_ID"
for _ in $(seq 1 15); do
  [[ "$(psql_orig "SELECT count(*) FROM outbox_event WHERE event_id = 'customer-linked-$CUSTOMER_C' AND published_at IS NOT NULL")" == 1 ]] && break
  sleep 1
done
check "origination published the event anyway" 1 \
  "$(psql_orig "SELECT count(*) FROM outbox_event WHERE event_id = 'customer-linked-$CUSTOMER_C' AND published_at IS NOT NULL")"
check "the message waits in the audit queue" 1 "$(queue_depth "$QUEUE_URL")"
$COMPOSE start audit >/dev/null
wait_healthy "$AUDIT"
TOKEN_C="$(token "$EMAIL_C" "$INSURED_PASSWORD")"
wait_linked "$TOKEN_C" 30
check "C is linked once audit is back" 200 "$LINK_STATUS"

echo "== [$(stamp)] E9. The outbox is durable: LocalStack down"
$COMPOSE stop localstack >/dev/null
register "$DOC_D" "$EMAIL_D"
check "D registers while the broker is down" 201 "$STATUS"
CUSTOMER_D="$CUSTOMER_ID"
sleep 4
ROW="$(psql_orig "SELECT (attempts > 0)::text || '|' || (published_at IS NULL)::text || '|' || (last_error <> '')::text FROM outbox_event WHERE event_id = 'customer-linked-$CUSTOMER_D'")"
check "the event is pending with attempts and its last error" "true|true|true" "$ROW"
$COMPOSE start localstack >/dev/null
$COMPOSE up -d --wait localstack >/dev/null
TOKEN_D="$(token "$EMAIL_D" "$INSURED_PASSWORD")"
wait_linked "$TOKEN_D" 150
check "D is linked once the broker is back" 200 "$LINK_STATUS"
echo "        linked ~${LINK_SECONDS} s after LocalStack came back"
check "and the event is marked published" 1 \
  "$(psql_orig "SELECT count(*) FROM outbox_event WHERE event_id = 'customer-linked-$CUSTOMER_D' AND published_at IS NOT NULL")"

echo "== [$(stamp)] E10. Minimization: no document in the responses nor in the logs of audit"
$COMPOSE logs --no-log-prefix audit >"$WORK/audit.log" 2>&1 || true
for doc in "$DOC_A" "$DOC_B" "$DOC_C" "$DOC_D"; do
  check "no response of /v1/consents carries $doc" 0 "$(grep -c -- "$doc" "$WORK/holder-responses" || true)"
  check "the audit logs never carry $doc" 0 "$(grep -c -- "$doc" "$WORK/audit.log" || true)"
done
if grep -q '"msg":"holder linked"' "$WORK/audit.log"; then
  check "audit logs each link with its event and customer" yes yes
else
  check "audit logs each link with its event and customer" yes no
fi

echo "== [$(stamp)] nube.sh rehearsed against this environment"
if API_URL="$AUDIT" COGNITO_ENDPOINT="http://localhost:$MOTO_PORT" COGNITO_CLIENT_ID="$WEB_CLIENT_ID" \
  USER_A_EMAIL="$EMAIL_A" USER_A_PASSWORD="$INSURED_PASSWORD" USER_B_EMAIL="$EMAIL_B" USER_B_PASSWORD="$INSURED_PASSWORD" \
  OPS_EMAIL=operaciones.local@example.com OPS_PASSWORD="$MOTO_USER_PASSWORD" bash ./nube.sh; then
  check "nube.sh passes" yes yes
else
  check "nube.sh passes" yes no
fi

echo
if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-M1.3-01 e2e: $FAILS check(s) failed" >&2
  exit 1
fi
echo "== HU-M1.3-01 e2e passed."
