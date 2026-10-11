#!/usr/bin/env bash
# Seeds the local Cognito (Moto, ./docker-compose.yml) with what HA-S01 needs,
# mirroring infra/terraform/modules/cognito:
#   - user pool: e-mail as username, sign-up only by an administrator,
#     password policy and the custom:partner_id attribute
#   - groups insured, advisor, operations and partner (one per role)
#   - app clients web and mobile without secret (USER_PASSWORD_AUTH + refresh)
#   - one user per role with a permanent password (D-13); the partner user
#     carries custom:partner_id
#
# Safety: it only talks to a local endpoint (anything else is refused), with
# fake credentials, and the users' password comes from MOTO_USER_PASSWORD:
# it is never written to a file nor printed.
#
# Usage (from the repository root):
#   docker compose -f tests/security/ha-s01/local/docker-compose.yml up -d --wait
#   export MOTO_USER_PASSWORD=...   # 12+ chars with upper, lower, digit and symbol
#   bash tests/security/ha-s01/local/seed.sh
# Option (environment):
#   MOTO_URL   Moto endpoint (default http://localhost:5100); must be local
set -euo pipefail

MOTO_URL="${MOTO_URL:-http://localhost:${MOTO_PORT:-5100}}"
MOTO_URL="${MOTO_URL%/}"
REGION="us-east-2"
POOL_NAME="solventa-local-ha-s01"
PARTNER_ID="socio-a"

