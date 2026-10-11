# Composición efímera `staging/ha-s01` — ventana de verificación de acceso de HA-S01

Levanta, solo durante una ventana corta, lo necesario para evidenciar en AWS lo que en local solo se prueba con proveedor
simulado: **CA-1** (401 del authorizer de Cognito), **CA-2** (tokens reales verificados por `pkg/security` y el JWT que llega a
la integración), **CA-3** (alta solo administrativa y `custom:partner_id` no escribible), **CA-4** (revocación y ventana
residual medida) y **CA-5** (API key y usage plan separados de Cognito). Se aplica y se destruye **en la misma sesión**.

> **Estado:** **aplicada y destruida el 2026-10-11** (14:51–15:43, `us-east-2`, 82 recursos más el ajuste del 401; estado remoto en
> el bucket de T4). Cualquier ventana nueva exige otra vez la autorización (estimación de costo escrita y aprobada) y destruirla en la
> misma sesión.

### Resultados de la ventana del 2026-10-11

| Criterio | Resultado medido en AWS |
|---|---|
| CA-1 | Sin token, con basura o con un token de otro emisor → `401`; token **vencido** → `401`; token del pool con la **firma o el contenido alterados** → AWS respondía `403` (`ACCESS_DENIED`) y, tras mapear `ACCESS_DENIED` → `401` en el módulo y en la API de prueba, → `401` en las dos APIs. El `401` del módulo lleva `Access-Control-Allow-Origin` y el `OPTIONS` responde `200` sin token |
| CA-2 | El módulo `apigateway` real **reenvía `Authorization`** a la integración (el *backend* de prueba devolvió las claims). `pkg/security` verificó y autorizó **tokens reales** de los 4 roles y de los dos clientes: 5/5 pruebas, `403` por rol y por titularidad. El tramo «servicio desplegado» queda para HU-W1.1-01 |
| CA-3 | `SignUp` con el cliente web → `NotAuthorizedException`; `UpdateUserAttributes(custom:partner_id)` → rechazado; cada usuario semilla en un solo grupo, el suyo |
| CA-4 | `RevokeToken` → `200`; renovación con el refresh revocado → rechazada; el ID token revocado siguió pasando **310 s** después de revocarlo y dejó de pasar a `exp` + 19–20 s (cota: `exp` + 30 s); `GetUser` con el access token revocado → `NotAuthorizedException` |
| CA-5 | Ruta humana solo con API key → `401`; ruta de socio solo con *Bearer* → `403`; ruta de socio con API key → `200` |
| Front | Inicio de sesión de los 4 roles contra este pool, renovación silenciosa a los 5 min (`REFRESH_TOKEN_AUTH`) y cierre de sesión (`RevokeToken`) verificados desde el navegador (CloudTrail) |

## Topología

```
curl / navegador ──▶ Cognito user pool (módulo cognito: 4 grupos, clientes web y mobile, 4 usuarios semilla)

curl ──▶ API REST de prueba, integraciones MOCK (sin backend, sin caché ni logs):
           GET /ha-s01/humana     authorizer COGNITO_USER_POOLS; devuelve las claims que recibió (sub, grupo, partner_id)
           GET /ha-s01/socio      solo API key + usage plan (5 req/s, ráfaga 10)
           OPTIONS en las dos     sin autorización (preflight CORS)
           GET /v1/ha-s01-check   mismo authorizer; devuelve las claims ("route":"module-backend"): backend del módulo real

curl ──▶ API del módulo apigateway REAL (caché del stage 0,5 GB, métricas detalladas):
           GET /v1/ha-s01-check   ruta humana: authorizer de Cognito → HTTP_PROXY a /v1/ha-s01-check de la API de prueba
           OPTIONS                sin autorización; respuestas 4XX del gateway con cabeceras CORS
           GET /v1/risk-profiles/{customer_id}   ruta de socio: solo API key (su backend no existe en la ventana)
```

- El módulo [`apigateway`](../../../modules/apigateway/README.md) se usa **tal cual** (el mismo artefacto que usará el equipo):
  `cognito_user_pool_arns` con el pool de la ventana, `human_routes = { ha-s01-check = GET }`, un socio (`socio-a`, 5 req/s,
  ráfaga 10) y `cors_allow_origin` en su valor por defecto `*`. Su «backend» es la API de prueba (integración `INTERNET`, sin
  VPC link). Como esa ruta tiene el mismo authorizer, **un 200 con las claims prueba que el módulo reenvía la cabecera
  `Authorization` a su integración** (decisión D-1: cada servicio vuelve a verificar el JWT con `pkg/security`).
