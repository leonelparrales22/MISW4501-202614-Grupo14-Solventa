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
#   R1-R11     HU-M1.3-02, the revocation: R1 the record exists when the 201
#              arrives; R2 blocked at once (offer 422, no new connection to
#              the Open Finance proxy nor query to the stub); R3 the entity
#              confirms within the deadline (5 s here, 300 s in AWS); R4 the
#              same key returns the same revocation, another key is 409; R5 an
#              expired consent is 409; R6 isolation and roles; R7 an entity
#              that never answers leaves it partially revoked; R8 the new
#              tables are append-only; R9 the receipt in PDF; R10 latency of
#              the POST (smoke only); R11 the time of a request confirmed
#              offline is kept
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
# HU-M1.3-02: the Open Finance stub, the Toxiproxy API and the shortened
# deadline of the notices to the entities.
export OPENFINANCE_PORT="${OPENFINANCE_PORT:-9390}" TOXIPROXY_PORT="${TOXIPROXY_PORT:-8394}"
export REVOCATION_DEADLINE_SECONDS="${REVOCATION_DEADLINE_SECONDS:-5}"
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

grant_body() { # <customer> <application> <document> [partner]
  cat <<JSON
{"customer_id":"$1","document_id":"$3","full_name":"Asegurado De Prueba","terms_version":"v1",
 "accepted_sources":["open_finance","open_data","kyc"],"accepted_data_policy":true,"channel":"embedded",
 "partner_id":"${4:-aliado-banco-1}","application_id":"$2"}
JSON
}

# --- HU-M1.3-02 ---

# uuid: a random UUID (version 4), the Idempotency-Key the app generates.
uuid() {
  local hex
  hex="$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  echo "${hex:0:8}-${hex:8:4}-4${hex:13:3}-a${hex:17:3}-${hex:20:12}"
}

# revoke <consent> <token> <key> <requested_at>: the holder's revocation; sets
# STATUS, BODY and SECONDS_TAKEN (the time of the request).
revoke() {
  local args=(-s -o "$WORK/body" -w '%{http_code} %{time_total}' -X POST "$AUDIT/v1/consents/$1/revocations"
    -H 'User-Agent: hu-m1.3-e2e' -H 'Content-Type: application/json' -H "Idempotency-Key: $3"
    -d "{\"requested_at\":\"$4\",\"device\":\"e2e Pixel 8\",\"confirmation_method\":\"biometric\"}")
  [[ -n "$2" ]] && args+=(-H "Authorization: Bearer $2")
  local out
  out="$(curl "${args[@]}")"
  STATUS="${out%% *}"
  SECONDS_TAKEN="${out##* }"
  BODY="$(cat "$WORK/body")"
  printf '%s\n' "$BODY" >>"$WORK/holder-responses"
}

# offer_body <customer> <consent> <application>: the personalized offer of
# the partner (POST /v1/offers, HU-W1.2-03), which verifies the consent first.
offer_body() {
  cat <<JSON
{"customer_id":"$1","consent_id":"$2","partner_id":"aliado-banco-1","application_id":"$3",
 "document_id":"$DOC_A","full_name":"Asegurado De Prueba","email":"oferta.$RUN_ID@example.com","birth_date":"1985-03-14",
 "insured_amount":250000000,"term_months":120}
JSON
}

# profile_requests <customer>: profile queries of the customer the stub received.
profile_requests() { $COMPOSE logs --no-log-prefix openfinance 2>/dev/null | grep -c "profile request customer=$1\$" || true; }

# profile_connections: connections origination opened to the Open Finance
# proxy (the notices of audit go through another proxy).
profile_connections() {
  $COMPOSE logs --no-log-prefix toxiproxy 2>/dev/null | grep 'Accepted client' | grep -c '"name":"openfinance"' || true
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

echo "== [$(stamp)] HU-M1.3-02. Revocation of a consent (R1-R11)"
# Consents of A for the revocation: R_ONLINE (revoked online, R1-R4, R9,
# R10), R_MUTE (its entity never answers, R7) and R_OFFLINE (granted a day ago
# and revoked with the time of a request confirmed two hours ago, R11).
call POST "$AUDIT/v1/audit/consents" "$(grant_body "cust-r1-$RUN_ID" "app-r1-$RUN_ID" "$DOC_A")"
check "a consent of A to revoke online" 201 "$STATUS"
R_ONLINE="$(field .consent_id)"
call POST "$AUDIT/v1/audit/consents" "$(grant_body "cust-r3-$RUN_ID" "app-r3-$RUN_ID" "$DOC_A" aliado-mudo)"
check "a consent of A whose entity never answers" 201 "$STATUS"
R_MUTE="$(field .consent_id)"
R_OFFLINE="cns-offline-$RUN_ID"
psql_audit -c "INSERT INTO consent_record (consent_id, customer_id, holder_document_id, holder_full_name, terms_version, scope,
  purpose, channel, partner_id, application_id, user_agent, granted_at)
  SELECT '$R_OFFLINE', 'cust-r11-$RUN_ID', holder_document_id, holder_full_name, terms_version, scope, purpose, channel, partner_id,
    'app-r11-$RUN_ID', 'seed', now() - interval '1 day' FROM consent_record WHERE consent_id = '$R_ONLINE'" >/dev/null

