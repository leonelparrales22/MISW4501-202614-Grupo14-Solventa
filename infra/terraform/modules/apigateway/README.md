# Módulo `apigateway` — API Gateway REST con caché y rate limiting por socio (HA-L01)

API REST regional delante del servicio `origination` para el recorrido de cotización embebida.

| Ruta | Método | Caché | Para qué |
|---|---|---|---|
| `/v1/risk-profiles/resolve` | `POST` | no (la clave de caché no puede incluir el cuerpo) | Recorrido de cotización |
| `/v1/risk-profiles/{customer_id}?consent_active=` | `GET` | sí, TTL `cache_ttl_seconds` (60 s, decisión D-6) | Misma respuesta, servida sin recalcular (CA-4) |

- **Aislamiento por socio (CA-4):** la clave de caché del `GET` es `customer_id` (ruta) + `consent_active` (query) + el
  encabezado `x-api-key`. Dos socios que piden el mismo cliente nunca comparten la entrada.
- **Rate limiting (CA-5):** cada socio de `partners` tiene su API key y su *usage plan* con `rate_limit` / `burst_limit`
  (decisión D-7: 100 req/s, ráfaga 200). Todas las rutas exigen API key; un socio por encima de su límite recibe `429` sin
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

Salidas: `rest_api_id`, `stage_name`, `invoke_url`, `usage_plan_ids`, `api_key_ids` y `api_key_values` (sensible; claves de
prueba para los guiones k6 de la ventana).

Validación local: `terraform -chdir=infra/terraform/modules/apigateway init -backend=false` y `terraform … validate`.