- **Sin VPC, NAT, ALB, Fargate, RDS ni Redis**; sin logs de CloudWatch. CA-2 contra un servicio de Solventa desplegado llega con
  la primera ruta humana real (HU-W1.1-01).
- Etiquetas por defecto: `Project=solventa`, `Owner=nicolas`, `Env=staging`, `Story=HA-S01`, `TTL=<var.ttl>`. Región fija
  `us-east-2` (validada). El user pool se crea con `deletion_protection` inactiva para que `destroy` lo borre.
- **Estado remoto** en el bucket del proyecto `solventa-tfstate-368083581678` (tarea T4), clave `staging/ha-s01/terraform.tfstate`,
  con bloqueo nativo de S3 (`use_lockfile`, Terraform ≥ 1.11) y cifrado. **No queda estado en disco** (solo la configuración del
  backend en `.terraform/`, ignorada por git). Las versiones anteriores del objeto de estado (que contienen la contraseña de prueba
  y las API keys, inútiles tras el destroy) las borra a los 30 días la regla de ciclo de vida del bucket. El `.terraform.lock.hcl` fija
  `hashicorp/aws` 6.68.0.

### Costo

| Recurso | Costo/h aprox. |
|---|---|
| Cognito user pool (Essentials, 4 usuarios) | US$ 0 (gratis hasta 10 000 MAU) |
| API REST de prueba (MOCK, sin caché ni logs) | US$ 0 por hora; US$ 3,50 por millón de peticiones |
| Caché del stage del módulo (0,5 GB) | ≈ US$ 0,020 |
| Métricas detalladas del stage del módulo (~40 métricas) | ≈ US$ 0,016 |
| **Total encendida** | **≈ US$ 0,036/h** (≈ US$ 0,86 al día si se olvida: destruir en la misma sesión) |

## Variables

| Variable | Defecto | Descripción |
|---|---|---|
| `ttl` | — (obligatoria) | Fecha `AAAA-MM-DD` límite; etiqueta `TTL` |
| `seed_user_password` | — (obligatoria, sensible) | Contraseña permanente de los usuarios semilla (D-13). **Solo** por `TF_VAR_seed_user_password`, nunca en un archivo |
| `seed_users` | asesor, operaciones, socio (`partner_id = socio-a`) y asegurado, con correos `@example.com` | Usuarios de prueba, uno por rol; no se envía ningún correo. El asegurado es un dato de prueba: en producción llega por KYC (HU-M1.1-01) |
| `name`, `region` | `solventa-stg-ha-s01`, `us-east-2` | Nombres y región |

Salidas: `region`, `invoke_url`, `human_url`, `partner_url`, `user_pool_id`, `issuer`, `jwks_url`, `client_ids`,
`web_client_id`, `mobile_client_id`, `seed_user_emails`, `api_key_id`, `api_key_value` (sensible), `module_invoke_url`,
`module_human_url`, `module_partner_url` y `module_api_key_value` (sensible). Las dos API keys son de prueba y se destruyen con la
ventana.

### Plantilla de variables (`ha-s01.example.tfvars`)

[`ha-s01.example.tfvars`](ha-s01.example.tfvars) trae todas las variables con valores de ejemplo **menos la contraseña**
(convención del `.gitignore`: `*.tfvars` ignorado, `!*.example.tfvars` versionado). Para usarla:

1. Copiarla a `ha-s01.tfvars` (git la ignora) y completar `ttl` con la fecha límite real de la ventana (y, si hace falta,
   los usuarios semilla).
2. Definir la contraseña solo en el entorno (`TF_VAR_seed_user_password`). **Nunca** se escribe en la plantilla ni en la copia.
3. `terraform -chdir=infra/terraform/environments/staging/ha-s01 plan -var-file=ha-s01.tfvars` (y `apply`/`destroy` con el
   mismo `-var-file` cuando la ventana esté autorizada).

## Procedimiento de la ventana (no ejecutado)

Requisitos previos: estimación de costo escrita y autorizada, credenciales de la cuenta del proyecto, permisos sobre
`cognito-idp` y `apigateway`, y el bucket del estado remoto (T4).

**Secretos.** La contraseña de los usuarios semilla y las dos API keys quedan en el estado (remoto) y en cualquier plan guardado:

