#!/usr/bin/env bash
# Runs the end-to-end scenarios of the controlled application of the
# endorsement (HU-W1.4-02) with Docker, against the real audit, origination and
# claims services (PostgreSQL of each, Redis, the Open Finance / Open Data /
# KYC stubs behind Toxiproxy, the payment gateway and e-mail stubs, Moto as
# Cognito). The policies are bought through the real journey (consent in
# audit, POST /v1/offers, acceptance and the card 4111) and the change requests
# are filed by the insured through the routes of HU-W1.4-01.
#
#   E1   CA-1..CA-5  beneficiaries: inbox, take (in review for the insured), detail with the rows, the policy and
#                    five validations passed, apply (END-<year>-<number>), the insured sees it applied and the
#                    current terms carry the new beneficiaries
#   E2   CA-3        insured value: the detail prices the new premium; applying it, the wallet of the insured and
#                    the list of the advisor show the new value and premium
#   E3   CA-1, CA-4  personal data: the current terms carry the phone, the address and the contact e-mail; the
#                    user of Moto keeps its e-mail
#   E4   CA-2, CA-5  a document ending in 000: not applied, the attempt recorded, the policy intact, the insured
#                    still sees it in review; returning without observations 422, with them the insured sees it
#                    rejected with the reason
#   E5   CA-5        rejecting without observations 422; with them rejected; applying it afterwards 409
#   E6   CA-2        a claim in progress in claims (INSERT in its local PostgreSQL): no_open_claim fails
#   E7   concurrency two applications at once: one endorsement, both answers with the same one
#   E8   fail closed KYC down (proxy disabled): 503 validation_unverifiable and nothing changes; claims stopped:
#                    the same for an insured value; both back: they apply
#   E9   CA-4        the history of a policy with its endorsement and of another with its attempt; the same after
#                    restarting origination
#   E10  roles       without a token 401; the insured and the advisor 403 on the seven routes
#   E11  D-12        the e-mail stub received one message per decision, with the key endorsement-outcome:<id>
#
# The script exits with a non-zero code when any check fails. The environment
# is always removed at the end (down -v). Requirements: Docker (compose v2),
# curl, jq and the AWS CLI (only against the local Moto). The secrets
# (PostgreSQL, Moto users, the document link) are random, generated here and
# never printed.
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
export AUDIT_PORT="${AUDIT_PORT:-8690}" ORIGINATION_PORT="${ORIGINATION_PORT:-8691}" CLAIMS_PORT="${CLAIMS_PORT:-8692}"
export TOXIPROXY_PORT="${TOXIPROXY_PORT:-8694}" MOTO_PORT="${MOTO_PORT:-5290}"
export EMAIL_PORT="${EMAIL_PORT:-9291}" GATEWAY_PORT="${GATEWAY_PORT:-9294}"
AUDIT="http://localhost:$AUDIT_PORT"
ORIGINATION="http://localhost:$ORIGINATION_PORT"
CLAIMS="http://localhost:$CLAIMS_PORT"
GATEWAY="http://localhost:$GATEWAY_PORT"
MAIL="http://localhost:$EMAIL_PORT"
RUN_ID="e$(date +%s)"
INSURED_EMAIL="asegurado.local@example.com"

secret() { head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-"$1"; }
PG_PASSWORD="$(secret 32)"
MOTO_USER_PASSWORD="Ha$(secret 20)9!"
DOCUMENT_LINK_SECRET="$(secret 48)"
export PG_PASSWORD MOTO_USER_PASSWORD DOCUMENT_LINK_SECRET

umask 077
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
check_true() { # <description> <0 when true>
  if [[ "$2" == 0 ]]; then
    echo "  ok    $1"
  else
    echo "  FAIL  $1" >&2
    FAILS=$((FAILS + 1))
  fi
}

