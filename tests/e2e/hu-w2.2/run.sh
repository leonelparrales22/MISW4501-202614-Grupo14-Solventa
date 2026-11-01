#!/usr/bin/env bash
# Runs the HU-W2.2-01 end-to-end scenarios with Docker, against the real claims
# service (its PostgreSQL and its sweep of the triage, the policy core stub
# behind Toxiproxy, Moto as Cognito and as the S3 of the supports):
#
#   1. CA-1  a claim filed from the portal gets its level within 60 seconds,
#            with the cause entered_evaluation and the date and time
#   2. CA-2  water damage for 4.8 million is Triage 2 (medium, low), a theft
#            without amount Triage 1, a collision for 40 million Triage 1 and
#            glass for 900,000 Triage 3
#   3. CA-3  a new amount (written with psql, as the stories of sprint 3 will
#            write it) recalculates the level with its cause; moving the claim
#            to in_evaluation does not
#   4. CA-4  the analyst reads level, severity and band of a page of claims and
#            the detail; the insured, the level and the days; anybody else 403
#   5. CA-5  the target of each level, in the policy and in each row
#   6.       a claim stored while claims is stopped gets its level when it is
#            back, and with the policy core down a change is still recalculated
#   7.       claim_triage cannot be changed with psql; neither the view of the
#            insured nor the logs carry the amount; a wrong amount is a field
#
# The script exits with a non-zero code when any check fails. The environment
# is always removed at the end (down -v). Requirements: Docker (compose v2),
# curl, jq and the AWS CLI (only to seed the local Moto, as
# tests/security/ha-s01/local does). The secrets (PostgreSQL, Moto users) are
# random, generated here and never printed.
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
export CLAIMS_PORT="${CLAIMS_PORT:-8298}" POLICY_CORE_PORT="${POLICY_CORE_PORT:-9161}"
export TOXIPROXY_PORT="${TOXIPROXY_PORT:-8496}" MOTO_PORT="${MOTO_PORT:-5124}"
CLAIMS="http://localhost:$CLAIMS_PORT"
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
CHECKS=0
check() { # <description> <expected> <actual>
  CHECKS=$((CHECKS + 1))
  if [[ "$2" == "$3" ]]; then
    echo "  ok    $1"
  else
    echo "  FAIL  $1 (expected '$2', got '$3')" >&2
    FAILS=$((FAILS + 1))
  fi
}

# call <method> <url> [json body] [bearer token] [idempotency key]: sets STATUS and BODY.
call() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -X "$1" "$2")
  [[ -n "${3:-}" ]] && args+=(-H 'Content-Type: application/json' -d "$3")
  [[ -n "${4:-}" ]] && args+=(-H "Authorization: Bearer $4")
  [[ -n "${5:-}" ]] && args+=(-H "Idempotency-Key: $5")
  STATUS="$(curl "${args[@]}")"
  BODY="$(cat "$WORK/body")"
}
field() { jq -r "$1" <<<"$BODY"; }

file_size() { wc -c <"$1" | tr -d '[:space:]'; }
file_sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}
content_type_of() {
  case "$1" in
    *.jpg) echo image/jpeg ;;
    *) echo application/pdf ;;
  esac
}

# upload <token> <path>: declare the file, PUT it to the signed URL (the S3 of
# Moto) and confirm it, as the portal does (HU-W2.1-01).
upload() {
  local file="$2" declaration id url put_status h
  local headers=()
  declaration="$(jq -nc --arg n "$(basename "$file")" --arg t "$(content_type_of "$file")" \
    --argjson s "$(file_size "$file")" --arg h "$(file_sha256 "$file")" \
    '{file_name:$n, content_type:$t, byte_size:$s, sha256:$h}')"
  call POST "$CLAIMS/v1/claims/supports" "$declaration" "$1"
  [[ "$STATUS" == 201 ]] || return 0
  id="$(field .support_id)"
  url="$(field .upload_url)"
  while IFS= read -r h; do headers+=(-H "$h"); done < <(
    jq -r '.upload_headers | to_entries[] | select(.key | ascii_downcase != "content-length") | "\(.key): \(.value)"' <<<"$BODY")
  url="$(sed "s#//moto:5000#//localhost:$MOTO_PORT#" <<<"$url")"
  put_status="$(curl -s -o "$WORK/put" -w '%{http_code}' -X PUT "$url" ${headers[@]+"${headers[@]}"} --data-binary "@$file")"
  if [[ "$put_status" != 200 ]]; then
    STATUS="$put_status"
    BODY="$(cat "$WORK/put")"
    return 0
  fi
  call POST "$CLAIMS/v1/claims/supports/$id/confirmation" "" "$1"
}