echo "== [$(stamp)] R2 (before). With the consent active the offer queries Open Finance"
call POST "$ORIGINATION/v1/offers" "$(offer_body "cust-r1-$RUN_ID" "$R_ONLINE" "app-offer1-$RUN_ID")"
check "POST /v1/offers answers 201 while the consent is active" 201 "$STATUS"
check "the stub received the profile query of the customer" true \
  "$([[ "$(profile_requests "cust-r1-$RUN_ID")" -ge 1 ]] && echo true || echo false)"

echo "== [$(stamp)] R1. A revokes online: the record exists when the 201 arrives"
KEY_1="$(uuid)"
REQUESTED_1="$(date -u -d '-2 seconds' +%Y-%m-%dT%H:%M:%SZ)"
revoke "$R_ONLINE" "$TOKEN_A" "$KEY_1" "$REQUESTED_1"
check "POST /v1/consents/{id}/revocations answers 201" 201 "$STATUS"
RECEIPT_1="$(field .receipt_number)"
EFFECTIVE_1="$(field .effective_at)"
R1_SECONDS="$SECONDS_TAKEN"
check "the receipt has the shape REV-<year>-<number>" true "$([[ "$RECEIPT_1" =~ ^REV-[0-9]{4}-[0-9]{4,}$ ]] && echo true || echo false)"
check "status revoked at once, entity pending" "revoked|in_progress|pending" "$(field '"\(.status)|\(.notice_status)|\(.entities[0].status)"')"
check "requested_at is the time of the device" "$REQUESTED_1" "$(field '.requested_at | sub("\\.[0-9]+";"")')"
check "the immutable record has every field of CA-4" 1 "$(psql_audit -c "SELECT count(*) FROM consent_revocation
  WHERE consent_id = '$R_ONLINE' AND receipt_number = '$RECEIPT_1' AND holder_document_id = '$DOC_A' AND jsonb_array_length(scope) = 3
  AND requested_at = '$REQUESTED_1' AND effective_at >= requested_at AND channel = 'mobile_app' AND device = 'e2e Pixel 8'
  AND confirmation_method = 'biometric' AND actor_type = 'holder' AND request_id = '$KEY_1'" | tr -d '\r')"
check "and the two events of the consent" "revocation_requested,revoked" \
  "$(psql_audit -c "SELECT string_agg(status, ',' ORDER BY occurred_at) FROM consent_event WHERE consent_id = '$R_ONLINE' AND entity_code IS NULL" | tr -d '\r')"
call GET "$AUDIT/v1/consents/$R_ONLINE" "" "$TOKEN_A"
check "the consent shows revoked, not revocable, with its receipt" "revoked|false|$RECEIPT_1" \
  "$(field '"\(.status)|\(.revocable)|\(.revocation_receipt_id)"')"

echo "== [$(stamp)] R2. Blocked at once: no new query reaches Open Finance"
call GET "$AUDIT/v1/audit/consent-verifications?customer_id=cust-r1-$RUN_ID"
check "audit verifies the consent as inactive" false "$(field .active)"
# A fresh origination has no pooled connection to the stub, so any query it
# made would show up as a new connection of the proxy.
$COMPOSE restart origination >/dev/null
wait_healthy "$ORIGINATION"
CONNECTIONS_BEFORE="$(profile_connections)"
QUERIES_BEFORE="$(profile_requests "cust-r1-$RUN_ID")"
call POST "$ORIGINATION/v1/offers" "$(offer_body "cust-r1-$RUN_ID" "$R_ONLINE" "app-offer2-$RUN_ID")"
check "POST /v1/offers with the revoked consent answers 422" 422 "$STATUS"
check "the error is consent_required" consent_required "$(field .error)"
check "no new connection reached the Open Finance proxy" "$CONNECTIONS_BEFORE" "$(profile_connections)"
check "no new profile query reached the stub" "$QUERIES_BEFORE" "$(profile_requests "cust-r1-$RUN_ID")"

