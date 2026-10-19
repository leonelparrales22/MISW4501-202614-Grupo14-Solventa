# `tests/e2e/portal-web` — humo del portal web en la nube (T5)

Humo posterior al despliegue del portal web: el *build* de Angular servido por CloudFront desde un bucket S3 privado, con el API
Gateway de la plataforma en el **mismo dominio** (`/v1/*`, `/pasarela/*`). Todo se pide por la URL del portal, como lo haría el
navegador. Solo `bash` y `curl` (no hay `jq`); las lecturas de AWS son de solo lectura.

> **Estado:** guion listo (`bash -n` y `--help` verificados). Se corre por primera vez en la ventana de staging de T5, con
> `scripts/environment/up.sh staging` (fase `smoke`).

## Fases

| Fase | Qué comprueba |
|---|---|
| W-1 `static` | `/` y las rutas profundas de la SPA (`/embebido/oferta/<id>`, `/originacion/bandeja`) devuelven `index.html`; `config.json` responde `no-store`, solo con `apiGatewayUrl`, `pasarelaPagosUrl` y `cognito`, con el portal como API y el `userPoolId` del pool de la plataforma (el permanente de `bootstrap/identity`); un *bundle* es `immutable`; un archivo inexistente responde el 403 de S3; HSTS, `nosniff` y `X-Frame-Options` |
| W-2 `bucket` | El bucket pedido directo a S3 responde 403 y su política no es pública |
| W-3 `api` | `/v1/review-requests` por CloudFront: 401 JSON sin token, 200 con el token del asesor, `X-Cache: Miss` dos veces (nunca se cachea), 403 JSON para una ruta inexistente |
| W-4 `partner` | Primero registra por CloudFront el consentimiento de `cliente-<run>` (HU-W1.2-01; la misma función de W-9). Luego `POST /v1/risk-profiles/resolve` por CloudFront sin key → 200 (la agrega CloudFront), y con una key inventada → 200 (CloudFront la pisa); `sin-consentimiento-<run>` → `422 consent_required` (originación sí pregunta a `audit`, CA-3); directo al gateway sin key → `403 {"message":"Forbidden"}` |
| W-5 `tokens` | `POST /pasarela/tokens` con `4111…` → 201 `tok_…`; número inválido → 400; `/pasarela/charges` y `/pasarela/debug/charges` → 403 |
| W-6 `journey` | Oferta `demo-active-<run>` → tarjeta tokenizada por el portal → cobro confirmado → emisión completa → enlace del correo en el portal → PDF por CloudFront con su SHA-256; tarjeta `…0002` → `payment_rejected` |
| W-7 `links` | En la definición de tarea de originación, `OFFER_LINK_BASE_URL` y `DOCUMENTS_PUBLIC_BASE_URL` son el portal |
| W-8 `publish` | `index.html` con `no-cache`, un *bundle* `immutable`, `config.json` con el mismo ETag que escribió Terraform y la última invalidación `Completed` |
| W-9 `audit` | Por CloudFront: `GET /v1/audit/consent-terms/current` → 200; `POST /v1/audit/consents` → 201 (200 si W-4 ya lo registró en la misma corrida) y repetirlo → 200 con el mismo `consent_id` (contrato HU-W1.2-01 §2.2); `POST /v1/offers` con ese `consent_id` → 201 `off-…`; con el `consent_id` de un segundo cliente que también consintió (`cliente-<run>-b`) → `422 consent_mismatch`; `GET /v1/offers/{id}` sin token → 401 y con el token del asegurado → 200 (la ruta humana sigue con Cognito); `POST /v1/offers` directo al gateway sin key → 403 |
| W-10 `internal` | `GET /v1/audit/consent-verifications?customer_id=cliente-<run>` por CloudFront y directo al gateway → 403 (no está publicada); en la definición de tarea de originación, `AUDIT_URL` es la salida `audit_url` de la plataforma (`http://audit.<nombre>.local:8080`, Cloud Map); el log de originación desde el arranque de su tarea trae `consent (HU-W1.2-01): verified with audit at …` y ninguna línea `AUDIT_URL is not set`; el *namespace* de Cloud Map tiene el servicio `audit` (lecturas de AWS) |

## Uso

```bash
# Desde la raíz del repo, con AWS_PROFILE=solventa y las composiciones inicializadas (lo hace up.sh).
SEED_USER_PASSWORD="$SOLVENTA_IDENTITY_SEED_PASSWORD" bash tests/e2e/portal-web/smoke.sh            # todas (W-1 a W-10)
SEED_USER_PASSWORD="$SOLVENTA_IDENTITY_SEED_PASSWORD" bash tests/e2e/portal-web/smoke.sh static api
bash tests/e2e/portal-web/smoke.sh partner internal                                              # sin contraseña
bash tests/e2e/portal-web/smoke.sh --help
```

| Variable | Defecto | Para qué |
|---|---|---|
| `ENVIRONMENT` | `staging` | `staging` o `production-demo`: elige las composiciones por defecto |
| `WEB_TF_DIR` / `PLATFORM_TF_DIR` | `infra/terraform/environments/<ambiente>/{web,platform}` | De dónde salen la URL, el bucket, la distribución, el ETag de `config.json`, el `invoke_url`, el clúster, el grupo de logs y el pool (`terraform output -raw`, nunca `-json`) |
| `PORTAL_URL`, `BUCKET`, `DISTRIBUTION_ID`, `CONFIG_ETAG`, `INVOKE_URL`, `LOG_GROUP`, `CLUSTER`, `EXPECTED_USER_POOL_ID`, `EXPECTED_AUDIT_URL` | de las salidas | Para correrlo sin Terraform |
| `SEED_USER_PASSWORD` | `SOLVENTA_IDENTITY_SEED_PASSWORD` | Contraseña de los usuarios permanentes; la necesitan `api`, `journey` y `audit` |
| `INSURED_USER`, `ADVISOR_USER`, `OPERATIONS_USER` | `asegurado@`, `asesor@`, `operaciones@example.com` | Usuarios permanentes de `bootstrap/identity` |
| `RUN_ID` | `r<segundos>` | Sufijo de las ofertas de demostración y de los clientes |

El ID del cliente web de Cognito sale del propio `config.json`. El guion **nunca imprime** la contraseña, los tokens, la key del
socio ni los enlaces completos: los cuerpos con credenciales van por la entrada estándar, la cabecera `Authorization` por un archivo
temporal privado, y los tokens y enlaces se recortan. La key del socio no la lee nunca: la agrega CloudFront.

Sale con código 0 si todas las comprobaciones dan OK, 1 si alguna falla y 2 si falta un dato (fase desconocida, contraseña o URL).