# upload_ids <token> <path> <n>: uploads the file n times and prints the ids as a JSON array.
upload_ids() {
  local ids=() i
  for i in $(seq 1 "$3"); do
    upload "$1" "$2"
    [[ "$STATUS" == 200 ]] || {
      echo "upload failed: $STATUS $BODY" >&2
      return 1
    }
    ids+=("\"$(field .support_id)\"")
  done
  (IFS=,; echo "[${ids[*]}]")
}

psql_claims() { $COMPOSE exec -T postgres-claims psql -U claims -d claims -tAc "$1"; }
calculations() { psql_claims "SELECT count(*) FROM claim_triage WHERE claim_id = '$1'" | tr -d '[:space:]'; }

wait_healthy() {
  local i
  for i in $(seq 1 90); do
    curl -sf "$CLAIMS/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "claims did not become healthy" >&2
  $COMPOSE logs claims | tail -40 >&2
  return 1
}

# wait_calculations <claim id> <n>: waits, at most the 60 seconds of
# criterion 1, until the claim has n calculations; sets WAITED (seconds) and
# leaves in BODY its triage as operations.
wait_calculations() {
  local start i
  start="$(date +%s)"
  for i in $(seq 1 60); do
    call GET "$CLAIMS/v1/claims/$1/triage" "" "$OPS_TOKEN"
    if [[ "$STATUS" == 200 && "$(field '.history | length')" == "$2" ]]; then
      WAITED=$(($(date +%s) - start))
      return 0
    fi
    sleep 1
  done
  WAITED=timeout
}

# Test files: images and PDFs are recognized by their first bytes.
printf '\xff\xd8\xff\xe0' >"$WORK/photo.jpg" && head -c 4096 /dev/urandom >>"$WORK/photo.jpg"
printf '%%PDF-1.7\n' >"$WORK/doc.pdf" && head -c 4096 /dev/urandom >>"$WORK/doc.pdf"

echo "== [$(stamp)] Starting PostgreSQL, the policy core stub behind Toxiproxy and Moto"
$COMPOSE up -d --build --wait postgres-claims policy-core toxiproxy moto
$COMPOSE up toxiproxy-init >/dev/null
curl -sf -X PUT "http://localhost:$MOTO_PORT/claims-evidence" >/dev/null

echo "== [$(stamp)] Seeding the local Cognito (Moto) and a second insured"
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
POOL_ID="${AUTH_ISSUER##*/}"
AUTH_JWKS_URL="http://moto:5000/$POOL_ID/.well-known/jwks.json"
export AUTH_ISSUER AUTH_CLIENT_IDS AUTH_JWKS_URL
if [[ -z "$AUTH_ISSUER" || -z "$WEB_CLIENT_ID" ]]; then
  echo "Could not read the pool and the client from seed.sh" >&2
  exit 1
fi
idp() {
  env -u MSYS_NO_PATHCONV AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION="${POOL_ID%%_*}" AWS_PAGER="" \
    aws --endpoint-url "http://localhost:$MOTO_PORT" cognito-idp "$@" >/dev/null
}
OTHER_USER="otro.asegurado.local@example.com"
idp admin-create-user --user-pool-id "$POOL_ID" --username "$OTHER_USER" --message-action SUPPRESS \
  --user-attributes Name=email,Value="$OTHER_USER" Name=email_verified,Value=true Name=name,Value="Andrés Gómez"
idp admin-set-user-password --user-pool-id "$POOL_ID" --username "$OTHER_USER" --password "$MOTO_USER_PASSWORD" --permanent
idp admin-add-user-to-group --user-pool-id "$POOL_ID" --username "$OTHER_USER" --group-name insured
token() { # <username>
  curl -sf -X POST "http://localhost:$MOTO_PORT/" -H 'Content-Type: application/x-amz-json-1.1' \
    -H 'X-Amz-Target: AWSCognitoIdentityProviderService.InitiateAuth' \
    -d "{\"AuthFlow\":\"USER_PASSWORD_AUTH\",\"ClientId\":\"$WEB_CLIENT_ID\",\"AuthParameters\":{\"USERNAME\":\"$1\",\"PASSWORD\":\"$MOTO_USER_PASSWORD\"}}" |
    tr -d '\r' | jq -r '.AuthenticationResult.IdToken'
}
INSURED_TOKEN="$(token asegurado.local@example.com)"
OTHER_TOKEN="$(token "$OTHER_USER")"
OPS_TOKEN="$(token operaciones.local@example.com)"
ADVISOR_TOKEN="$(token asesor.local@example.com)"
INSURED_SUB="$(cut -d. -f2 <<<"$INSURED_TOKEN" | tr '_-' '/+' | awk '{ while (length($0) % 4) $0 = $0 "="; print }' | base64 -d 2>/dev/null | jq -r .sub)"

