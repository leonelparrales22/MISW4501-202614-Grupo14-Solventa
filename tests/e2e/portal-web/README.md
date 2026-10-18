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
| W-4 `partner` | `POST /v1/risk-profiles/resolve` por CloudFront sin key → 200 (la agrega CloudFront), y con una key inventada → 200 (CloudFront la pisa); directo al gateway sin key → `403 {"message":"Forbidden"}` |
| W-5 `tokens` | `POST /pasarela/tokens` con `4111…` → 201 `tok_…`; número inválido → 400; `/pasarela/charges` y `/pasarela/debug/charges` → 403 |
| W-6 `journey` | Oferta `demo-active-<run>` → tarjeta tokenizada por el portal → cobro confirmado → emisión completa → enlace del correo en el portal → PDF por CloudFront con su SHA-256; tarjeta `…0002` → `payment_rejected` |
| W-7 `links` | En la definición de tarea de originación, `OFFER_LINK_BASE_URL` y `DOCUMENTS_PUBLIC_BASE_URL` son el portal |
| W-8 `publish` | `index.html` con `no-cache`, un *bundle* `immutable`, `config.json` con el mismo ETag que escribió Terraform y la última invalidación `Completed` |
| W-9 `audit` | Solo con `SMOKE_AUDIT=1`, cuando las rutas de `audit` del aliado estén en el gateway: términos y consentimiento por CloudFront y `POST /v1/offers` → oferta `off-…` |

## Uso

```bash
# Desde la raíz del repo, con AWS_PROFILE=solventa y las composiciones inicializadas (lo hace up.sh).
SEED_USER_PASSWORD="$SOLVENTA_IDENTITY_SEED_PASSWORD" bash tests/e2e/portal-web/smoke.sh            # todas (sin W-9)
SEED_USER_PASSWORD="$SOLVENTA_IDENTITY_SEED_PASSWORD" bash tests/e2e/portal-web/smoke.sh static api
SMOKE_AUDIT=1 SEED_USER_PASSWORD="$SOLVENTA_IDENTITY_SEED_PASSWORD" bash tests/e2e/portal-web/smoke.sh
bash tests/e2e/portal-web/smoke.sh --help
```

| Variable | Defecto | Para qué |
|---|---|---|
| `ENVIRONMENT` | `staging` | `staging` o `production-demo`: elige las composiciones por defecto |
| `WEB_TF_DIR` / `PLATFORM_TF_DIR` | `infra/terraform/environments/<ambiente>/{web,platform}` | De dónde salen la URL, el bucket, la distribución, el ETag de `config.json`, el `invoke_url`, el clúster, el grupo de logs y el pool (`terraform output -raw`, nunca `-json`) |
| `PORTAL_URL`, `BUCKET`, `DISTRIBUTION_ID`, `CONFIG_ETAG`, `INVOKE_URL`, `LOG_GROUP`, `CLUSTER`, `EXPECTED_USER_POOL_ID` | de las salidas | Para correrlo sin Terraform |
| `SEED_USER_PASSWORD` | `SOLVENTA_IDENTITY_SEED_PASSWORD` | Contraseña de los usuarios permanentes; la necesitan `api` y `journey` |
| `INSURED_USER`, `ADVISOR_USER`, `OPERATIONS_USER` | `asegurado@`, `asesor@`, `operaciones@example.com` | Usuarios permanentes de `bootstrap/identity` |
| `SMOKE_AUDIT` | `0` | `1` agrega W-9 |
| `RUN_ID` | `r<segundos>` | Sufijo de las ofertas de demostración y de los clientes |

El ID del cliente web de Cognito sale del propio `config.json`. El guion **nunca imprime** la contraseña, los tokens, la key del
socio ni los enlaces completos: los cuerpos con credenciales van por la entrada estándar, la cabecera `Authorization` por un archivo
temporal privado, y los tokens y enlaces se recortan. La key del socio no la lee nunca: la agrega CloudFront.

Sale con código 0 si todas las comprobaciones dan OK, 1 si alguna falla y 2 si falta un dato (fase desconocida, contraseña o URL).
