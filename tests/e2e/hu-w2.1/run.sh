#!/usr/bin/env bash
# Runs the HU-W2.1-01 end-to-end scenarios with Docker, against the real claims
# service (its PostgreSQL, the policy core stub behind Toxiproxy, Moto as
# Cognito and as the S3 of the supports):
#
#   1. CA-1  the filing options are the insured's active policies, without
#            travel nor the expired one, with the supports of each type
#   2. CA-2  without the traffic report the filing is refused and the missing
#            support is named; nothing is stored
#   3. CA-3  an incident before the policy started, a type the policy does not
#            cover and an expired policy are refused with their reason
#   4. CA-4  a complete filing creates the active claim with its number; the
#            detail and the PDF receipt carry it; a retry gives the same claim
#   5. CA-5  a file of 10 MiB + 1 byte (413 when declared) and an executable
#            renamed .pdf (415 when confirmed) are refused, 10 MiB exactly is
#            accepted; the supports uploaded before still file
#   6. CA-6  a policy of another insured is not found, the claim of another
#            insured is 403, and the claim is in the insured's list
#   7.       the policy core down (Toxiproxy): 503 and no new claim
#   8.       claims restarts: the claim, its supports and its receipt persist;
#            UPDATE on claim_support fails with psql
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
export CLAIMS_PORT="${CLAIMS_PORT:-8297}" POLICY_CORE_PORT="${POLICY_CORE_PORT:-9160}"
export TOXIPROXY_PORT="${TOXIPROXY_PORT:-8495}" MOTO_PORT="${MOTO_PORT:-5123}"
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
check() { # <description> <expected> <actual>
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
    *.png) echo image/png ;;
    *) echo application/pdf ;;
  esac
}

