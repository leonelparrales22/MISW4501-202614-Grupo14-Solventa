# `pkg` — librerías compartidas

Módulo Go (`.../pkg`) que enlazan los servicios. Es lo **único** que un servicio puede importar fuera de su propio módulo.

**Estado:** `resilience` y `adapters/openfinance` implementados (HA-D01); `security` (HA-S01); el resto de carpetas sigue reservado.

| Paquete | Propósito |
|---|---|
| `adapters/{openfinance,opendata,kyc,payments,esignature,iot}` (hoy solo `openfinance`: `GET /profiles/{id}`, corte de 700 ms y circuit breaker; errores `ErrSourceUnavailable` / `ErrInvalidCustomerID`) | Capa de adaptadores (*library* en proceso): aísla el modelo interno del formato de cada proveedor externo. Incluye circuit breaker y degradación (≤ 120 ms por dependencia, corte duro 700 ms) |
| `resilience` | Circuit breaker genérico sobre `sony/gobreaker` (`DefaultConfig`: 3 fallas, 5 s, 3 éxitos). Timeouts y reintentos con backoff llegarán con HA-D02 |
| `events` | Contratos de eventos y publicación/consumo sobre SNS/SQS |
| `outbox` | *Transactional outbox*: persiste el evento junto con la operación de negocio |
| `idempotency` | Claves de idempotencia y consumidor idempotente |
| `observability` | OpenTelemetry (CloudWatch + X-Ray), logs estructurados con identificador de correlación |
| `httpx` | Utilidades HTTP sobre `chi` (middleware, respuestas de error) |
| `config` | Configuración por variables de entorno |
| `security` | Validación de JWT (Cognito) y propagación de claims. **HA-S01:** `JWKSVerifier` (RS256, `kid`, `iss`, `aud`/`client_id`, `token_use` y vigencia; llaves del user pool descargadas una vez al arrancar, sin red por petición), middleware `Authenticate` (401) y `RequireRole` (403) con cuerpo `{"error","message"}`, roles `insured`/`advisor`/`operations`/`partner` (grupos de Cognito), `Claims` en el contexto (`WithClaims`/`ClaimsFromContext`) y ayudas para casos de uso `RequireAnyRole`, `RequireOwnerOrRole` y `RequireSamePartner` (solo biblioteca estándar). Configuración `AUTH_*` y contrato en [docs/api](../docs/api/README.md#autenticación-y-autorización-ha-s01) |
| `health` | Handler de `GET /health` consumido por los health checks del ALB |

Regla: `pkg/` no importa nada de `services/`.