# call <method> <url> [json body] [bearer token]: sets STATUS and BODY. The
# token goes through a private header file, never through the command line.
call() {
  : >"$WORK/header"
  printf 'User-Agent: hu-w1.4-e2e\n' >>"$WORK/header"
  [[ -n "${4:-}" ]] && printf 'Authorization: Bearer %s\n' "$4" >>"$WORK/header"
  if [[ -n "${3:-}" ]]; then
    printf 'Content-Type: application/json\n' >>"$WORK/header"
    STATUS="$(printf '%s' "$3" | curl -s --max-time 20 -o "$WORK/body" -w '%{http_code}' -X "$1" -H @"$WORK/header" --data-binary @- "$2" || true)"
  else
    STATUS="$(curl -s --max-time 20 -o "$WORK/body" -w '%{http_code}' -X "$1" -H @"$WORK/header" "$2" || true)"
  fi
  BODY="$(tr -d '\r' <"$WORK/body")"
}
field() { jq -r "$1" <<<"$BODY" 2>/dev/null || echo "<not json>"; }

wait_healthy() { # <url> <service>
  local i
  for i in $(seq 1 120); do
    curl -sf --max-time 2 "$1/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "$1 did not become healthy" >&2
  $COMPOSE logs "$2" | tail -40 >&2
  return 1
}

toxi() { curl -sf -X POST "http://localhost:$TOXIPROXY_PORT/proxies/$1" -H 'Content-Type: application/json' -d "{\"enabled\":$2}" >/dev/null; }
psql_in() { # <database> <sql>
  $COMPOSE exec -T "postgres-$1" psql -U "$1" -d "$1" -v ON_ERROR_STOP=1 -tA -c "$2" 2>&1 | tr -d '\r'
}

echo "== [$(stamp)] Starting PostgreSQL, Redis, the stubs behind Toxiproxy, the gateway, the e-mail and Moto"
$COMPOSE up -d --build --wait postgres-audit postgres-origination postgres-claims email payment-gateway redis openfinance opendata kyc \
  toxiproxy moto

echo "== [$(stamp)] Seeding the local Cognito (Moto)"
SEED_OUT=""
# The published port of Moto can lag behind its health check on Docker Desktop.
for attempt in $(seq 1 15); do
  if SEED_OUT="$(env -u MSYS_NO_PATHCONV MOTO_URL="http://localhost:$MOTO_PORT" bash ../../security/ha-s01/local/seed.sh 2>&1 | tr -d '\r')"; then
    break
  fi
  if [[ "$attempt" == 15 ]]; then
    echo "$SEED_OUT" >&2
    exit 1
  fi
  sleep 3
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
OPERATIONS_TOKEN="$(token operaciones.local@example.com)"
INSURED_TOKEN="$(token "$INSURED_EMAIL")"
ADVISOR_TOKEN="$(token asesor.local@example.com)"

echo "== [$(stamp)] Starting audit, claims and origination"
$COMPOSE up -d --build audit claims origination
wait_healthy "$AUDIT" audit
wait_healthy "$CLAIMS" claims
wait_healthy "$ORIGINATION" origination

call GET "$AUDIT/v1/audit/consent-terms/current"
TERMS_VERSION="$(field .version)"
TERMS_SOURCES="$(field '[.sources[].code] | map("\"" + . + "\"") | join(",")')"

# pay <acceptance id> <card number>: tokenises the card at the gateway (as the
# browser does) and hands it over as the insured.
pay() {
  call POST "$GATEWAY/tokens" "{\"number\":\"$2\",\"holder\":\"Prueba E2E\"}"
  local tok brand last4
  tok="$(field .token)" brand="$(field .brand)" last4="$(field .last4)"
  call POST "$ORIGINATION/v1/acceptances/$1/charge/payment-method" \
    "{\"method\":\"card\",\"payment_token\":\"$tok\",\"brand\":\"$brand\",\"last4\":\"$last4\",\"installments\":1}" "$INSURED_TOKEN"
}

