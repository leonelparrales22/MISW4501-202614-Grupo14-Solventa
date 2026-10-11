# Módulo `apigateway` — API Gateway REST con caché y rate limiting por socio (HA-L01)

API REST regional delante del servicio `origination` para el recorrido de cotización embebida.

| Ruta | Método | Caché | Para qué |
|---|---|---|---|
| `/v1/risk-profiles/resolve` | `POST` | no (la clave de caché no puede incluir el cuerpo) | Recorrido de cotización |
| `/v1/risk-profiles/{customer_id}?consent_active=` | `GET` | sí, TTL `cache_ttl_seconds` (60 s, decisión D-6) | Misma respuesta, servida sin recalcular (CA-4) |

- **Aislamiento por socio (CA-4):** la clave de caché del `GET` es `customer_id` (ruta) + `consent_active` (query) + el
  encabezado `x-api-key`. Dos socios que piden el mismo cliente nunca comparten la entrada.
- **Rate limiting (CA-5):** cada socio de `partners` tiene su API key y su *usage plan* con `rate_limit` / `burst_limit`
  (decisión D-7: 100 req/s, ráfaga 200). Todas las rutas de cotización exigen API key; un socio por encima de su límite recibe `429` sin
  afectar a los demás.
- **Integración:** `HTTP_PROXY` con timeout de 5 s (ADR-02 / EXP-D03). Con `vpc_link_v2_id` e `integration_target_arn` es una
  integración privada a un ALB interno por **VPC Link V2** (soportado en REST desde el proveedor `hashicorp/aws` 6.x, atributo
  `integration_target`); `backend_base_url` fija el encabezado `Host` y la ruta. Sin VPC link, la integración es pública
  (`INTERNET`), útil solo para pruebas.
- La caché del stage se cobra por hora mientras exista (0,5 GB ≈ US$ 0,02/h): el módulo es para ventanas efímeras de staging
  y se destruye en la misma sesión (regla R12 del equipo de Nicolás, `infra/COSTOS.md` del harness).

## Uso

```hcl
module "apigateway" {
  source                 = "../../../modules/apigateway"
  name                   = "solventa-staging-ha-l01"
  backend_base_url       = "http://${aws_lb.internal.dns_name}"
  vpc_link_v2_id         = aws_apigatewayv2_vpc_link.this.id
  integration_target_arn = aws_lb.internal.arn
}
```

| Variable | Defecto | Descripción |
|---|---|---|
| `name` | — | Nombre del API |
| `stage_name` | `staging` | Stage |
| `backend_base_url` | — | URL base del servicio vista por la integración |
| `vpc_link_v2_id` / `integration_target_arn` | `null` | VPC Link V2 y ARN del ALB interno |
| `integration_timeout_ms` | `5000` | Timeout de integración (ADR-02) |
| `cache_cluster_size` | `"0.5"` | Tamaño de la caché del stage (GB) |
| `cache_ttl_seconds` | `60` | TTL del `GET` cacheado (≤ 300 s por la revocación de consentimiento, HU-W1.2-01 CA-5) |
| `partners` | `socio-a`, `socio-b` (100 req/s, ráfaga 200) | Socios de prueba con su API key y usage plan |
| `tags` | `{}` | Etiquetas adicionales |
| `cognito_user_pool_arns` | `[]` | User pools de los canales humanos (HA-S01); vacío = API solo de socios |
| `human_routes` | `{}` | Rutas humanas bajo `/v1` con sus métodos (HA-S01) |
| `cors_allow_origin` | `"*"` | Origen permitido en el *preflight* y en las respuestas 4XX del gateway (HA-S01) |

Salidas: `rest_api_id`, `stage_name`, `invoke_url`, `usage_plan_ids`, `api_key_ids`, `api_key_values` (sensible; claves de
prueba para los guiones k6 de la ventana) y `cognito_authorizer_id` (HA-S01; `null` sin user pool).

## Canales humanos: authorizer de Cognito (HA-S01)

Con `cognito_user_pool_arns` (salida `user_pool_arn` del módulo [`cognito`](../cognito/README.md)) el módulo agrega:

| Elemento | Configuración | Criterio |
|---|---|---|
| Authorizer | `COGNITO_USER_POOLS` sobre `method.request.header.Authorization`: valida firma y vigencia del JWT antes de llegar al servicio; sin token, con token inválido o vencido responde `401` | CA-1 |
| Rutas humanas (`human_routes`) | Cada clave es un segmento bajo `/v1` (p. ej. `quotes` → `/v1/quotes` y `/v1/quotes/{proxy+}`), con `authorization = "COGNITO_USER_POOLS"` y `api_key_required = false`, integradas por `HTTP_PROXY` (mismo VPC link y timeout) a la misma ruta del servicio. La cabecera `Authorization` llega al servicio, que vuelve a verificar el JWT con `pkg/security` y autoriza por rol y titularidad (D-1) | CA-1, CA-2 |
| Rutas de socio | Las de cotización siguen con `authorization = "NONE"` y `api_key_required = true`; nunca usan el authorizer. `risk-profiles` no puede declararse como ruta humana | CA-5 |
| *Preflight* CORS | `OPTIONS` sin autorización ni API key, respondido por el gateway (`MOCK`) con los métodos de la ruta y las cabeceras `Authorization,Content-Type` | — |
| Respuestas del gateway | `UNAUTHORIZED`, `ACCESS_DENIED`, `EXPIRED_TOKEN` y `DEFAULT_4XX` con cabeceras CORS y el cuerpo por defecto (`{"message": …}`), para que el navegador pueda leer el `401` y el cliente web lo trate como sesión expirada | — |

Una ruta humana sin `cognito_user_pool_arns` hace fallar el `plan` (*precondition*): nunca se publica una ruta humana sin
authorizer. Las respuestas `2XX`/`5XX` de las rutas `HTTP_PROXY` llevan las cabeceras CORS que ponga el servicio. Sin
`cognito_user_pool_arns` no se crea nada de esta sección y el *hash* de redespliegue no cambia.

```hcl
module "apigateway" {
  source                 = "../../../modules/apigateway"
  name                   = "solventa-staging"
  backend_base_url       = "http://${aws_lb.internal.dns_name}"
  vpc_link_v2_id         = aws_apigatewayv2_vpc_link.this.id
  integration_target_arn = aws_lb.internal.arn
  cognito_user_pool_arns = [module.cognito.user_pool_arn]
  human_routes           = { quotes = { methods = ["GET", "POST"] } } # ejemplo: la primera ruta humana llega con HU-W1.1-01
  cors_allow_origin      = "https://<origen del portal>"
}
```

## Validación local (sin AWS)

```bash
terraform -chdir=infra/terraform/modules/apigateway init -backend=false
terraform -chdir=infra/terraform/modules/apigateway validate
terraform -chdir=infra/terraform/modules/apigateway test
```

`tests/apigateway.tftest.hcl` (HA-S01) usa un proveedor simulado: afirma que sin user pool la API sigue siendo solo de socios,
que toda ruta humana usa el authorizer y ninguna exige API key, que las rutas de socio nunca usan el authorizer, el *preflight*
sin autorización y las respuestas 4XX con CORS.
