#!/usr/bin/env bash
# Runs the HU-W4.2-02 end-to-end scenarios with Docker, against the real
# audit and origination services (PostgreSQL of each, Redis, the Open Finance
# stub behind Toxiproxy and Moto as Cognito). The compliance portal revokes a
# consent of Open Finance with the process of HU-M1.3-02:
#
#   S1   CA-1  GET .../revocation of an active consent gives the holder, the
#              scope, the entities and revocable=true; a POST without reason,
#              reference and role confirmation is 400 with the three fields
#              and leaves no row
#   S2   CA-2  blocked at once: before, POST /v1/offers is 201; after the
#              revocation, origination answers 422 consent_required with no new
#              connection to the Open Finance proxy
#   S3   CA-2  the entity confirms within the deadline (5 s here, 300 s in AWS):
#              revoked / completed, one notice with its key in the stub
#   S4   CA-3  the notices proxy is disabled: partially_revoked with the entity
#              not_confirmed and the time its window ended; the proxy is enabled,
#              POST .../revocation-retries is 202, the entity confirms and it
#              becomes revoked / completed with no extra notice; a second retry
#              is 409 nothing_to_retry
#   S5   CA-4  another key on the revoked consent is 409, the same key is 200; a
#              consent revoked by the holder from the app is 409 for the portal;
#              an expired one is 409 consent_not_active and not revocable
#   S6   CA-5  the row of consent_revocation has actor, channel, reason,
#              reference, device, method and scope when the 201 arrives; the
#              tables are append-only; the holder sees it revoked and downloads
#              the receipt
#   S7   CA-6  no token 401; advisor and insured 403 and no row is written
#   S8         minimization: neither the responses nor the logs of audit carry
#              the full document nor the reference
#
# The script exits with a non-zero code when any check fails. The environment
# is always removed at the end (down -v). Requirements: Docker (compose v2),
# curl, jq and the AWS CLI (only to seed the local Moto). The secrets
# (PostgreSQL, Moto users) are random, generated here and never printed.
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
export AUDIT_PORT="${AUDIT_PORT:-8490}" ORIGINATION_PORT="${ORIGINATION_PORT:-8491}" MOTO_PORT="${MOTO_PORT:-5140}"
export OPENFINANCE_PORT="${OPENFINANCE_PORT:-9490}" TOXIPROXY_PORT="${TOXIPROXY_PORT:-8495}"
export REVOCATION_DEADLINE_SECONDS="${REVOCATION_DEADLINE_SECONDS:-5}"
AUDIT="http://localhost:$AUDIT_PORT"
ORIGINATION="http://localhost:$ORIGINATION_PORT"
RUN_ID="r$(date +%s)"

secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
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

# req <method> <url> <token> [json body] [idempotency key]: sets STATUS and
# BODY; every response is kept for the minimization check (S8).
req() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -X "$1" "$2" -H 'User-Agent: hu-w4.2-e2e (Macintosh)')
  [[ -n "${3:-}" ]] && args+=(-H "Authorization: Bearer $3")
  [[ -n "${4:-}" ]] && args+=(-H 'Content-Type: application/json' -d "$4")
  [[ -n "${5:-}" ]] && args+=(-H "Idempotency-Key: $5")
  STATUS="$(curl "${args[@]}")"
  BODY="$(cat "$WORK/body")"
  printf '%s\n' "$BODY" >>"$WORK/responses"
}
field() { jq -r "$1" <<<"$BODY"; }

# capture <name>: with CAPTURE_DIR set, keeps the last response as <name>.json
# (the examples of docs/contratos/HU-W4.2-02-api.md come from here).
capture() {
  [[ -n "${CAPTURE_DIR:-}" ]] || return 0
  mkdir -p "$CAPTURE_DIR"
  jq . "$WORK/body" >"$CAPTURE_DIR/$1.json" 2>/dev/null || cp "$WORK/body" "$CAPTURE_DIR/$1.json"
}

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