echo "== [$(stamp)] Starting claims (sweep of the triage every 2 s)"
$COMPOSE up -d --build claims
wait_healthy
$COMPOSE logs claims | grep -E "triage|database migration" || true

CORE="$(curl -sf "http://localhost:$POLICY_CORE_PORT/holders/$INSURED_SUB/policies")"
AUTO="$(jq -r '.policies[] | select(.line=="auto" and .status=="active") | .policy_id' <<<"$CORE")"
HOME_POLICY="$(jq -r '.policies[] | select(.line=="home") | .policy_id' <<<"$CORE")"
GLASS_COVERED="$(jq -r '[.policies[] | select(.line=="auto" and .status=="active") | .covered_incidents[]] | index("glass") != null' <<<"$CORE")"
OCCURRED="$(jq -rn 'now - 2 * 86400 | floor | todateiso8601')"

# filing <key> <policy> <incident> <supports JSON> [amount in cents]: files
# from the portal; sets STATUS, BODY and CLAIM.
filing() {
  local body
  body="$(jq -nc --arg p "$2" --arg i "$3" --arg o "$OCCURRED" --argjson s "$4" \
    '{policy_id:$p, incident_type:$i, occurred_at:$o, place:"Carrera 15 # 93-40, apto 402, Bogotá",
      description:"Descripción del siniestro de la prueba de punta a punta del triage.", supports:$s}')"
  [[ -n "${5:-}" ]] && body="$(jq -c --argjson a "$5" '. + {estimated_amount_minor:$a}' <<<"$body")"
  call POST "$CLAIMS/v1/claims/web-filings" "$body" "$INSURED_TOKEN" "$RUN_ID-$1"
  CLAIM="$(field '.claim_id // empty')"
}
water_supports() {
  jq -nc --argjson a "$(upload_ids "$INSURED_TOKEN" "$WORK/photo.jpg" 2)" --argjson b "$(upload_ids "$INSURED_TOKEN" "$WORK/doc.pdf" 1)" \
    '[{requirement_code:"damage_photos",support_ids:$a},{requirement_code:"repair_quote",support_ids:$b}]'
}
theft_supports() {
  jq -nc --argjson a "$(upload_ids "$INSURED_TOKEN" "$WORK/doc.pdf" 1)" --argjson b "$(upload_ids "$INSURED_TOKEN" "$WORK/doc.pdf" 1)" \
    '[{requirement_code:"police_report",support_ids:$a},{requirement_code:"driver_document",support_ids:$b}]'
}
collision_supports() {
  jq -nc --argjson a "$(upload_ids "$INSURED_TOKEN" "$WORK/photo.jpg" 4)" --argjson b "$(upload_ids "$INSURED_TOKEN" "$WORK/doc.pdf" 1)" \
    --argjson c "$(upload_ids "$INSURED_TOKEN" "$WORK/doc.pdf" 1)" \
    '[{requirement_code:"vehicle_photos",support_ids:$a},{requirement_code:"driver_document",support_ids:$b},
      {requirement_code:"traffic_report",support_ids:$c}]'
}
glass_supports() {
  jq -nc --argjson a "$(upload_ids "$INSURED_TOKEN" "$WORK/photo.jpg" 2)" '[{requirement_code:"glass_photos",support_ids:$a}]'
}

echo "== [$(stamp)] 1. CA-1: a claim that enters evaluation gets its level within 60 seconds"
filing water "$HOME_POLICY" water_damage "$(water_supports)" 480000000
check "water damage is filed (201)" 201 "$STATUS"
WATER="$CLAIM"
check "the detail carries the declared amount" 480000000 "$(field .estimated_amount_minor)"
wait_calculations "$WATER" 1
check "it has its level in at most 60 s (waited ${WAITED}s)" yes "$([[ "$WAITED" != timeout && "$WAITED" -le 60 ]] && echo yes || echo no)"
check "state assessed" assessed "$(field .state)"
check "cause entered_evaluation" entered_evaluation "$(field '.history[0].causes | join(",")')"
check "calculated by the system" system "$(field '.history[0].actor')"
check "with its date and time" yes "$([[ "$(field .current.assessed_at)" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T ]] && echo yes || echo no)"
check "one row in claim_triage" 1 "$(calculations "$WATER")"