# Never against AWS: only http(s)://localhost, 127.0.0.1 or [::1], optional port.
if [[ ! "$MOTO_URL" =~ ^https?://(localhost|127\.0\.0\.1|\[::1\])(:[0-9]+)?$ ]]; then
  echo "Refusing to run: MOTO_URL must be a local endpoint such as http://localhost:5100 (got '$MOTO_URL')." >&2
  exit 2
fi

PASSWORD="${MOTO_USER_PASSWORD:-}"
if [[ -z "$PASSWORD" ]]; then
  echo "Set MOTO_USER_PASSWORD with the password of the local users (12+ chars: upper, lower, digit, symbol)." >&2
  exit 2
fi

# Fake credentials only; ignore any profile of the machine.
unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
export AWS_REGION="$REGION" AWS_DEFAULT_REGION="$REGION" AWS_PAGER="" AWS_EC2_METADATA_DISABLED=true

if ! curl -sf -o /dev/null "$MOTO_URL/moto-api/"; then
  echo "Moto is not answering at $MOTO_URL. Start it first:" >&2
  echo "  docker compose -f tests/security/ha-s01/local/docker-compose.yml up -d --wait" >&2
  exit 1
fi

# Every call goes to the local endpoint; text output without the CR of Windows.
idp() {
  aws cognito-idp --endpoint-url "$MOTO_URL" --region "$REGION" --output text "$@" | tr -d '\r'
}

client_id_of() { # suffix
  idp list-user-pool-clients --user-pool-id "$POOL_ID" --max-results 60 \
    --query "UserPoolClients[?ClientName=='$POOL_NAME-$1'].ClientId | [0]"
}

# One user per role: key, e-mail, name, group.
USERS=(
  "insured|asegurado.local@example.com|Carolina Pérez|insured"
  "advisor|asesor.local@example.com|Laura Gómez|advisor"
  "operations|operaciones.local@example.com|Andrés Ruiz|operations"
  "partner|socio.local@example.com|Banco Aliado|partner"
)

POOL_ID="$(idp list-user-pools --max-results 60 --query "UserPools[?Name=='$POOL_NAME'].Id | [0]")"
if [[ -n "$POOL_ID" && "$POOL_ID" != "None" ]]; then
  echo "== Already seeded (docker compose ... down -v to start from scratch)"
else
  echo "== Creating the user pool $POOL_NAME"
  POOL_ID="$(idp create-user-pool --pool-name "$POOL_NAME" \
    --username-attributes email --auto-verified-attributes email \
    --username-configuration CaseSensitive=false \
    --admin-create-user-config AllowAdminCreateUserOnly=true \
    --policies 'PasswordPolicy={MinimumLength=12,RequireUppercase=true,RequireLowercase=true,RequireNumbers=true,RequireSymbols=true,TemporaryPasswordValidityDays=7}' \
    --account-recovery-setting 'RecoveryMechanisms=[{Priority=1,Name=verified_email}]' \
    --schema 'Name=partner_id,AttributeDataType=String,Mutable=true,Required=false,DeveloperOnlyAttribute=false,StringAttributeConstraints={MinLength=1,MaxLength=64}' \
    --query UserPool.Id)"

  echo "== Creating the role groups"
  idp create-group --user-pool-id "$POOL_ID" --group-name operations --precedence 10 --description "Operations analysts" >/dev/null
  idp create-group --user-pool-id "$POOL_ID" --group-name advisor --precedence 20 --description "Sales advisors" >/dev/null
  idp create-group --user-pool-id "$POOL_ID" --group-name partner --precedence 30 --description "Partner portal users (with custom:partner_id)" >/dev/null
  idp create-group --user-pool-id "$POOL_ID" --group-name insured --precedence 40 --description "Insured customers" >/dev/null

  echo "== Creating the web and mobile clients"
  for client in "web 12 hours" "mobile 30 days"; do
    read -r suffix refresh unit <<<"$client"
    idp create-user-pool-client --user-pool-id "$POOL_ID" --client-name "$POOL_NAME-$suffix" --no-generate-secret \
      --explicit-auth-flows ALLOW_USER_PASSWORD_AUTH ALLOW_REFRESH_TOKEN_AUTH \
      --enable-token-revocation --prevent-user-existence-errors ENABLED \
      --id-token-validity 5 --access-token-validity 5 --refresh-token-validity "$refresh" \
      --token-validity-units "IdToken=minutes,AccessToken=minutes,RefreshToken=$unit" \
      --read-attributes email email_verified name custom:partner_id --write-attributes name >/dev/null
  done

  echo "== Creating one user per role"
  for user in "${USERS[@]}"; do
    IFS='|' read -r key email name group <<<"$user"
    attributes=("Name=email,Value=$email" "Name=email_verified,Value=true" "Name=name,Value=$name")
    if [[ "$key" == "partner" ]]; then
      attributes+=("Name=custom:partner_id,Value=$PARTNER_ID")
    fi
    idp admin-create-user --user-pool-id "$POOL_ID" --username "$email" \
      --user-attributes "${attributes[@]}" --message-action SUPPRESS >/dev/null
    idp admin-set-user-password --user-pool-id "$POOL_ID" --username "$email" --password "$PASSWORD" --permanent >/dev/null
    idp admin-add-user-to-group --user-pool-id "$POOL_ID" --username "$email" --group-name "$group" >/dev/null
  done
fi

WEB_CLIENT_ID="$(client_id_of web)"
MOBILE_CLIENT_ID="$(client_id_of mobile)"
ISSUER="https://cognito-idp.$REGION.amazonaws.com/$POOL_ID"

cat <<EOF

Local Cognito (Moto) ready at $MOTO_URL
  user pool      $POOL_ID
  web client     $WEB_CLIENT_ID
  mobile client  $MOBILE_CLIENT_ID
  issuer         $ISSUER   (same format as Cognito; Moto signs the tokens)
  JWKS           $MOTO_URL/$POOL_ID/.well-known/jwks.json
  users (password: \$MOTO_USER_PASSWORD)
EOF
for user in "${USERS[@]}"; do
  IFS='|' read -r key email name group <<<"$user"
  extra=""
  [[ "$key" == "partner" ]] && extra="  custom:partner_id=$PARTNER_ID"
  printf '    %-11s %-32s %s\n' "$group" "$email" "$extra"
done
cat <<EOF

Backend (pkg/security) against this pool:
  export AUTH_ISSUER=$ISSUER
  export AUTH_JWKS_URL=$MOTO_URL/$POOL_ID/.well-known/jwks.json
  export AUTH_CLIENT_IDS=$WEB_CLIENT_ID,$MOBILE_CLIENT_ID
Integration test of pkg/security:
  AUTH_MOTO_URL=$MOTO_URL go test -count=1 -run Moto ./pkg/security/...
Front (src/environments/environment.development.ts):
  cognito: { region: '$REGION', endpoint: '$MOTO_URL/', userPoolId: '$POOL_ID', clienteId: '$WEB_CLIENT_ID' }
EOF