# upload <token> <path> [file name]: what the portal does with a file. It
# declares it, PUTs it to the signed URL (the S3 of Moto) and confirms it. STATUS
# and BODY are the ones of the last step that ran: the declaration when it was
# refused, the PUT when it failed, otherwise the confirmation.
upload() {
  local file="$2" name="${3:-$(basename "$2")}" declaration id url put_status h
  local headers=()
  declaration="$(jq -nc --arg n "$name" --arg t "$(content_type_of "$file")" --argjson s "$(file_size "$file")" \
    --arg h "$(file_sha256 "$file")" '{file_name:$n, content_type:$t, byte_size:$s, sha256:$h}')"
  call POST "$CLAIMS/v1/claims/supports" "$declaration" "$1"
  [[ "$STATUS" == 201 ]] || return 0
  id="$(field .support_id)"
  url="$(field .upload_url)"
  while IFS= read -r h; do headers+=(-H "$h"); done < <(
    jq -r '.upload_headers | to_entries[] | select(.key | ascii_downcase != "content-length") | "\(.key): \(.value)"' <<<"$BODY")
  # The signed URL names the host of the compose network; the script reaches Moto on its published port.
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
claim_count() { psql_claims "SELECT count(*) FROM claim" | tr -d '[:space:]'; }

# jwt_sub <token>: the subject of an ID token (base64url payload).
jwt_sub() {
  local payload
  payload="$(cut -d. -f2 <<<"$1" | tr '_-' '/+')"
  while (( ${#payload} % 4 )); do payload+="="; done
  base64 -d <<<"$payload" 2>/dev/null | jq -r .sub
}

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

# Test files: images and PDFs are recognized by their first bytes.
printf '\xff\xd8\xff\xe0' >"$WORK/photo.jpg" && head -c 4096 /dev/urandom >>"$WORK/photo.jpg"
printf '%%PDF-1.7\n' >"$WORK/doc.pdf" && head -c 4096 /dev/urandom >>"$WORK/doc.pdf"
printf 'MZ\x90\x00' >"$WORK/virus.pdf" && head -c 4096 /dev/urandom >>"$WORK/virus.pdf"
printf '%%PDF-1.7\n' >"$WORK/huge.pdf" && head -c $((10 * 1024 * 1024 - 8)) /dev/zero >>"$WORK/huge.pdf"
printf '%%PDF-1.7\n' >"$WORK/limit.pdf" && head -c $((10 * 1024 * 1024 - 9)) /dev/zero >>"$WORK/limit.pdf"

echo "== [$(stamp)] Starting PostgreSQL, the policy core stub behind Toxiproxy and Moto"
$COMPOSE up -d --build --wait postgres-claims policy-core toxiproxy moto
$COMPOSE up toxiproxy-init >/dev/null
# The bucket of the supports, in the S3 of Moto, before claims starts: it checks it.
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
INSURED_SUB="$(jwt_sub "$INSURED_TOKEN")"
OTHER_SUB="$(jwt_sub "$OTHER_TOKEN")"

echo "== [$(stamp)] Starting claims (POLICY_CORE_URL through Toxiproxy)"
$COMPOSE up -d --build claims
wait_healthy
$COMPOSE logs claims | grep -E "web filing|database migration" || true

# What the policy core knows of each insured.
CORE="$(curl -sf "http://localhost:$POLICY_CORE_PORT/holders/$INSURED_SUB/policies")"
AUTO="$(jq -r '.policies[] | select(.line=="auto" and .status=="active") | .policy_id' <<<"$CORE")"
AUTO_FROM="$(jq -r '.policies[] | select(.line=="auto" and .status=="active") | .valid_from' <<<"$CORE")"
HOME_POLICY="$(jq -r '.policies[] | select(.line=="home") | .policy_id' <<<"$CORE")"
EXPIRED="$(jq -r '.policies[] | select(.status=="expired") | .policy_id' <<<"$CORE")"
OTHER_AUTO="$(curl -sf "http://localhost:$POLICY_CORE_PORT/holders/$OTHER_SUB/policies" |
  jq -r '.policies[] | select(.line=="auto" and .status=="active") | .policy_id')"
# Date arithmetic with jq: the same on the date of macOS (BSD) and of Linux (GNU).
OCCURRED="$(jq -rn 'now - 2 * 86400 | floor | todateiso8601')"

# filing_body <policy> <incident> <occurred_at> <supports JSON>
filing_body() {
  jq -nc --arg p "$1" --arg i "$2" --arg o "$3" --argjson s "$4" \
    '{policy_id:$p, incident_type:$i, occurred_at:$o, place:"Calle 80 con carrera 30, Bogotá",
      description:"Al detenerme en el semáforo, un vehículo particular golpeó la parte trasera.", supports:$s}'
}
# collision_supports <token> [without the traffic report: "partial"]
collision_supports() {
  local photos driver report
  photos="$(upload_ids "$1" "$WORK/photo.jpg" 4)"
  driver="$(upload_ids "$1" "$WORK/doc.pdf" 1)"
  if [[ "${2:-}" == partial ]]; then
    jq -nc --argjson a "$photos" --argjson b "$driver" \
      '[{requirement_code:"vehicle_photos",support_ids:$a},{requirement_code:"driver_document",support_ids:$b}]'
    return
  fi
  report="$(upload_ids "$1" "$WORK/doc.pdf" 1)"
  jq -nc --argjson a "$photos" --argjson b "$driver" --argjson c "$report" \
    '[{requirement_code:"vehicle_photos",support_ids:$a},{requirement_code:"driver_document",support_ids:$b},
      {requirement_code:"traffic_report",support_ids:$c}]'
}

echo "== [$(stamp)] 1. CA-1: the filing options are the insured's active policies"
call GET "$CLAIMS/v1/claims/filing-options" "" "$INSURED_TOKEN"
check "the options answer 200" 200 "$STATUS"
check "only the active auto and home policies" "$AUTO,$HOME_POLICY" "$(field '[.policies[].policy_id] | join(",")')"
check "travel (parametric) is not offered" 0 "$(field '[.policies[] | select(.line=="travel")] | length')"
check "the expired policy is not offered" 0 "$(field "[.policies[] | select(.policy_id==\"$EXPIRED\")] | length")"
check "collision asks for three required supports" "driver_document,traffic_report,vehicle_photos" \
  "$(field '.policies[0].incident_types[] | select(.code=="collision") | [.requirements[] | select(.required) | .code] | sort | join(",")')"

echo "== [$(stamp)] 2. CA-2: a missing support is named and nothing is filed"
BEFORE="$(claim_count)"
call POST "$CLAIMS/v1/claims/web-filings" "$(filing_body "$AUTO" collision "$OCCURRED" "$(collision_supports "$INSURED_TOKEN" partial)")" \
  "$INSURED_TOKEN" "$RUN_ID-ca2"
check "the filing is refused (422)" 422 "$STATUS"
check "the error is validation_failed" validation_failed "$(field .error)"
check "the missing support is named" "supports.traffic_report:missing" "$(field '[.fields[] | .field + ":" + .code] | join(",")')"
check "no claim was stored" "$BEFORE" "$(claim_count)"

echo "== [$(stamp)] 3. CA-3: outside the validity, not covered or expired is refused with the reason"
BEFORE="$(claim_count)"
BEFORE_START="$(jq -rn --arg t "$AUTO_FROM" '$t | fromdateiso8601 - 3600 | todateiso8601')"
call POST "$CLAIMS/v1/claims/web-filings" "$(filing_body "$AUTO" collision "$BEFORE_START" "$(collision_supports "$INSURED_TOKEN")")" \
  "$INSURED_TOKEN" "$RUN_ID-ca3a"
check "an incident before the policy started is 422" 422 "$STATUS"
check "the reason is occurred_outside_validity" occurred_outside_validity "$(field .reason)"
check "the validity is told" "$AUTO_FROM" "$(field .valid_from)"
call POST "$CLAIMS/v1/claims/web-filings" "$(filing_body "$HOME_POLICY" collision "$OCCURRED" "$(collision_supports "$INSURED_TOKEN")")" \
  "$INSURED_TOKEN" "$RUN_ID-ca3b"
check "a collision on the home policy is incident_not_covered" incident_not_covered "$(field .reason)"
call POST "$CLAIMS/v1/claims/web-filings" "$(filing_body "$EXPIRED" collision "$OCCURRED" "$(collision_supports "$INSURED_TOKEN")")" \
  "$INSURED_TOKEN" "$RUN_ID-ca3c"
check "the expired policy is policy_not_active" policy_not_active "$(field .reason)"
check "no claim was stored" "$BEFORE" "$(claim_count)"

echo "== [$(stamp)] 4. CA-4: a complete filing creates the active claim with its receipt"
BODY_CA4="$(filing_body "$AUTO" collision "$OCCURRED" "$(collision_supports "$INSURED_TOKEN")")"
call POST "$CLAIMS/v1/claims/web-filings" "$BODY_CA4" "$INSURED_TOKEN" "$RUN_ID-ca4"
check "the claim is created (201)" 201 "$STATUS"
CLAIM_ID="$(field .claim_id)"
FILING_NUMBER="$(field .filing_number)"
check "it is active" active "$(field .status)"
check "it was filed on the web" web "$(field .channel)"
check "the filing number is SIN-<year>-<5 digits>" yes "$([[ "$FILING_NUMBER" =~ ^SIN-[0-9]{4}-[0-9]{5}$ ]] && echo yes || echo no)"
check "3 of 3 required supports, 6 files" "3/3/6" "$(field '"\(.supports.required_attached)/\(.supports.required_total)/\(.supports.files)"')"
call POST "$CLAIMS/v1/claims/web-filings" "$BODY_CA4" "$INSURED_TOKEN" "$RUN_ID-ca4"
check "a retry with the same key answers 200" 200 "$STATUS"
check "with the same claim" "$FILING_NUMBER" "$(field .filing_number)"
call GET "$CLAIMS/v1/claims/$CLAIM_ID" "" "$INSURED_TOKEN"
check "the detail answers 200" 200 "$STATUS"
RECEIPT_STATUS="$(curl -s -o "$WORK/receipt.pdf" -w '%{http_code}' "$CLAIMS/v1/claims/$CLAIM_ID/filing-receipt" \
  -H "Authorization: Bearer $INSURED_TOKEN")"
check "the receipt answers 200" 200 "$RECEIPT_STATUS"
check "the receipt is a PDF" "%PDF-" "$(head -c 5 "$WORK/receipt.pdf")"
check "the receipt carries the filing number" yes "$(grep -aq "$FILING_NUMBER" "$WORK/receipt.pdf" && echo yes || echo no)"

echo "== [$(stamp)] 5. CA-5: a file of a type or size not allowed is refused without losing the rest"
SUPPORTS_CA5="$(collision_supports "$INSURED_TOKEN")"
upload "$INSURED_TOKEN" "$WORK/huge.pdf" huge.pdf
check "10 MiB + 1 byte is 413 when declared" 413 "$STATUS"
check "the error is file_too_large" file_too_large "$(field .error)"
upload "$INSURED_TOKEN" "$WORK/limit.pdf" limit.pdf
check "exactly 10 MiB is uploaded and confirmed (200)" 200 "$STATUS"
check "with its size" 10485760 "$(field .byte_size)"
upload "$INSURED_TOKEN" "$WORK/virus.pdf" virus.pdf
check "an executable renamed .pdf is 415 when confirmed" 415 "$STATUS"
check "the error is unsupported_file_type" unsupported_file_type "$(field .error)"
call POST "$CLAIMS/v1/claims/supports" '{"file_name":"run.exe","content_type":"application/x-msdownload","byte_size":100,"sha256":"'"$(printf 'a%.0s' $(seq 1 64))"'"}' "$INSURED_TOKEN"
check "a type that is not JPG, PNG or PDF is 415 when declared" 415 "$STATUS"
call POST "$CLAIMS/v1/claims/supports" '{"file_name":"late.pdf","content_type":"application/pdf","byte_size":100,"sha256":"'"$(printf 'a%.0s' $(seq 1 64))"'"}' "$INSURED_TOKEN"
LATE_ID="$(field .support_id)"
call POST "$CLAIMS/v1/claims/supports/$LATE_ID/confirmation" "" "$INSURED_TOKEN"
check "a file that was never uploaded is 422" 422 "$STATUS"
check "the error is support_not_uploaded" support_not_uploaded "$(field .error)"
call POST "$CLAIMS/v1/claims/supports/$LATE_ID/confirmation" "" "$OTHER_TOKEN"
check "another insured cannot confirm it (404)" 404 "$STATUS"
call POST "$CLAIMS/v1/claims/web-filings" "$(filing_body "$AUTO" collision "$OCCURRED" "$SUPPORTS_CA5")" "$INSURED_TOKEN" "$RUN_ID-ca5"
check "the supports uploaded before still file (201)" 201 "$STATUS"

echo "== [$(stamp)] 6. CA-6: only the insured's own policies, claims and list"
call POST "$CLAIMS/v1/claims/web-filings" "$(filing_body "$OTHER_AUTO" collision "$OCCURRED" "$(collision_supports "$INSURED_TOKEN")")" \
  "$INSURED_TOKEN" "$RUN_ID-ca6"
check "the policy of another insured is refused (422)" 422 "$STATUS"
check "as if it did not exist" policy_not_found "$(field .reason)"
check "without telling its validity" null "$(field .valid_from)"
call GET "$CLAIMS/v1/claims/$CLAIM_ID" "" "$OTHER_TOKEN"
check "another insured cannot read the claim (403)" 403 "$STATUS"
call GET "$CLAIMS/v1/claims" "" "$INSURED_TOKEN"
check "the list answers 200" 200 "$STATUS"
check "the claim is in the insured's list" 1 "$(field "[.items[] | select(.filing_number==\"$FILING_NUMBER\")] | length")"
check "with its status" active "$(field ".items[] | select(.filing_number==\"$FILING_NUMBER\") | .status")"
call GET "$CLAIMS/v1/claims" "" "$OTHER_TOKEN"
check "another insured's list does not have it" 0 "$(field '.items | length')"

echo "== [$(stamp)] 7. the policy core down: 503 and no claim"
SUPPORTS_CA7="$(collision_supports "$INSURED_TOKEN")"
BEFORE="$(claim_count)"
curl -sf -X POST "http://localhost:$TOXIPROXY_PORT/proxies/policycore" -d '{"enabled":false}' >/dev/null
call POST "$CLAIMS/v1/claims/web-filings" "$(filing_body "$AUTO" collision "$OCCURRED" "$SUPPORTS_CA7")" "$INSURED_TOKEN" "$RUN_ID-ca7"
check "the filing answers 503" 503 "$STATUS"
check "the error is policy_directory_unavailable" policy_directory_unavailable "$(field .error)"
call GET "$CLAIMS/v1/claims/filing-options" "" "$INSURED_TOKEN"
check "the options answer 503" 503 "$STATUS"
check "no claim was stored" "$BEFORE" "$(claim_count)"
curl -sf -X POST "http://localhost:$TOXIPROXY_PORT/proxies/policycore" -d '{"enabled":true}' >/dev/null

echo "== [$(stamp)] 8. claims restarts: the claim and its supports persist"
$COMPOSE restart claims >/dev/null
wait_healthy
call GET "$CLAIMS/v1/claims/$CLAIM_ID" "" "$INSURED_TOKEN"
check "the claim is still there (200)" 200 "$STATUS"
check "with its number" "$FILING_NUMBER" "$(field .filing_number)"
check "and its 6 files" 6 "$(field .supports.files)"
RECEIPT_STATUS="$(curl -s -o /dev/null -w '%{http_code}' "$CLAIMS/v1/claims/$CLAIM_ID/filing-receipt" \
  -H "Authorization: Bearer $INSURED_TOKEN")"
check "the receipt is still downloadable" 200 "$RECEIPT_STATUS"
if psql_claims "UPDATE claim_support SET requirement_code = 'x'" >/dev/null 2>&1; then
  check "UPDATE on claim_support fails" error ok
else
  check "UPDATE on claim_support fails" error error
fi

echo
if [[ "$FAILS" -gt 0 ]]; then
  echo "== HU-W2.1-01 e2e: $FAILS check(s) FAILED" >&2
  exit 1
fi
echo "== HU-W2.1-01 e2e: every check passed."