# seed <tag> <holder name> <document>: buys a policy of 12 months of the
# insured through the real journey; sets POLICY.
seed() {
  local customer="cust-$1-$RUN_ID" app="app-$1-$RUN_ID" consent offer acceptance i
  call POST "$AUDIT/v1/audit/consents" "{\"customer_id\":\"$customer\",\"document_id\":\"$3\",\"full_name\":\"$2\",
    \"terms_version\":\"$TERMS_VERSION\",\"accepted_sources\":[$TERMS_SOURCES],\"accepted_data_policy\":true,
    \"channel\":\"embedded\",\"partner_id\":\"partner-e2e\",\"application_id\":\"$app\"}"
  [[ "$STATUS" == 201 ]] || { echo "consent of $1: HTTP $STATUS $BODY" >&2; exit 1; }
  consent="$(field .consent_id)"
  call POST "$ORIGINATION/v1/offers" "{\"customer_id\":\"$customer\",\"consent_id\":\"$consent\",\"partner_id\":\"partner-e2e\",
    \"application_id\":\"$app\",\"document_id\":\"$3\",\"full_name\":\"$2\",\"email\":\"$INSURED_EMAIL\",\"birth_date\":\"1985-03-14\",
    \"insured_amount\":68000000,\"term_months\":12}"
  [[ "$STATUS" == 201 ]] || { echo "offer of $1: HTTP $STATUS $BODY" >&2; exit 1; }
  offer="$(field .offer_id)"
  call GET "$ORIGINATION/v1/offers/$offer" "" "$INSURED_TOKEN"
  call POST "$ORIGINATION/v1/offers/$offer/acceptance" '{"channel":"web"}' "$INSURED_TOKEN"
  [[ "$STATUS" == 202 ]] || { echo "acceptance of $1: HTTP $STATUS $BODY" >&2; exit 1; }
  acceptance="$(field .acceptance_id)"
  for i in $(seq 1 60); do
    call GET "$ORIGINATION/v1/acceptances/$acceptance/charge" "" "$INSURED_TOKEN"
    [[ "$STATUS" == 200 ]] && break
    sleep 1
  done
  [[ "$STATUS" == 200 ]] || { echo "charge of $1 not created: HTTP $STATUS $BODY" >&2; exit 1; }
  pay "$acceptance" 4111111111111111
  for i in $(seq 1 60); do
    call GET "$ORIGINATION/v1/acceptances/$acceptance/charge" "" "$INSURED_TOKEN"
    [[ "$(field .status)" == confirmed && "$(field .policy_id)" != null ]] && break
    sleep 1
  done
  POLICY="$(field .policy_id)"
  [[ "$POLICY" != null && -n "$POLICY" ]] || { echo "policy of $1 not issued: $BODY" >&2; exit 1; }
  for i in $(seq 1 30); do
    call GET "$ORIGINATION/v1/policies/$POLICY/current-terms" "" "$INSURED_TOKEN"
    [[ "$STATUS" == 200 ]] && break
    sleep 1
  done
  echo "  seeded $1: $POLICY ($2)"
}

# file <policy> <body>: the insured files a change request; sets REQ.
file() {
  call POST "$ORIGINATION/v1/policies/$1/change-requests" "$2" "$INSURED_TOKEN"
  [[ "$STATUS" == 201 ]] || { echo "change request on $1: HTTP $STATUS $BODY" >&2; exit 1; }
  REQ="$(field .request_id)"
}

