# API

Cada servicio mantiene su contrato en `services/<service>/api/openapi.yaml`; Schemathesis valida las peticiones y respuestas contra él en CI.

| Servicio | Contrato | Estado |
|---|---|---|
| origination | [openapi.yaml](../../services/origination/api/openapi.yaml) | 0.3.0: `/health`, `POST /v1/risk-profiles/resolve` (HA-D01) y `GET /v1/risk-profiles/{customer_id}` (HA-L01); la respuesta agrega `partial` y `sources[]` (estado y latencia por fuente) |
| claims | [openapi.yaml](../../services/claims/api/openapi.yaml) | Solo `/health` |
| partners | [openapi.yaml](../../services/partners/api/openapi.yaml) | Solo `/health` |
| audit | [openapi.yaml](../../services/audit/api/openapi.yaml) | Solo `/health` |
| notifications | [openapi.yaml](../../services/notifications/api/openapi.yaml) | Solo `/health` |

Aquí se documentará además: reglas de versionado (la web, el móvil y los socios no deben romperse a la vez), autenticación (JWT de Cognito para clientes; API keys y usage plans para socios) y ejemplos de uso.