echo "== [$(stamp)] R3. The entity confirms within the deadline"
for _ in $(seq 1 30); do
  call GET "$AUDIT/v1/consents/$R_ONLINE" "" "$TOKEN_A"
  [[ "$(field '.revocation.entities[0].status')" == confirmed ]] && break
  sleep 0.5
done
check "the entity confirmed" "confirmed|completed" "$(field '"\(.revocation.entities[0].status)|\(.revocation.notice_status)"')"
CONFIRM_SECONDS="$(field '((.revocation.entities[0].confirmed_at | sub("\\.[0-9]+";"") | fromdate) - (.revocation.effective_at | sub("\\.[0-9]+";"") | fromdate))')"
echo "        confirmed ~${CONFIRM_SECONDS} s after the effective time (deadline ${REVOCATION_DEADLINE_SECONDS} s; 300 s in AWS)"
check "confirmed_at - effective_at is within the deadline" true \
  "$([[ "$CONFIRM_SECONDS" -le "$REVOCATION_DEADLINE_SECONDS" ]] && echo true || echo false)"
check "the history shows the confirmation of the entity" "revocation_confirmed|aliado-banco-1" \
  "$(field '.history[-1] | "\(.status)|\(.entity_code)"')"
check "the stub recorded it once, with the key of the notice" 1 \
  "$(curl -s "http://localhost:$OPENFINANCE_PORT/debug/consent-revocations" | jq --arg k "revocation-$R_ONLINE-aliado-banco-1" '[.revocations[] | select(.idempotency_key == $k)] | length')"

echo "== [$(stamp)] R4. Idempotent: the same key returns the same revocation"
revoke "$R_ONLINE" "$TOKEN_A" "$KEY_1" "$REQUESTED_1"
check "the same key answers 200" 200 "$STATUS"
check "with the same receipt" "$RECEIPT_1" "$(field .receipt_number)"
revoke "$R_ONLINE" "$TOKEN_A" "$(uuid)" "$REQUESTED_1"
check "another key on the revoked consent answers 409" 409 "$STATUS"
check "with consent_already_revoked (CA-6)" consent_already_revoked "$(field .error)"
check "still one revocation" 1 "$(psql_audit -c "SELECT count(*) FROM consent_revocation WHERE consent_id = '$R_ONLINE'" | tr -d '\r')"

echo "== [$(stamp)] R5. An expired consent cannot be revoked"
revoke "cns-expired-$RUN_ID" "$TOKEN_A" "$(uuid)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
check "409 consent_not_active" "409|consent_not_active" "$STATUS|$(field .error)"
call GET "$AUDIT/v1/consents/cns-expired-$RUN_ID" "" "$TOKEN_A"
check "an expired consent is not revocable" false "$(field .revocable)"

echo "== [$(stamp)] R6. Only the holder revokes; roles"
revoke "$R_OFFLINE" "$TOKEN_B" "$(uuid)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
check "B revoking A's consent: 404" 404 "$STATUS"
revoke "$R_OFFLINE" "$OPERATIONS_TOKEN" "$(uuid)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
check "operations: 403" 403 "$STATUS"
revoke "$R_OFFLINE" "" "$(uuid)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
check "without a token: 401" 401 "$STATUS"
call GET "$AUDIT/v1/consents/$R_OFFLINE" "" "$TOKEN_A"
check "A's consent is still active" active "$(field .status)"

echo "== [$(stamp)] R7. An entity that never answers: partial revocation when the deadline passes"
revoke "$R_MUTE" "$TOKEN_A" "$(uuid)" "$(date -u -d '-1 seconds' +%Y-%m-%dT%H:%M:%SZ)"
check "the revocation is recorded (201)" 201 "$STATUS"
for _ in $(seq 1 40); do
  call GET "$AUDIT/v1/consents/$R_MUTE" "" "$TOKEN_A"
  [[ "$(field .status)" == partially_revoked ]] && break
  sleep 0.5
done
check "partially revoked after the deadline" "partially_revoked|partial|not_confirmed" \
  "$(field '"\(.status)|\(.revocation.notice_status)|\(.revocation.entities[0].status)"')"
check "the worker wrote revocation_not_confirmed" 1 \
  "$(psql_audit -c "SELECT count(*) FROM consent_event WHERE consent_id = '$R_MUTE' AND entity_code = 'aliado-mudo' AND status = 'revocation_not_confirmed'" | tr -d '\r')"