psql_audit() { $COMPOSE exec -T postgres-audit psql -U audit -d audit -v ON_ERROR_STOP=1 -tA "$@" 2>&1 | tr -d '\r' || true; }

uuid() { # a random UUID (version 4), the Idempotency-Key the portal generates
  local hex
  hex="$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  echo "${hex:0:8}-${hex:8:4}-4${hex:13:3}-a${hex:17:3}-${hex:20:12}"
}

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

portal_body() { # <reason> <reference> <role_confirmed>
  printf '{"reason":"%s","reference":"%s","role_confirmed":%s}' "$1" "$2" "$3"
}

grant_body() { # <customer> <application> <document>
  cat <<JSON
{"customer_id":"$1","document_id":"$3","full_name":"Asegurado De Prueba","terms_version":"v1",
 "accepted_sources":["open_finance","open_data","kyc"],"accepted_data_policy":true,"channel":"embedded",
 "partner_id":"aliado-banco-1","application_id":"$2"}
JSON
}

# grant <name> <document>: records a consent through the partner route and
# sets CONSENT_ID.
grant() {
  req POST "$AUDIT/v1/audit/consents" "" "$(grant_body "cust-$1-$RUN_ID" "app-$1-$RUN_ID" "$2")"
  check "a consent for $1 (partner route)" 201 "$STATUS"
  CONSENT_ID="$(field .consent_id)"
}

