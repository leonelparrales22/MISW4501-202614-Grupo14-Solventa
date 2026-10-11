# Composición efímera `staging/ha-s01` — ventana de verificación de acceso de HA-S01

Levanta, solo durante una ventana corta, lo necesario para evidenciar en AWS lo que en local solo se prueba con proveedor
simulado: **CA-1** (401 del authorizer de Cognito), **CA-3** (alta solo administrativa y `custom:partner_id` no escribible),
**CA-4** (revocación) y **CA-5** (API key y usage plan separados de Cognito). Se aplica y se destruye **en la misma sesión**.

> **Estado:** código validado (`terraform fmt` y `validate`). **Nunca se ha aplicado.** Aplicarlo exige la autorización de la
> ventana (estimación de costo escrita y aprobada) y destruirlo en la misma sesión.

## Topología

```
curl ──▶ Cognito user pool (módulo cognito: grupos, clientes web y mobile, 3 usuarios semilla)
curl ──▶ API REST de prueba, integraciones MOCK (sin backend):
           GET /ha-s01/humana   authorizer COGNITO_USER_POOLS; devuelve las claims que recibió (sub, grupo, partner_id)
           GET /ha-s01/socio    solo API key + usage plan (5 req/s, ráfaga 10)
           OPTIONS en las dos   sin autorización (preflight CORS)
```

- **Sin VPC, NAT, ALB, Fargate, RDS ni Redis**; sin caché del stage ni logs de CloudWatch. Costo de la ventana ≈ US$ 0
  (Cognito Essentials gratis hasta 10 000 MAU; ~200 peticiones al API a US$ 3,50 por millón).
- La API de prueba replica lo que el módulo [`apigateway`](../../../modules/apigateway/README.md) hace con las rutas humanas
  (authorizer, `OPTIONS` sin autorización) y con las de socio (API key), sin necesitar el servicio desplegado. CA-2 contra
  un servicio real llega con la primera ruta humana (HU-W1.1-01).
- Etiquetas por defecto: `Project=solventa`, `Owner=nicolas`, `Env=staging`, `Story=HA-S01`, `TTL=<var.ttl>`. Región fija
  `us-east-2` (validada). El user pool se crea con `deletion_protection` inactiva para que `destroy` lo borre.
- Estado local y efímero (no hay estado remoto del equipo todavía). El `.terraform.lock.hcl` fija `hashicorp/aws` 6.68.0.

## Variables

| Variable | Defecto | Descripción |
|---|---|---|
| `ttl` | — (obligatoria) | Fecha `AAAA-MM-DD` límite; etiqueta `TTL` |
| `seed_user_password` | — (obligatoria, sensible) | Contraseña permanente de los usuarios semilla (D-13). **Solo** por `TF_VAR_seed_user_password`, nunca en un archivo |
| `seed_users` | asesor, operaciones y socio (`partner_id = socio-a`) con correos `@example.com` | Usuarios de prueba; no se envía ningún correo |
| `name`, `region` | `solventa-stg-ha-s01`, `us-east-2` | Nombres y región |

Salidas: `region`, `invoke_url`, `human_url`, `partner_url`, `user_pool_id`, `issuer`, `jwks_url`, `client_ids`,
`web_client_id`, `seed_user_emails`, `api_key_id` y `api_key_value` (sensible; clave de prueba que se destruye con la ventana).

### Plantilla de variables (`ha-s01.example.tfvars`)

[`ha-s01.example.tfvars`](ha-s01.example.tfvars) trae todas las variables con valores de ejemplo **menos la contraseña**
(convención del `.gitignore`: `*.tfvars` ignorado, `!*.example.tfvars` versionado). Para usarla:

1. Copiarla a `ha-s01.tfvars` (git la ignora) y completar `ttl` con la fecha límite real de la ventana (y, si hace falta,
   los usuarios semilla).
2. Definir la contraseña solo en el entorno: `export TF_VAR_seed_user_password='…'`. **Nunca** se escribe en la plantilla
   ni en la copia.
3. `terraform -chdir=infra/terraform/environments/staging/ha-s01 plan -var-file=ha-s01.tfvars` (y `apply`/`destroy` con el
   mismo `-var-file` cuando la ventana esté autorizada).

## Procedimiento de la ventana (paso I4 del plan de HA-S01; no ejecutado)

Requisitos previos: estimación de costo escrita y autorizada, credenciales de la cuenta del proyecto con MFA, y permisos sobre
`cognito-idp` y `apigateway`.

1. `export TF_VAR_seed_user_password='…'` (cumple la política: 12+ caracteres con mayúsculas, minúsculas, números y símbolos).
2. `terraform -chdir=infra/terraform/environments/staging/ha-s01 init` y `… apply -var-file=ha-s01.tfvars` (o `-var ttl=<fecha>`
   sin archivo de variables).
3. `bash tests/security/ha-s01/verify-access.sh` y, para el caso del token vencido, `WAIT_FOR_EXPIRY=1 bash …`
   ([guion y casos](../../../../../tests/security/ha-s01/README.md)). Registrar la salida, en especial las líneas `RECORD` (D-6).
4. `terraform … destroy -var-file=ha-s01.tfvars` en la misma sesión y comprobar `terraform state list` vacío.

## Local (Moto) ↔ AWS: qué cambia

En el portátil, Cognito se emula con Moto ([`tests/security/ha-s01/local`](../../../../../tests/security/ha-s01/local/README.md)).
Pasar de ahí a esta composición solo cambia configuración:

| Aspecto | Local (Moto) | AWS (esta composición) |
|---|---|---|
| Quién crea el pool, grupos, clientes y usuarios | `seed.sh` con la CLI de AWS contra `http://localhost:5100` | Módulo `cognito` (`terraform apply`) |
| Endpoint de Cognito del front | `cognito.endpoint` en `environment.development.ts` | Sin `endpoint` (el regional de `us-east-2`) |
| `AUTH_ISSUER` / `AUTH_JWKS_URL` del backend | Emisor con formato Cognito + JWKS de Moto (obligatorio) | Salidas `issuer` y `jwks_url` (el JWKS se deduce del emisor) |
| Pool y clientes | IDs deterministas de Moto | Salidas `user_pool_id`, `web_client_id`, `client_ids` |
| Vigencia de los tokens | 1 h fija | 5 min (ID y access) |
| `RevokeToken` / `SignUp` | No implementado / se acepta | Revoca / `NotAuthorizedException`: se verifican aquí (CA-3, CA-4) |
| Authorizer del API Gateway y API key de socios | No existen en local | `GET /ha-s01/humana` y `GET /ha-s01/socio` (CA-1, CA-5) |

Validación local (sin AWS): `terraform -chdir=infra/terraform/environments/staging/ha-s01 init -backend=false` y `… validate`.
Con credenciales de solo lectura también se puede revisar el plan con la plantilla tal cual:
`TF_VAR_seed_user_password=<ficticia> terraform … plan -var-file=ha-s01.example.tfvars` (no aplica nada).