echo "== [$(stamp)] 2. CA-2: severity of the claim type x band of the amount"
check "water damage for 4.8 million is Triage 2" 2 "$(field .current.level)"
check "severity medium, amount low" "medium/low" "$(field '"\(.current.severity)/\(.current.amount_band)"')"
filing theft "$AUTO" theft "$(theft_supports)"
THEFT="$CLAIM"
wait_calculations "$THEFT" 1
check "a theft without amount is Triage 1" "1/high/unknown" "$(field '"\(.current.level)/\(.current.severity)/\(.current.amount_band)"')"
filing collision "$AUTO" collision "$(collision_supports)" 4000000000
COLLISION="$CLAIM"
wait_calculations "$COLLISION" 1
check "a collision for 40 million is Triage 1" "1/medium/high" "$(field '"\(.current.level)/\(.current.severity)/\(.current.amount_band)"')"
echo "  (the auto policy of this insured covers glass: $GLASS_COVERED)"
if [[ "$GLASS_COVERED" == true ]]; then
  filing glass "$AUTO" glass "$(glass_supports)" 90000000
  GLASS="$CLAIM"
else
  # The stub covers glass for some insureds only; this one does not have it,
  # so a collision for 900,000 becomes glass, as a correction of the type.
  filing glass "$AUTO" collision "$(collision_supports)" 90000000
  GLASS="$CLAIM"
  wait_calculations "$GLASS" 1
  psql_claims "UPDATE claim SET incident_type = 'glass' WHERE id = '$GLASS'" >/dev/null
fi
wait_calculations "$GLASS" "$([[ "$GLASS_COVERED" == true ]] && echo 1 || echo 2)"
check "glass for 900,000 is Triage 3" "3/low/low" "$(field '"\(.current.level)/\(.current.severity)/\(.current.amount_band)"')"

echo "== [$(stamp)] 3. CA-3: a change of the amount recalculates the level with its cause"
psql_claims "UPDATE claim SET estimated_amount_minor = 4000000000 WHERE id = '$WATER'" >/dev/null
wait_calculations "$WATER" 2
check "recalculated in at most 60 s (waited ${WAITED}s)" yes "$([[ "$WAITED" != timeout ]] && echo yes || echo no)"
check "now Triage 1" 1 "$(field .current.level)"
check "it was Triage 2" 2 "$(field '.history[0].previous_level')"
check "with the cause estimated_amount_changed" estimated_amount_changed "$(field '.history[0].causes | join(",")')"
check "the first calculation is still there" entered_evaluation "$(field '.history[1].causes | join(",")')"
psql_claims "UPDATE claim SET status = 'in_evaluation' WHERE id = '$WATER'" >/dev/null
sleep 6
check "moving it to in_evaluation recalculates nothing" 2 "$(calculations "$WATER")"

echo "== [$(stamp)] 4. CA-4: the analyst sees level, severity and band; the insured, the level and the days"
call GET "$CLAIMS/v1/claims?page_size=50" "" "$INSURED_TOKEN"
PAGE="$(field '[.items[].claim_id] | map("claim_id=" + .) | join("&")')"
PAGE_SIZE="$(field '.items | length')"
call GET "$CLAIMS/v1/claims/triage?$PAGE" "" "$OPS_TOKEN"
check "the page of the list is read at once (200)" 200 "$STATUS"
check "one row per claim of the page" "$PAGE_SIZE" "$(field '.items | length')"
check "every row is assessed" "$PAGE_SIZE" "$(field '[.items[] | select(.state=="assessed")] | length')"
check "the row of the theft" "1/high/unknown" "$(field ".items[] | select(.claim_id==\"$THEFT\") | \"\(.level)/\(.severity)/\(.amount_band)\"")"
call GET "$CLAIMS/v1/claims/triage?claim_id=$WATER&claim_id=00000000-0000-4000-8000-000000000000" "" "$OPS_TOKEN"
check "an unknown claim is not_found in its row" not_found "$(field '.items[1].state')"
call GET "$CLAIMS/v1/claims/$GLASS/triage" "" "$INSURED_TOKEN"
check "the insured reads the triage of their claim" 200 "$STATUS"
check "in the view of the insured" insured "$(field .view)"
check "level and estimated resolution time" "3/10" "$(field '"\(.level)/\(.estimated_resolution_business_days)"')"
check "without severity, amount nor history" "null/null/null" "$(field '"\(.severity)/\(.current)/\(.history)"')"
call GET "$CLAIMS/v1/claims/$GLASS/triage" "" "$OTHER_TOKEN"
check "another insured is refused (403)" 403 "$STATUS"
call GET "$CLAIMS/v1/claims/$GLASS/triage" "" "$ADVISOR_TOKEN"
check "an advisor is refused (403)" 403 "$STATUS"
call GET "$CLAIMS/v1/claims/triage?claim_id=$GLASS" "" "$INSURED_TOKEN"
check "the batch is not for the insured (403)" 403 "$STATUS"
call GET "$CLAIMS/v1/claims/$GLASS/triage"
check "without a token (401)" 401 "$STATUS"

