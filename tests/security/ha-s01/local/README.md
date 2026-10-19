# `security/ha-s01/local` — Cognito local con Moto (HA-S01)

Cognito **en el portátil, sin AWS**, para el ciclo manual del portal web y del backend: [Moto](https://github.com/getmoto/moto)
en Docker emula el API de Cognito User Pools (inicio de sesión, renovación, operaciones de administrador) y publica el JWKS de
cada pool, así que los tokens que emite se verifican con `pkg/security` igual que los de Cognito. Lo usan:

- la prueba de integración `pkg/security/moto_integration_test.go` (omitida salvo que exista `AUTH_MOTO_URL`; el CI no cambia);
- el portal web en local (`src/environments/environment.development.ts` del front apunta aquí) y su E2E de humo `local-moto`,
  que no corre en el CI.

> **Solo local.** Nada de esto entra al código de producción: es configuración y archivos bajo `tests/`. En AWS el pool lo
> crea el módulo [`cognito`](../../../../infra/terraform/modules/cognito/README.md) de Terraform y se verifica con la
> [ventana de staging](../../../../infra/terraform/environments/staging/ha-s01/README.md).

## Qué siembra (`seed.sh`)

Lo mismo que el módulo `cognito`, en la región del proyecto (`us-east-2`):

| Elemento | Valor |
|---|---|
| User pool | `solventa-local-ha-s01`: correo como usuario, alta solo por un administrador, política de contraseñas (12+, mayúsculas, minúsculas, números y símbolos), atributo `custom:partner_id` |
| Grupos | `operations` (10), `advisor` (20), `partner` (30), `insured` (40) |
| Clientes | `solventa-local-ha-s01-web` y `-mobile`, sin secreto, `ALLOW_USER_PASSWORD_AUTH` + `ALLOW_REFRESH_TOKEN_AUTH`, revocación activa, `custom:partner_id` legible y no escribible |
| Usuarios (contraseña permanente, D-13) | `asegurado.local@example.com` (insured), `asesor.local@example.com` (advisor), `operaciones.local@example.com` (operations), `socio.local@example.com` (partner, `custom:partner_id = socio-a`) |

Los IDs del pool y de los clientes son **deterministas** (`MOTO_COGNITO_IDP_USER_POOL_ID_STRATEGY=HASH` y
`MOTO_COGNITO_IDP_USER_POOL_CLIENT_ID_STRATEGY=HASH` en el compose): dependen de la región, el nombre y los parámetros, así que
cada `down -v` + siembra produce los mismos y la configuración local del front no cambia. Con la siembra actual:

| Dato | Valor |
|---|---|
| User pool | `us-east-2_88555728a50459bf24b7f9852bd1309c320c7b54aae1b` |
| Cliente web | `9dbed27502f38ace4281ec963a` |
| Cliente móvil | `eb253e97af9e24e813e7265633` |

Si cambian los parámetros de `seed.sh`, cambian los IDs: el guion los imprime al final y hay que copiarlos al ambiente local del
front.

Seguridad del guion: **se niega a correr** si `MOTO_URL` no es `http(s)://localhost`, `127.0.0.1` o `[::1]`; usa credenciales
ficticias (`AWS_ACCESS_KEY_ID=test`) e ignora los perfiles de la máquina; la contraseña de los usuarios sale **solo** de
`MOTO_USER_PASSWORD` (nunca se escribe en un archivo ni se imprime). Si el pool ya existe, solo muestra los datos.

## Uso

Desde la raíz del repo (en Git Bash de Windows, anteponer `MSYS_NO_PATHCONV=1` a `docker compose`):

```bash
docker compose -f tests/security/ha-s01/local/docker-compose.yml up -d --wait     # Moto en 127.0.0.1:5100
export MOTO_USER_PASSWORD='…'    # 12+ caracteres con mayúsculas, minúsculas, números y símbolos; la eliges tú
bash tests/security/ha-s01/local/seed.sh                                          # ~1 min (la CLI de AWS es lenta en Windows)

# Prueba de integración de pkg/security contra Moto
AUTH_MOTO_URL=http://localhost:5100 go test -count=1 -run Moto ./pkg/security/...

# Un servicio en el portátil contra este pool (valores que imprime seed.sh)
export AUTH_ISSUER=https://cognito-idp.us-east-2.amazonaws.com/<user pool>
export AUTH_JWKS_URL=http://localhost:5100/<user pool>/.well-known/jwks.json
export AUTH_CLIENT_IDS=<cliente web>,<cliente móvil>

# Apagar y borrar todo
docker compose -f tests/security/ha-s01/local/docker-compose.yml down -v
```

| Variable | Defecto | Uso |
|---|---|---|
| `MOTO_USER_PASSWORD` | — (obligatoria) | Contraseña de los cuatro usuarios: la usan `seed.sh`, la prueba de integración y el E2E `local-moto` del front |
| `MOTO_PORT` | `5100` | Puerto publicado en `127.0.0.1` (el 5000 de Moto choca con AirPlay en macOS). Si cambia, cambia también `MOTO_URL` y el ambiente local del front |
| `MOTO_URL` | `http://localhost:5100` | Endpoint que usa `seed.sh` (solo local) |
| `AUTH_MOTO_URL` | — | Activa `moto_integration_test.go` |

Si el servicio corre **dentro** de Docker, `localhost` no es el portátil: usar `http://host.docker.internal:5100` en
`AUTH_JWKS_URL`.

## Local (Moto) ↔ AWS: qué cambia

Solo cambia la **configuración**; el código de `pkg/security` y del portal es el mismo.

| Aspecto | Local (Moto) | AWS (staging / producción) |
|---|---|---|
| Endpoint de Cognito (front) | `cognito.endpoint = 'http://localhost:5100/'` en `environment.development.ts` | Sin `endpoint`: `https://cognito-idp.us-east-2.amazonaws.com/` (`environment.staging.ts`, `environment.ts`) |
| Pool y cliente (front) | IDs deterministas de la tabla de arriba | Salidas `user_pool_id` y `web_client_id` del módulo `cognito` |
| `AUTH_ISSUER` (backend) | `https://cognito-idp.us-east-2.amazonaws.com/<pool de Moto>` (Moto emite el mismo formato) | `https://cognito-idp.us-east-2.amazonaws.com/<pool>` (salida `issuer`) |
| `AUTH_JWKS_URL` (backend) | **Obligatoria**: `http://localhost:5100/<pool>/.well-known/jwks.json` | Se omite: por defecto `AUTH_ISSUER + /.well-known/jwks.json` |
| `AUTH_CLIENT_IDS` | Clientes web y móvil de Moto | Salida `client_ids` |
| Firma | RS256 con una llave fija de Moto (`kid = dummy`) | RS256 con las llaves rotadas por Cognito |
| Vigencia de los tokens | **1 hora siempre** (Moto ignora la vigencia del cliente) | 5 minutos (ID y access), renovación 12 h web / 30 días móvil |
| `RevokeToken` | **No implementado** (HTTP 500); el portal borra la sesión igual y la renovación sigue funcionando | Revoca el token de renovación (CA-4; el comportamiento con el ID token se mide en staging) |
| `SignUp` con el cliente web | **Se acepta** (Moto no aplica `AllowAdminCreateUserOnly`) | `NotAuthorizedException` (CA-3) |
| Alta de usuarios | `seed.sh` con la CLI de AWS contra Moto | Terraform (usuarios semilla) + runbook (D-2) |
| CORS de Cognito | Moto responde el *preflight* del navegador | Cognito lo resuelve |

Por eso CA-3 (sin autorregistro) y CA-4 (revocación, vencimiento a los 5 min) **siguen pendientes en staging**: Moto no los
reproduce. Lo que sí se prueba aquí: tokens reales con `cognito:groups` y `custom:partner_id`, su verificación por `pkg/security`
y el ciclo del portal (inicio de sesión por rol, menú propio y cierre de sesión).