offer_body() { # <customer> <consent> <application>
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

# notices <key>: notices with that idempotency key that the stub accepted.
notices() {
  curl -s "http://localhost:$OPENFINANCE_PORT/debug/consent-revocations" |
    jq --arg k "$1" '[.revocations[] | select(.idempotency_key == $k)] | length'
}

# proxy <name> <true|false>: enables or disables a proxy of Toxiproxy.
proxy() {
  curl -sf -o /dev/null -X POST "http://localhost:$TOXIPROXY_PORT/proxies/$1" -H 'Content-Type: application/json' -d "{\"enabled\":$2}"
}

# state <consent>: the revocation state as the operations analyst reads it.
state() { req GET "$AUDIT/v1/audit/consents/$1/revocation" "$OPS_TOKEN"; }

# wait_state <consent> <jq expression> <expected> <tries>: polls the state
# every 0.5 s until the expression gives the expected value.
wait_state() {
  local i
  for i in $(seq 1 "$4"); do
    state "$1"
    [[ "$(field "$2")" == "$3" ]] && return 0
    sleep 0.5
  done
  return 0
}

DOC_A="1020304050"
REFERENCE="TCK-$RUN_ID"

echo "== [$(stamp)] Starting PostgreSQL, Redis and Moto"
$COMPOSE up -d --build --wait postgres-audit postgres-origination redis moto

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
AUTH_JWKS_URL="http://moto:5000/${AUTH_ISSUER##*/}/.well-known/jwks.json"
export AUTH_ISSUER AUTH_CLIENT_IDS AUTH_JWKS_URL
if [[ -z "$AUTH_ISSUER" || -z "$WEB_CLIENT_ID" ]]; then
  echo "Could not read the pool and the client from seed.sh" >&2
  exit 1
fi
token() { # <username>
  curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
    -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$WEB_CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"$1\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken'
}
subject() { # the "sub" claim of a token (base64url payload)
  local payload
  payload="$(cut -d. -f2 <<<"$1" | tr '_-' '/+')"
  while ((${#payload} % 4)); do payload="$payload="; done
  { base64 -d <<<"$payload" 2>/dev/null || base64 -D <<<"$payload"; } | jq -r .sub
}
OPS_TOKEN="$(token operaciones.local@example.com)"
ADVISOR_TOKEN="$(token asesor.local@example.com)"
INSURED_TOKEN="$(token asegurado.local@example.com)"
OPS_SUB="$(subject "$OPS_TOKEN")"
INSURED_SUB="$(subject "$INSURED_TOKEN")"

echo "== [$(stamp)] Starting origination and audit"
$COMPOSE up -d --build origination audit
wait_healthy "$ORIGINATION"
wait_healthy "$AUDIT"
$COMPOSE logs audit | grep -E "compliance revocation|revocation notices" || true

echo "== [$(stamp)] The insured is linked to their document (test data by SQL, as the contract test does)"
psql_audit -q -c "INSERT INTO holder_link (subject, document_id, customer_id, linked_at, event_id)
  VALUES ('$INSURED_SUB', '$DOC_A', 'cus-e2e-$RUN_ID', now(), 'customer-linked-e2e-$RUN_ID')" >/dev/null

grant block "$DOC_A"
W_BLOCK="$CONSENT_ID"
grant partial "$DOC_A"
W_PARTIAL="$CONSENT_ID"
grant mobile "$DOC_A"
W_MOBILE="$CONSENT_ID"
grant auth "$DOC_A"
W_AUTH="$CONSENT_ID"
grant expired "$DOC_A"
W_EXPIRED="$CONSENT_ID"
# An expired consent: granted 13 months ago (the validity is 12). Only an
# insertion, as the append-only tables of audit demand.
psql_audit -q -c "INSERT INTO consent_record (consent_id, customer_id, holder_document_id, holder_full_name, terms_version, scope,
  purpose, channel, partner_id, application_id, user_agent, granted_at)
  SELECT 'cns-old-$RUN_ID', 'cust-old-$RUN_ID', holder_document_id, holder_full_name, terms_version, scope, purpose, channel,
    partner_id, 'app-old-$RUN_ID', 'seed', now() - interval '13 months' FROM consent_record WHERE consent_id = '$W_EXPIRED'" >/dev/null
W_EXPIRED="cns-old-$RUN_ID"

echo "== [$(stamp)] S1. CA-1: the confirmation shows the holder and demands reason, reference and role"
state "$W_BLOCK"
check "GET .../revocation answers 200" 200 "$STATUS"
check "the holder: name and masked document" "Asegurado De Prueba|******4050" "$(field '"\(.holder.full_name)|\(.holder.document_masked)"')"
check "status active and revocable" "active|true" "$(field '"\(.status)|\(.revocable)"')"
check "the scope has the three sources" 3 "$(field '.scope | length')"
check "the entity" "aliado-banco-1|Banco Aliado" "$(field '.entities[0] | "\(.code)|\(.name)"')"
check "no revocation yet" null "$(field .revocation)"
capture state-active
KEY_1="$(uuid)"
req POST "$AUDIT/v1/audit/consents/$W_BLOCK/revocations" "$OPS_TOKEN" '{}' "$KEY_1"
check "a POST without reason, reference nor role is 400" 400 "$STATUS"
check "naming the three fields at once" "reason:required,reference:required,role_confirmed:must_be_true" \
  "$(field '[.fields[] | "\(.field):\(.code)"] | join(",")')"
capture revoke-400
req POST "$AUDIT/v1/audit/consents/$W_BLOCK/revocations" "$OPS_TOKEN" "$(portal_body customer_request "$REFERENCE" false)" "$KEY_1"
check "role_confirmed=false is 400 too" "400|role_confirmed:must_be_true" "$STATUS|$(field '[.fields[] | "\(.field):\(.code)"] | join(",")')"
check "nothing was recorded" "0|0" \
  "$(psql_audit -c "SELECT (SELECT count(*) FROM consent_revocation WHERE consent_id = '$W_BLOCK') || '|' || (SELECT count(*) FROM consent_event WHERE consent_id = '$W_BLOCK')")"

echo "== [$(stamp)] S2. CA-2: with the consent active the offer queries Open Finance; revoked, it does not"
req POST "$ORIGINATION/v1/offers" "" "$(offer_body "cust-block-$RUN_ID" "$W_BLOCK" "app-offer1-$RUN_ID")"
check "POST /v1/offers answers 201 while the consent is active" 201 "$STATUS"
check "the stub received the profile query of the customer" true \
  "$([[ "$(profile_requests "cust-block-$RUN_ID")" -ge 1 ]] && echo true || echo false)"
req POST "$AUDIT/v1/audit/consents/$W_BLOCK/revocations" "$OPS_TOKEN" "$(portal_body customer_request "$REFERENCE" true)" "$KEY_1"
check "the portal revokes (201)" 201 "$STATUS"
capture revoke-201
RECEIPT_1="$(field .revocation.receipt_number)"
check "the receipt has the shape REV-<year>-<number>" true "$([[ "$RECEIPT_1" =~ ^REV-[0-9]{4}-[0-9]{4,}$ ]] && echo true || echo false)"
check "revoked at once, the entity pending" "revoked|false|in_progress|pending" \
  "$(field '"\(.status)|\(.revocable)|\(.revocation.notice_status)|\(.revocation.entities[0].status)"')"
req GET "$AUDIT/v1/audit/consent-verifications?customer_id=cust-block-$RUN_ID"
check "audit verifies the consent as inactive" false "$(field .active)"
# A fresh origination has no pooled connection to the stub, so any query it
# made would show up as a new connection of the proxy.
$COMPOSE restart origination >/dev/null
wait_healthy "$ORIGINATION"
CONNECTIONS_BEFORE="$(profile_connections)"
QUERIES_BEFORE="$(profile_requests "cust-block-$RUN_ID")"
req POST "$ORIGINATION/v1/offers" "" "$(offer_body "cust-block-$RUN_ID" "$W_BLOCK" "app-offer2-$RUN_ID")"
check "POST /v1/offers with the revoked consent answers 422" "422|consent_required" "$STATUS|$(field .error)"
check "no new connection reached the Open Finance proxy" "$CONNECTIONS_BEFORE" "$(profile_connections)"
check "no new profile query reached the stub" "$QUERIES_BEFORE" "$(profile_requests "cust-block-$RUN_ID")"

echo "== [$(stamp)] S3. CA-2: the entity confirms within the deadline"
wait_state "$W_BLOCK" '.revocation.entities[0].status' confirmed 40
check "the entity confirmed: revoked / completed" "revoked|completed|confirmed" \
  "$(field '"\(.status)|\(.revocation.notice_status)|\(.revocation.entities[0].status)"')"
CONFIRM_SECONDS="$(field '((.revocation.entities[0].confirmed_at | sub("\\.[0-9]+";"") | fromdate) - (.revocation.effective_at | sub("\\.[0-9]+";"") | fromdate))')"
echo "        confirmed ~${CONFIRM_SECONDS} s after the effective time (deadline ${REVOCATION_DEADLINE_SECONDS} s; 300 s in AWS)"
check "confirmed_at - effective_at is within the deadline" true \
  "$([[ "$CONFIRM_SECONDS" -le "$REVOCATION_DEADLINE_SECONDS" ]] && echo true || echo false)"
check "the stub recorded it once, with the key of the notice" 1 "$(notices "revocation-$W_BLOCK-aliado-banco-1")"

echo "== [$(stamp)] S4. CA-3: partial revocation, alert and retry of the entity that did not confirm"
proxy openfinance-revocations false
KEY_2="$(uuid)"
req POST "$AUDIT/v1/audit/consents/$W_PARTIAL/revocations" "$OPS_TOKEN" "$(portal_body regulatory_order "$REFERENCE-P" true)" "$KEY_2"
check "the revocation is recorded (201) with the notices proxy down" 201 "$STATUS"
wait_state "$W_PARTIAL" .status partially_revoked 60
check "partially revoked after the deadline" "partially_revoked|partial|not_confirmed" \
  "$(field '"\(.status)|\(.revocation.notice_status)|\(.revocation.entities[0].status)"')"
check "the alert has the time the window ended" true "$(field '.revocation.entities[0].deadline_at != null')"
capture state-partial
for _ in $(seq 1 60); do
  [[ "$(psql_audit -c "SELECT count(*) FROM consent_event WHERE consent_id = '$W_PARTIAL' AND status = 'revocation_not_confirmed'")" == 1 ]] && break
  sleep 0.5
done
check "the worker wrote revocation_not_confirmed" 1 \
  "$(psql_audit -c "SELECT count(*) FROM consent_event WHERE consent_id = '$W_PARTIAL' AND status = 'revocation_not_confirmed'")"
check "no notice reached the stub" 0 "$(notices "revocation-$W_PARTIAL-aliado-banco-1")"
req GET "$AUDIT/v1/audit/consent-verifications?customer_id=cust-partial-$RUN_ID"
check "a partially revoked consent is not verified as active" false "$(field .active)"
proxy openfinance-revocations true
req POST "$AUDIT/v1/audit/consents/$W_PARTIAL/revocation-retries" "$OPS_TOKEN"
check "the retry is accepted (202)" 202 "$STATUS"
capture retry-202
check "only the entity that did not confirm" "aliado-banco-1" "$(field '.retried_entities | join(",")')"
check "a retry keeps it partial until the entity confirms" "partially_revoked|partial|pending" \
  "$(field '"\(.state.status)|\(.state.revocation.notice_status)|\(.state.revocation.entities[0].status)"')"
req POST "$AUDIT/v1/audit/consents/$W_PARTIAL/revocation-retries" "$OPS_TOKEN"
check "a second retry with the window open is 409 nothing_to_retry" "409|nothing_to_retry" "$STATUS|$(field .error)"
capture retry-409
wait_state "$W_PARTIAL" .status revoked 60
check "the entity confirmed: revoked / completed" "revoked|completed|confirmed" \
  "$(field '"\(.status)|\(.revocation.notice_status)|\(.revocation.entities[0].status)"')"
sleep 3
check "the stub got the notice once, with the same key, and no extra one" 1 "$(notices "revocation-$W_PARTIAL-aliado-banco-1")"
check "the retry is recorded with its actor" 1 \
  "$(psql_audit -c "SELECT count(*) FROM consent_event WHERE consent_id = '$W_PARTIAL' AND status = 'revocation_retry_requested' AND actor_type = 'operations' AND actor_id = '$OPS_SUB'")"
req POST "$AUDIT/v1/audit/consents/$W_PARTIAL/revocation-retries" "$OPS_TOKEN"
check "once revoked there is nothing to retry" "409|nothing_to_retry" "$STATUS|$(field .error)"
req POST "$AUDIT/v1/audit/consents/$W_AUTH/revocation-retries" "$OPS_TOKEN"
check "an active consent has no revocation to retry" "409|consent_not_revoked" "$STATUS|$(field .error)"

echo "== [$(stamp)] S5. CA-4: a revoked or expired consent is refused"
req POST "$AUDIT/v1/audit/consents/$W_BLOCK/revocations" "$OPS_TOKEN" "$(portal_body customer_request "$REFERENCE" true)" "$(uuid)"
check "another key on the revoked consent is 409 consent_already_revoked" "409|consent_already_revoked" "$STATUS|$(field .error)"
capture revoke-409
req POST "$AUDIT/v1/audit/consents/$W_BLOCK/revocations" "$OPS_TOKEN" "$(portal_body customer_request "$REFERENCE" true)" "$KEY_1"
check "the same key is 200 with the same receipt" "200|$RECEIPT_1" "$STATUS|$(field .revocation.receipt_number)"
check "still one revocation" 1 "$(psql_audit -c "SELECT count(*) FROM consent_revocation WHERE consent_id = '$W_BLOCK'")"
req POST "$AUDIT/v1/consents/$W_MOBILE/revocations" "$INSURED_TOKEN" \
  "{\"requested_at\":\"$(now)\",\"device\":\"e2e Pixel 8\",\"confirmation_method\":\"biometric\"}" "$(uuid)"
check "the holder revokes from the app (201)" 201 "$STATUS"
req POST "$AUDIT/v1/audit/consents/$W_MOBILE/revocations" "$OPS_TOKEN" "$(portal_body customer_request "$REFERENCE" true)" "$(uuid)"
check "the portal is refused: 409 consent_already_revoked" "409|consent_already_revoked" "$STATUS|$(field .error)"
state "$W_MOBILE"
check "the portal sees who revoked it and from where" "mobile_app|holder|null" \
  "$(field '"\(.revocation.channel)|\(.revocation.requested_by.type)|\(.revocation.reason)"')"
state "$W_EXPIRED"
check "an expired consent is not revocable" "expired|false" "$(field '"\(.status)|\(.revocable)"')"
req POST "$AUDIT/v1/audit/consents/$W_EXPIRED/revocations" "$OPS_TOKEN" "$(portal_body customer_request "$REFERENCE" true)" "$(uuid)"
check "and the revocation is 409 consent_not_active" "409|consent_not_active" "$STATUS|$(field .error)"
check "nothing was recorded for the expired one" 0 "$(psql_audit -c "SELECT count(*) FROM consent_revocation WHERE consent_id = '$W_EXPIRED'")"

echo "== [$(stamp)] S6. CA-5: the immutable record, the holder's view and the receipt"
check "the row has actor, channel, reason, reference, device, method, scope and the server time" 1 "$(psql_audit -c "SELECT count(*) FROM consent_revocation
  WHERE consent_id = '$W_BLOCK' AND receipt_number = '$RECEIPT_1' AND actor_type = 'operations' AND actor_id = '$OPS_SUB'
  AND channel = 'compliance_portal' AND reason = 'customer_request' AND reference = '$REFERENCE'
  AND device LIKE 'hu-w4.2-e2e%' AND confirmation_method = 'role_attestation' AND jsonb_array_length(scope) = 3
  AND requested_at <= effective_at AND effective_at - requested_at < interval '2 seconds' AND request_id = '$KEY_1'")"
check "the events of the consent carry the actor, the reason and the reference" "revocation_requested,revoked" \
  "$(psql_audit -c "SELECT string_agg(status, ',' ORDER BY occurred_at, event_id) FROM consent_event
  WHERE consent_id = '$W_BLOCK' AND entity_code IS NULL AND actor_type = 'operations' AND reason = 'customer_request' AND reference = '$REFERENCE'")"
check "and the answer of the entity with its time" 1 \
  "$(psql_audit -c "SELECT count(*) FROM consent_event WHERE consent_id = '$W_BLOCK' AND entity_code = 'aliado-banco-1' AND status = 'revocation_confirmed'")"
state "$W_BLOCK"
check "the portal reads who, why and the reference" "operations|$OPS_SUB|customer_request|$REFERENCE" \
  "$(field '.revocation | "\(.requested_by.type)|\(.requested_by.id)|\(.reason)|\(.reference)"')"
check "the history of the consent" "granted,revocation_requested,revoked,revocation_confirmed" "$(field '[.history[].status] | join(",")')"
capture state-revoked
for stmt in "UPDATE consent_revocation SET reason = 'x'" "DELETE FROM consent_revocation" "TRUNCATE consent_revocation" \
  "UPDATE consent_event SET reference = 'x'" "DELETE FROM consent_event" "TRUNCATE consent_event" \
  "UPDATE revocation_notice_step SET status = 'confirmed'" "DELETE FROM revocation_notice_step" "TRUNCATE revocation_notice_step"; do
  OUT="$(psql_audit -c "$stmt")"
  if grep -q "append-only" <<<"$OUT"; then
    check "$stmt is rejected" rejected rejected
  else
    check "$stmt is rejected" rejected "accepted: $OUT"
  fi
done
check "the record is intact" 1 "$(psql_audit -c "SELECT count(*) FROM consent_revocation WHERE consent_id = '$W_BLOCK' AND reason = 'customer_request'")"
req GET "$AUDIT/v1/consents/$W_BLOCK" "$INSURED_TOKEN"
check "the holder sees it revoked, from the portal, with its receipt" "revoked|compliance_portal|$RECEIPT_1" \
  "$(field '"\(.status)|\(.revocation.channel)|\(.revocation_receipt_id)"')"
RECEIPT_OUT="$(curl -s -o "$WORK/receipt.pdf" -w '%{http_code}|%{content_type}' -H "Authorization: Bearer $INSURED_TOKEN" \
  "$AUDIT/v1/consents/$W_BLOCK/revocation-receipt")"
check "the holder downloads the receipt" "200|application/pdf" "$RECEIPT_OUT"
check "it is a PDF" "%PDF-" "$(head -c 5 "$WORK/receipt.pdf")"

echo "== [$(stamp)] S7. CA-6: only the operations role; nothing is executed for the rest"
ROWS_BEFORE="$(psql_audit -c "SELECT (SELECT count(*) FROM consent_revocation) || '|' || (SELECT count(*) FROM consent_event) || '|' || (SELECT count(*) FROM revocation_notice_step)")"
for who in none advisor insured; do
  case "$who" in
  none) tok="" want=401 ;;
  advisor) tok="$ADVISOR_TOKEN" want=403 ;;
  insured) tok="$INSURED_TOKEN" want=403 ;;
  esac
  req GET "$AUDIT/v1/audit/consents/$W_AUTH/revocation" "$tok"
  check "$who: GET .../revocation is $want" "$want" "$STATUS"
  req POST "$AUDIT/v1/audit/consents/$W_AUTH/revocations" "$tok" "$(portal_body customer_request "$REFERENCE" true)" "$(uuid)"
  check "$who: POST .../revocations is $want" "$want" "$STATUS"
  req POST "$AUDIT/v1/audit/consents/$W_PARTIAL/revocation-retries" "$tok"
  check "$who: POST .../revocation-retries is $want" "$want" "$STATUS"