echo "== [$(stamp)] 5. CA-5: every level has its target"
call GET "$CLAIMS/v1/claims/triage-policy" "" "$OPS_TOKEN"
check "the policy is served" triage-policy-v1 "$(field .version)"
check "Triage 1 immediate, 2 and 3 in 5 and 10 business days" "immediate:null,business_days:5,business_days:10" \
  "$(field '[.targets[].target | "\(.kind):\(.business_days)"] | join(",")')"
call GET "$CLAIMS/v1/claims/triage?claim_id=$GLASS" "" "$OPS_TOKEN"
check "the row carries the target of its level" 10 "$(field '.items[0].target.business_days')"

echo "== [$(stamp)] 6. Resilience: a restart and the policy core down do not stop the triage"
# A claim that reaches the database while no task of claims is running (as
# one filed just before a deploy): the sweep finds it when claims is back.
RESTARTED="22222222-2222-4222-8222-222222222222"
$COMPOSE stop claims >/dev/null
psql_claims "INSERT INTO claim (id, filing_number, cognito_sub, filing_key, fingerprint, policy_id, incident_type,
  occurred_at, place, description, status, filed_at, channel, estimated_amount_minor)
  VALUES ('$RESTARTED', 'SIN-2026-99999', '$INSURED_SUB', '$RUN_ID-down', 'f', '$HOME_POLICY', 'water_damage',
  now() - interval '1 day', 'Calle 80', 'Radicado mientras claims estaba detenido.', 'active', now(), 'web', 480000000)" >/dev/null
check "no calculation while claims is stopped" 0 "$(calculations "$RESTARTED")"
$COMPOSE start claims >/dev/null
wait_healthy
wait_calculations "$RESTARTED" 1
check "the claim gets its level once claims is back (waited ${WAITED}s)" yes "$([[ "$WAITED" != timeout ]] && echo yes || echo no)"
curl -sf -X POST "http://localhost:$TOXIPROXY_PORT/proxies/policycore" -d '{"enabled":false}' >/dev/null
psql_claims "UPDATE claim SET estimated_amount_minor = 100000000 WHERE id = '$RESTARTED'" >/dev/null
wait_calculations "$RESTARTED" 2
check "with the policy core down a change is still recalculated" yes "$([[ "$WAITED" != timeout ]] && echo yes || echo no)"
curl -sf -X POST "http://localhost:$TOXIPROXY_PORT/proxies/policycore" -d '{"enabled":true}' >/dev/null

echo "== [$(stamp)] 7. Immutability, minimization and a wrong amount"
for stmt in "UPDATE claim_triage SET level = 3" "DELETE FROM claim_triage" "TRUNCATE claim_triage"; do
  if psql_claims "$stmt" >/dev/null 2>&1; then
    check "$stmt fails" error ok
  else
    check "$stmt fails" error error
  fi
done
LOGS="$($COMPOSE logs claims 2>&1)"
check "the logs carry no amount" 0 "$(grep -cE '480000000|4000000000|90000000' <<<"$LOGS" || true)"
check "the logs carry the levels assessed" yes "$(grep -q 'claim priority assessed' <<<"$LOGS" && echo yes || echo no)"
filing wrong "$HOME_POLICY" water_damage "$(water_supports)" '"4.8 millones"'
check "a wrong amount is 422" 422 "$STATUS"
check "named as a field" "estimated_amount_minor:invalid_format" \
  "$(field '[.fields[] | select(.field=="estimated_amount_minor") | .field + ":" + .code] | join(",")')"

echo
echo "== HU-W2.2-01 e2e: $CHECKS checks"
if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-W2.2-01 e2e: $FAILS check(s) FAILED" >&2
  exit 1
fi
echo "== HU-W2.2-01 e2e: every check passed."