check "the stub refused the notices of that entity" true \
  "$(curl -s "http://localhost:$OPENFINANCE_PORT/debug/consent-revocations" | jq '.failures_injected > 0')"
call GET "$AUDIT/v1/audit/consent-verifications?customer_id=cust-r3-$RUN_ID"
check "a partially revoked consent is not verified as active either" false "$(field .active)"

echo "== [$(stamp)] R8. The revocation tables are append-only"
for stmt in "UPDATE consent_revocation SET device = 'x'" "DELETE FROM consent_revocation" "TRUNCATE consent_revocation" \
  "UPDATE revocation_notice SET idempotency_key = 'x'" "DELETE FROM revocation_notice" "TRUNCATE revocation_notice" \
  "UPDATE revocation_notice_step SET status = 'confirmed'" "DELETE FROM revocation_notice_step" "TRUNCATE revocation_notice_step"; do
  OUT="$(psql_audit -c "$stmt")"
  if grep -q "append-only" <<<"$OUT"; then
    check "$stmt is rejected" rejected rejected
  else
    check "$stmt is rejected" rejected "accepted: $OUT"
  fi
done
check "the record is intact" 1 "$(psql_audit -c "SELECT count(*) FROM consent_revocation WHERE consent_id = '$R_ONLINE' AND device = 'e2e Pixel 8'" | tr -d '\r')"

echo "== [$(stamp)] R9. The receipt in PDF"
RECEIPT_OUT="$(curl -s -o "$WORK/receipt.pdf" -w '%{http_code}|%{content_type}' -H "Authorization: Bearer $TOKEN_A" \
  "$AUDIT/v1/consents/$R_ONLINE/revocation-receipt")"
check "A downloads the receipt" "200|application/pdf" "$RECEIPT_OUT"
check "it is a PDF" "%PDF-" "$(head -c 5 "$WORK/receipt.pdf")"
check "B gets 404 for A's receipt" 404 \
  "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN_B" "$AUDIT/v1/consents/$R_ONLINE/revocation-receipt")"
check "an active consent has no receipt" 404 \
  "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN_A" "$AUDIT/v1/consents/$R_OFFLINE/revocation-receipt")"

echo "== [$(stamp)] R10. Latency of the revocation (smoke only, not evidence)"
echo "        POST /v1/consents/{id}/revocations took ${R1_SECONDS} s"
check "the revocation answered in less than 5 s" true "$(awk -v s="$R1_SECONDS" 'BEGIN { print (s < 5) ? "true" : "false" }')"

echo "== [$(stamp)] R11. A request queued offline keeps its time"
REQUESTED_11="$(date -u -d '-2 hours' +%Y-%m-%dT%H:%M:%SZ)"
revoke "$R_OFFLINE" "$TOKEN_A" "$(uuid)" "$REQUESTED_11"
check "the queued revocation is recorded (201)" 201 "$STATUS"
check "requested_at is the time of the confirmation, two hours ago" "$REQUESTED_11" "$(field '.requested_at | sub("\\.[0-9]+";"")')"
check "effective_at is the server time of the upload" true \
  "$(field '((.effective_at | sub("\\.[0-9]+";"") | fromdate) - (.requested_at | sub("\\.[0-9]+";"") | fromdate)) > 7000')"

echo "== [$(stamp)] Minimization of the revocation"
$COMPOSE logs --no-log-prefix audit >"$WORK/audit.log" 2>&1 || true
$COMPOSE logs --no-log-prefix openfinance >"$WORK/openfinance.log" 2>&1 || true
check "the audit logs never carry the document of A" 0 "$(grep -c -- "$DOC_A" "$WORK/audit.log" || true)"
check "the notices never carry the document of A" 0 "$(grep -c -- "$DOC_A" "$WORK/openfinance.log" || true)"
check "no response of /v1/consents carries the document of A" 0 "$(grep -c -- "$DOC_A" "$WORK/holder-responses" || true)"
if grep -q "consent revocation key=revocation-$R_ONLINE-aliado-banco-1" "$WORK/openfinance.log"; then
  check "the stub logs each notice on one line (the evidence in CloudWatch)" yes yes
else
  check "the stub logs each notice on one line (the evidence in CloudWatch)" yes no
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
  echo "== HU-M1.3-01 and HU-M1.3-02 e2e: $FAILS check(s) failed" >&2
  exit 1
fi
echo "== HU-M1.3-01 and HU-M1.3-02 e2e passed."