done
check "no row was written by a refused caller" "$ROWS_BEFORE" \
  "$(psql_audit -c "SELECT (SELECT count(*) FROM consent_revocation) || '|' || (SELECT count(*) FROM consent_event) || '|' || (SELECT count(*) FROM revocation_notice_step)")"
req GET "$AUDIT/v1/audit/consent-verifications?customer_id=cust-auth-$RUN_ID"
check "the consent of the refused callers is still active" true "$(field .active)"
req GET "$AUDIT/v1/audit/consents/$W_AUTH/revocation" "$OPS_TOKEN"
check "operations reads it (200)" 200 "$STATUS"

echo "== [$(stamp)] S8. Minimization: no full document, no reference in the responses nor in the logs of audit"
$COMPOSE logs --no-log-prefix audit >"$WORK/audit.log" 2>&1 || true
$COMPOSE logs --no-log-prefix openfinance >"$WORK/openfinance.log" 2>&1 || true
check "no response carries the full document" 0 "$(grep -c -- "$DOC_A" "$WORK/responses" || true)"
check "the audit logs never carry the document" 0 "$(grep -c -- "$DOC_A" "$WORK/audit.log" || true)"
check "the audit logs never carry the reference" 0 "$(grep -c -- "$REFERENCE" "$WORK/audit.log" || true)"
check "the notices never carry the document" 0 "$(grep -c -- "$DOC_A" "$WORK/openfinance.log" || true)"
check "the notices never carry the reference" 0 "$(grep -c -- "$REFERENCE" "$WORK/openfinance.log" || true)"
check "the audit logs never carry a token" 0 "$(grep -c -- "$OPS_TOKEN" "$WORK/audit.log" || true)"

echo
if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-W4.2-02 e2e: $FAILS check(s) failed" >&2
  exit 1
fi
echo "== HU-W4.2-02 e2e passed."