ER="$ORIGINATION/v1/policies/endorsement-requests"
beneficiaries() { # <document of the second beneficiary>
  echo "{\"change_type\":\"beneficiaries\",\"channel\":\"web\",\"beneficiaries\":[
    {\"full_name\":\"Mateo Pérez Gómez\",\"relationship\":\"child\",\"document_type\":\"TI\",\"document_number\":\"1140223001\",\"share_percent\":60},
    {\"full_name\":\"Lucía Pérez Gómez\",\"relationship\":\"child\",\"document_type\":\"TI\",\"document_number\":\"$1\",\"share_percent\":40}]}"
}
INSURED_AMOUNT='{"change_type":"insured_amount","channel":"web","insured_amount":120000000}'
CONTACT='{"change_type":"personal_data","channel":"mobile","personal_data":{"address":"Calle 100 # 15-20, Bogotá","phone":"301 234 5678","email":"contacto.e2e@example.com"}}'
apply() { call POST "$ER/$1/application" "" "$OPERATIONS_TOKEN"; }
insured_status() { # <request>: the stored status and the decision reason, as the insured reads them
  call GET "$ORIGINATION/v1/policies/change-requests/$1" "" "$INSURED_TOKEN"
  echo "$(field .status) $(field .decision_reason)"
}

echo "== [$(stamp)] Buying the policies through the real journey"
seed p1 "Carolina Perez Ruiz" 1020345678
P1="$POLICY"
seed p2 "Mateo Rios Lara" 1030456789
P2="$POLICY"
seed p3 "Ana Maria Torres" 1040567890
P3="$POLICY"
seed p4 "Diego Salazar Pena" 1050678901
P4="$POLICY"
seed p5 "Pedro Gomez Lara" 1060789012
P5="$POLICY"
seed p6 "Laura Salazar Mejia" 1070890123
P6="$POLICY"
seed p7 "Sofia Castro Diaz" 1080901234
P7="$POLICY"
seed p8 "Tomas Vargas Silva" 1091012345
P8="$POLICY"

echo "== [$(stamp)] E1. Beneficiaries: inbox, take, detail, apply"
file "$P1" "$(beneficiaries 1142998310)"
R1="$REQ"
call GET "$ER" "" "$OPERATIONS_TOKEN"
check "the inbox answers 200" 200 "$STATUS"
check "the request is pending in the inbox, with the holder" "filed Carolina Perez Ruiz 1020345678" \
  "$(field ".items[] | select(.request_id == \"$R1\") | \"\(.status) \(.holder.full_name) \(.holder.document_id)\"")"
check "the indicators count it as pending" true "$(field '.indicators.pending.count >= 1')"
check_true "the inbox never carries the e-mail of the insured" "$(! grep -qF "$INSURED_EMAIL" <<<"$BODY"; echo $?)"
call POST "$ER/$R1/review" "" "$OPERATIONS_TOKEN"
check "taking it answers 200 in_review" "200 in_review" "$STATUS $(field .request.status)"
check "the insured sees it in review" "in_review null" "$(insured_status "$R1")"
call GET "$ER/$R1" "" "$OPERATIONS_TOKEN"
check "the detail answers 200" 200 "$STATUS"
check "two rows of beneficiaries" 2 "$(field '.changes | length')"
check "the policy panel: insured value and the lender" "68000000 Banco Aliado E2E" \
  "$(field '"\(.policy.insured_amount) \(.policy.onerous_beneficiary.name)"')"
check "five validations passed (preview)" "shares_total_100,onerous_beneficiary_intact,identity_documents_valid,no_open_claim,requester_is_holder passed preview" \
  "$(field '"\([.validations[].code] | join(",")) \([.validations[].outcome] | unique | join(",")) \(.validations_source)"')"
check "the registry checked the two new documents" 2 "$(field '.validations[] | select(.code == "identity_documents_valid") | .params.checks')"
check_true "the detail never carries the e-mail of the insured" "$(! grep -qF "$INSURED_EMAIL" <<<"$BODY"; echo $?)"
apply "$R1"
check "applying answers 200 applied" "200 applied" "$STATUS $(field .outcome)"
END1="$(field .endorsement.endorsement_id)"
check "with an END number" true "$([[ "$END1" =~ ^END-[0-9]{4}-[0-9]{4,}$ ]] && echo true || echo false)"
check "applied by the analyst" "Andrés Ruiz" "$(field .endorsement.by.name)"
check "the insured sees it applied" "applied null" "$(insured_status "$R1")"
call GET "$ORIGINATION/v1/policies/$P1/current-terms" "" "$INSURED_TOKEN"
check "the current terms carry the new beneficiaries" "Mateo Pérez Gómez:60,Lucía Pérez Gómez:40" \
  "$(field '[.terms.beneficiaries[] | "\(.full_name):\(.share_percent)"] | join(",")')"

echo "== [$(stamp)] E2. Insured value: the premium of the engine, everywhere"
file "$P2" "$INSURED_AMOUNT"
R2="$REQ"
call GET "$ER/$R2" "" "$OPERATIONS_TOKEN"
check "the detail prices the new premium with the engine v1" "true v1" "$(field '"\(.premium.new > .premium.previous) \(.premium.rating_version)"')"
NEW_PREMIUM="$(field .premium.new)"
check "three validations for an insured value" "insured_amount_within_limits,no_open_claim,requester_is_holder" \
  "$(field '[.validations[].code] | join(",")')"
apply "$R2"
check "applying it answers applied" applied "$(field .outcome)"
call GET "$ORIGINATION/v1/policies" "" "$INSURED_TOKEN"
check "the wallet of the insured shows the new value and premium" "120000000 $NEW_PREMIUM" \
  "$(field ".items[] | select(.policy_id == \"$P2\") | \"\(.insured_amount) \(.premium)\"")"
call GET "$ORIGINATION/v1/policies/expiring?page_size=50" "" "$ADVISOR_TOKEN"
check "the list of the advisor shows the new premium" "$NEW_PREMIUM" \
  "$(field ".items[] | select(.policy_id == \"$P2\") | .premium")"
call GET "$ER/$R2" "" "$OPERATIONS_TOKEN"
check "the policy panel of the analyst shows them" "120000000 $NEW_PREMIUM" "$(field '"\(.policy.insured_amount) \(.policy.premium)"')"

echo "== [$(stamp)] E3. Personal data"
file "$P3" "$CONTACT"
R3="$REQ"
apply "$R3"
check "applying the contact data answers applied" applied "$(field .outcome)"
call GET "$ORIGINATION/v1/policies/$P3/current-terms" "" "$INSURED_TOKEN"
check "the current terms carry the address, the phone and the contact e-mail" \
  "Calle 100 # 15-20, Bogotá 3012345678 contacto.e2e@example.com" \
  "$(field '"\(.terms.personal_data.address) \(.terms.personal_data.phone) \(.terms.personal_data.email)"')"
MOTO_EMAIL="$(
  unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN
  export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-2 AWS_DEFAULT_REGION=us-east-2 AWS_PAGER=""
  MSYS_NO_PATHCONV=1 aws cognito-idp admin-get-user --endpoint-url "http://localhost:$MOTO_PORT" --region us-east-2 \
    --user-pool-id "${AUTH_ISSUER##*/}" --username "$INSURED_EMAIL" --output json | tr -d '\r' |
    jq -r '.UserAttributes[] | select(.Name == "email") | .Value'
)"
check "the sign-in e-mail of the insured does not change" "$INSURED_EMAIL" "$MOTO_EMAIL"

echo "== [$(stamp)] E4. A document not in the registry: not applied, then returned"
file "$P4" "$(beneficiaries 1142998000)"
R4="$REQ"
apply "$R4"
check "applying answers 200 not_applied" "200 not_applied" "$STATUS $(field .outcome)"
check "the attempt names the validation that failed" identity_documents_valid "$(field '.attempt.failed | join(",")')"
call GET "$ORIGINATION/v1/policies/$P4/current-terms" "" "$INSURED_TOKEN"
check "the policy keeps its terms" 0 "$(field '.terms.beneficiaries | length')"
call GET "$ORIGINATION/v1/policies/change-requests?status=in_review" "" "$INSURED_TOKEN"
check "the insured still sees it in review" 1 "$(field "[.items[] | select(.request_id == \"$R4\")] | length")"
call GET "$ER/$R4" "" "$OPERATIONS_TOKEN"
check "the detail shows the attempt" "attempt not_applied" "$(field '"\(.validations_source) \(.decision.outcome)"')"
call POST "$ER/$R4/return" '{"observations":"corto"}' "$OPERATIONS_TOKEN"
check "returning without observations: 422 observations_required" "422 observations_required" "$STATUS $(field .error)"
call POST "$ER/$R4/return" '{"observations":"El documento de Lucía no está en la Registraduría. Corrígelo y radica de nuevo."}' "$OPERATIONS_TOKEN"
check "returning with observations: 200, returned" "200 true" "$STATUS $(field .request.returned)"
call GET "$ORIGINATION/v1/policies/change-requests?status=rejected" "" "$INSURED_TOKEN"
check "the insured sees it rejected with the reason" "El documento de Lucía no está en la Registraduría. Corrígelo y radica de nuevo." \
  "$(field ".items[] | select(.request_id == \"$R4\") | .decision_reason")"

echo "== [$(stamp)] E5. Rejection"
file "$P5" "$INSURED_AMOUNT"
R5="$REQ"
call POST "$ER/$R5/rejection" '{"observations":" "}' "$OPERATIONS_TOKEN"
check "rejecting without observations: 422" 422 "$STATUS"
call POST "$ER/$R5/rejection" '{"observations":"La suma pedida no corresponde al saldo del crédito."}' "$OPERATIONS_TOKEN"
check "rejecting with observations: 200 rejected" "200 rejected" "$STATUS $(field .request.status)"
check "the insured sees the reason" "rejected La suma pedida no corresponde al saldo del crédito." "$(insured_status "$R5")"
apply "$R5"
check "applying it afterwards: 409 request_already_decided" "409 request_already_decided rejected" "$STATUS $(field .error) $(field .status)"

echo "== [$(stamp)] E6. A claim in progress"
psql_in claims "INSERT INTO claim (id, filing_number, cognito_sub, filing_key, fingerprint, policy_id, incident_type, occurred_at,
  place, description, status, filed_at) VALUES ('claim-$RUN_ID', 'SIN-E2E-$RUN_ID', 'e2e', 'key-$RUN_ID', 'x', '$P6', 'death',
  now(), 'Bogotá', 'Siniestro de prueba', 'in_evaluation', now())" >/dev/null
curl -s "$CLAIMS/internal/policies/$P6/open-claims" -o "$WORK/body"
BODY="$(tr -d '\r' <"$WORK/body")"
check "claims counts it in progress on its internal route" 1 "$(field .open_claims)"
file "$P6" "$(beneficiaries 1142998310)"
R6="$REQ"
apply "$R6"
check "no_open_claim fails: not applied" "not_applied no_open_claim" "$(field .outcome) $(field '.attempt.failed | join(",")')"

echo "== [$(stamp)] E7. Two applications at once"
file "$P7" "$(beneficiaries 1142998310)"
R7="$REQ"
for i in 1 2; do
  printf 'User-Agent: hu-w1.4-e2e\nAuthorization: Bearer %s\n' "$OPERATIONS_TOKEN" >"$WORK/h$i"
  curl -s --max-time 20 -o "$WORK/b$i" -w '%{http_code}' -X POST -H @"$WORK/h$i" "$ER/$R7/application" >"$WORK/s$i" &
done
wait
check "both answer 200" "200 200" "$(cat "$WORK/s1") $(cat "$WORK/s2")"
E7A="$(jq -r .endorsement.endorsement_id <"$WORK/b1")"
E7B="$(jq -r .endorsement.endorsement_id <"$WORK/b2")"
check "with the same endorsement" "$E7A" "$E7B"
check "one endorsement stored" 1 "$(psql_in origination "SELECT count(*) FROM endorsement WHERE request_id = '$R7'")"

echo "== [$(stamp)] E8. KYC and claims down: nothing changes"
file "$P8" "$(beneficiaries 1142998310)"
R8B="$REQ"
file "$P8" "$INSURED_AMOUNT"
R8A="$REQ"
toxi kyc false
apply "$R8B"
check "KYC down: 503 validation_unverifiable" "503 validation_unverifiable identity_documents_valid" \
  "$STATUS $(field .error) $(field '.unverifiable | join(",")')"
check "the request did not change" filed "$(insured_status "$R8B" | cut -d' ' -f1)"
toxi kyc true
$COMPOSE stop claims >/dev/null
apply "$R8A"
check "claims down: 503 validation_unverifiable" "503 validation_unverifiable no_open_claim" \
  "$STATUS $(field .error) $(field '.unverifiable | join(",")')"
check "nothing was recorded" 0 "$(psql_in origination "SELECT count(*) FROM endorsement_attempt WHERE request_id IN ('$R8A', '$R8B')")"
$COMPOSE start claims >/dev/null
wait_healthy "$CLAIMS" claims
sleep 6 # the circuit of the claims client closes again after its open time
apply "$R8B"
check "KYC back: the beneficiaries apply" applied "$(field .outcome)"
apply "$R8A"
check "claims back: the insured value applies over the new version" "applied 2" "$(field .outcome) $(field .endorsement.version)"

echo "== [$(stamp)] E9. The history, before and after a restart"
history_of() { # <policy>
  call GET "$ORIGINATION/v1/policies/$1/endorsements" "" "$OPERATIONS_TOKEN"
  echo "$STATUS $(field '[.entries[] | "\(.outcome):\(.endorsement_id // "-"):\(.by.name):\(.failed | join("+"))"] | join(",")')"
}
H1="$(history_of "$P1")"
H4="$(history_of "$P4")"
check "the endorsement of E1 with its author" "200 applied:$END1:Andrés Ruiz:" "$H1"
check "the attempt of E4 with the validation that failed" "200 not_applied:-:Andrés Ruiz:identity_documents_valid" "$H4"
check "the history of a policy without endorsements is empty" "200 " "$(history_of "$P5")"
$COMPOSE restart origination >/dev/null
wait_healthy "$ORIGINATION" origination
check "the same after restarting origination (E1)" "$H1" "$(history_of "$P1")"
check "the same after restarting origination (E4)" "$H4" "$(history_of "$P4")"

echo "== [$(stamp)] E10. Roles"
ROUTES=("GET $ER" "GET $ER/$R1" "POST $ER/$R1/review" "POST $ER/$R1/application" "POST $ER/$R1/rejection"
  "POST $ER/$R1/return" "GET $ORIGINATION/v1/policies/$P1/endorsements")
for route in "${ROUTES[@]}"; do
  read -r method url <<<"$route"
  call "$method" "$url" '{"observations":"No procede el cambio pedido"}'
  no_token="$STATUS"
  call "$method" "$url" '{"observations":"No procede el cambio pedido"}' "$INSURED_TOKEN"
  insured="$STATUS"
  call "$method" "$url" '{"observations":"No procede el cambio pedido"}' "$ADVISOR_TOKEN"
  check "$method ${url#"$ORIGINATION"}: 401 without a token, 403 insured and advisor" "401 403 403" "$no_token $insured $STATUS"
done

echo "== [$(stamp)] E11. One e-mail per decision"
mail_count() { # <request>
  curl -s --max-time 10 "$MAIL/messages?to=$INSURED_EMAIL" | tr -d '\r' |
    jq -r "[.messages[] | select(.idempotency_key == \"endorsement-outcome:$1\")] | length"
}
for i in $(seq 1 20); do
  [[ "$(mail_count "$R5")" == 1 && "$(mail_count "$R4")" == 1 && "$(mail_count "$R1")" == 1 ]] && break
  sleep 1
done
check "applied (E1): one e-mail" 1 "$(mail_count "$R1")"
check "returned (E4): one e-mail" 1 "$(mail_count "$R4")"
check "rejected (E5): one e-mail" 1 "$(mail_count "$R5")"
check "not applied and not returned (E6): none yet" 0 "$(mail_count "$R6")"
call GET "$MAIL/messages?to=$INSURED_EMAIL"
check "the returned one carries the observations" true \
  "$(field "[.messages[] | select(.idempotency_key == \"endorsement-outcome:$R4\") | .body | contains(\"no está en la Registraduría\")] | first")"

echo "== [$(stamp)] Minimization of the logs"
LOGS="$($COMPOSE logs --no-log-prefix origination 2>/dev/null | grep -E 'endorsement|change request' || true)"
check_true "the logs of the endorsements exist" "$([[ -n "$LOGS" ]]; echo $?)"
check_true "they carry no document, name, e-mail nor phone" \
  "$(! grep -qE '1142998310|1142998000|Lucía|Mateo|contacto.e2e|3012345678' <<<"$LOGS"; echo $?)"

echo
if [[ "$FAILS" -gt 0 ]]; then
  echo "== $FAILS check(s) failed" >&2
  exit 1
fi
echo "== TODO OK: the controlled application of the endorsement (HU-W1.4-02) passed end to end."