- La contraseña se genera al azar y vive solo en el entorno. En Windows, una variable de usuario (p. ej.
  `SOLVENTA_HA_S01_SEED_PASSWORD`) que cada comando lee sin imprimirla y exporta como `TF_VAR_seed_user_password`. Debe cumplir la
  política del pool: 12+ caracteres con mayúsculas, minúsculas, números y símbolos.
- El plan guardado (`-out`) va **fuera del repositorio** (un directorio temporal): `.gitignore` no ignora `*.tfplan`. Se borra
  después del `apply`.
- Nunca `terraform output -json` sin nombre de salida, `terraform show -json`, `output -raw` de `api_key_value` o
  `module_api_key_value` fuera del guion, `TF_LOG` ni `set -x`.

**Pasos** (`D=infra/terraform/environments/staging/ha-s01`):

1. `terraform -chdir=$D init -input=false` (backend S3) y `terraform -chdir=$D plan -input=false -var ttl=<fecha> -out=<tmp>/w1.tfplan`.
   Comprobar que el plan solo trae tipos `aws_cognito_*` y `aws_api_gateway_*`.
2. `terraform -chdir=$D apply -input=false <tmp>/w1.tfplan` y borrar el plan.
3. `bash tests/security/ha-s01/verify-access.sh` y, para la cota de revocación y el token vencido,
   `WAIT_FOR_EXPIRY=1 bash …` ([guion y casos](../../../../../tests/security/ha-s01/README.md)). Registrar la salida, en especial
   las líneas `RECORD`.
4. Prueba de `pkg/security` con tokens reales: `AUTH_STAGING_ISSUER`, `AUTH_STAGING_CLIENT_IDS` (salidas `issuer`,
   `web_client_id` y `mobile_client_id`) y `go test -count=1 -run CognitoStaging -v ./pkg/security/...`.
5. `terraform -chdir=$D destroy -input=false -auto-approve -var ttl=<fecha>` en la misma sesión (siempre con `-chdir`; el destroy
   no necesita la contraseña real: cualquier valor sirve) y comprobar `terraform -chdir=$D state list` vacío. Después, borrar la
   variable de la contraseña.

## Local (Moto) ↔ AWS: qué cambia

En el portátil, Cognito se emula con Moto ([`tests/security/ha-s01/local`](../../../../../tests/security/ha-s01/local/README.md)).
Pasar de ahí a esta composición solo cambia configuración:

| Aspecto | Local (Moto) | AWS (esta composición) |
|---|---|---|
| Quién crea el pool, grupos, clientes y usuarios | `seed.sh` con la CLI de AWS contra `http://localhost:5100` | Módulo `cognito` (`terraform apply`) |
| Endpoint de Cognito del front | `cognito.endpoint` en `environment.development.ts` | Sin `endpoint` (el regional de `us-east-2`) |
| `AUTH_ISSUER` / `AUTH_JWKS_URL` del backend | Emisor con formato Cognito + JWKS de Moto (obligatorio) | Salidas `issuer` y `jwks_url` (el JWKS se deduce del emisor) |
| Pool y clientes | IDs deterministas de Moto | Salidas `user_pool_id`, `web_client_id`, `mobile_client_id`, `client_ids` |
| Vigencia de los tokens | 1 h fija | 5 min (ID y access) |
| `RevokeToken` / `SignUp` | No implementado / se acepta | Revoca / `NotAuthorizedException`: se verifican aquí (CA-3, CA-4) |
| Authorizer del API Gateway y API key de socios | No existen en local | `GET /ha-s01/humana` y `GET /ha-s01/socio` (CA-1, CA-5) |
| Módulo `apigateway` real (authorizer en `human_routes`, 4XX con CORS, `OPTIONS`, reenvío del JWT) | Solo `terraform test` del módulo, con proveedor simulado | `GET /v1/ha-s01-check` del módulo → backend de prueba que devuelve las claims (CA-1, CA-2, CA-5) |

Validación local (sin AWS): `terraform -chdir=infra/terraform/environments/staging/ha-s01 init -backend=false` y `… validate`.
Con credenciales de solo lectura también se puede revisar el plan con la plantilla tal cual, sin escribir nada en el bucket:
`terraform … init` y `TF_VAR_seed_user_password=<ficticia> terraform … plan -lock=false -var-file=ha-s01.example.tfvars` (no
aplica nada; un `plan` no escribe estado y `-lock=false` evita el archivo de bloqueo).
