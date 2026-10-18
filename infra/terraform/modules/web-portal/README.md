# Módulo `web-portal` — portal web en S3 privado + CloudFront, con el API en el mismo dominio (T5)

Publica el *build* de Angular del cliente web en un bucket S3 **privado** detrás de CloudFront y pone el API Gateway de la
plataforma en el **mismo dominio** (`/v1/*` y `/pasarela/*`), igual que el `proxy.conf.json` de desarrollo: el navegador nunca
hace peticiones de otro origen y el portal funciona sin CORS. Lo usan las composiciones
[`staging/web`](../../environments/staging/web/README.md) y [`production-demo/web`](../../environments/production-demo/web/README.md).

```
navegador ──HTTPS──▶ CloudFront  d<id>.cloudfront.net
                      ├─ partner_paths (/v1/risk-profiles/*, /v1/offers, /v1/audit/consent-terms/current, /v1/audit/consents)
                      │      └─▶ origen «api-socio»: API Gateway /<stage> + x-api-key del socio portal-embebido
                      ├─ /v1/*        ─▶ origen «api»: API Gateway /<stage> (Authorization del visor, sin Host)
                      ├─ /pasarela/*  ─▶ origen «api»: POST /pasarela/tokens público de la plataforma
                      └─ /* (resto)   ─▶ origen «sitio»: S3 privado con OAC + función de enlaces profundos
```

| Pieza | Configuración | Por qué |
|---|---|---|
| Bucket del sitio | Módulo [`s3`](../s3/README.md): sin acceso público, SSE-S3, solo TLS, versionado (las versiones anteriores expiran a los 7 días). Nombre `<name>-web-<cuenta>`. Política extra (`extra_policy_json`): solo `cloudfront.amazonaws.com` lee objetos, y solo para esta distribución (`AWS:SourceArn`) | Bucket privado (D-1). Sin `s3:ListBucket`, un archivo que no existe responde **403** |
| OAC | `sigv4`, `always`, tipo `s3` | CloudFront firma cada petición al bucket |
| Función de visor | `cloudfront-js-2.0` ([`functions/spa-rewrite.js`](functions/spa-rewrite.js)): si el último segmento de la ruta no tiene punto, la petición pasa a `/index.html`; los archivos siguen igual | Recargar `/embebido/oferta/<id>` o `/originacion/bandeja` devuelve la aplicación. **Sin `custom_error_response`**: si existiera, se tragaría los 401/403/404/5xx reales del API |
| Comportamientos del API | En orden: `partner_paths` → `api-socio`; `/v1/*` y `/pasarela/*` → `api`. Todos los métodos, `Managed-CachingDisabled`, `Managed-AllViewerExceptHostHeader`, `redirect-to-https` | Nunca se cachea una respuesta del API; con la caché desactivada `Authorization` llega al gateway, que sigue validando token, API key y límites (D-2) |
| Key del socio | El origen `api-socio` agrega `x-api-key` con la key de `portal-embebido`. CloudFront **pisa** el valor que mande el visor | Las pantallas públicas E1 y E2 llaman rutas de socio; el navegador nunca ve la key (el repo del front es público) y la misma ruta pedida directo al gateway sin key sigue en 403 (D-3) |
| Sitio | `Managed-CachingOptimized`, compresión, la función de visor | — |
| Todos | `Managed-SecurityHeadersPolicy` (HSTS, `nosniff`, `X-Frame-Options`, `Referrer-Policy`), `PriceClass_100`, HTTP/2 y 3, IPv6, certificado `*.cloudfront.net`, sin WAF ni registros | La opción más barata (D-15) |
| `config.json` | Objeto del bucket que escribe **Terraform** (solo con `api` y `cognito`), `application/json`, `no-store`: `apiGatewayUrl` y `pasarelaPagosUrl` = la URL del propio portal, y `cognito` (`region`, `userPoolId`, `clienteId`) | Configuración del front en tiempo de ejecución: un solo *build* sirve a todos los ambientes (D-5). Solo URL públicas e identificadores; nunca la key ni secretos |
| Parámetros SSM | `/solventa/<environment>/web/{bucket,distribution-id,url}` (`String` estándar, sin costo) | La plataforma lee la `url` para los enlaces de los correos (D-11) y CORS de production-demo (D-12 b); el *script* de publicación lee el bucket y la distribución |
| Rol del CD (opcional) | Con `cd`: rol que el workflow del front asume por OIDC (proveedor existente de la cuenta), con `aud` y `sub` exactos `<prefijo>:ref:refs/heads/<rama>` por cada rama. Política: listar el bucket, subir y borrar objetos, **`Deny` sobre `config.json`**, invalidar **esta** distribución y leer sus tres parámetros | Sin llaves guardadas en GitHub (D-8). `staging/web` lo usa; `production-demo/web` no (`cd = null`): la publicación desde `main` es del pipeline de `main` (#68), que puede activar esta entrada |

## Variables

| Variable | Defecto | Descripción |
|---|---|---|
| `name` | — | Prefijo (`solventa-stg`, `solventa-demo`) |
| `environment` | — | `staging` o `production-demo` (ruta de los parámetros SSM) |
| `force_destroy` | `false` | Borrar el bucket con sus objetos (solo ambientes efímeros) |
| `api` | `null` | `{ domain, stage }` del REST API; `null` deja solo el sitio |
| `partner_api_key` | `null` (sensible) | Key de `portal-embebido`; obligatoria con `api` (*precondition*) |
| `partner_paths` | las 4 rutas de socio de E1/E2 | Rutas que llevan la key |
| `cognito` | `null` | `{ region, user_pool_id, client_id }` para `config.json` |
| `cd` | `null` | `{ role_name, github_subject_prefix, branches }` del rol del workflow; sin comodines |
| `price_class` | `"PriceClass_100"` | Clase de precio |
| `tags` | `{}` | Etiquetas adicionales |

Salidas: `url`, `domain_name`, `distribution_id`, `bucket`, `config_etag` (ETag del `config.json` de Terraform, para el humo),
`ssm_parameters` y `cd_role_arn` (`null` sin `cd`).

## Costo

CloudFront no cobra por hora y tiene capa gratuita permanente (1 TB, 10 millones de peticiones y 2 millones de invocaciones de
funciones al mes; 1 000 rutas de invalidación gratis). S3 del sitio (unos MB), SSM estándar e IAM: centavos o cero. El sitio puede
quedar encendido sin costo apreciable (D-13).

## Pruebas (locales, sin AWS)

```bash
terraform -chdir=infra/terraform/modules/web-portal init -backend=false
terraform -chdir=infra/terraform/modules/web-portal test
node --test infra/terraform/modules/web-portal/functions/spa-rewrite.test.mjs
```

`tests/web-portal.tftest.hcl` (proveedor simulado) comprueba: el bucket solo lo lee esta distribución con OAC; el comportamiento por
defecto es el sitio con la función de visor; los del API nunca cachean y reenvían `Authorization`, en el orden correcto; solo las rutas
de socio llevan la key y esta es sensible; no hay páginas de error; `config.json` apunta al portal y no lleva secretos; sin `api` solo
existe el sitio; la key es obligatoria con `api`; el rol confía solo en sus ramas y no puede escribir `config.json`; y los parámetros
SSM se publican. El `id` de las políticas administradas de caché y de origen no se puede simular (en el esquema del proveedor es
opcional, no computado): las pruebas verifican los nombres buscados y el cableado, y el `plan` de solo lectura de las composiciones
muestra los ID reales. `spa-rewrite.test.mjs` (`node:test` con `vm`) comprueba que `/` y las rutas de la SPA van a `/index.html` y que
los archivos pasan sin cambios.
