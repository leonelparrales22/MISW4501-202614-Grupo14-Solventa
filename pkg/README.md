# `pkg` — librerías compartidas

Módulo Go (`.../pkg`) que enlazan los servicios. Es lo **único** que un servicio puede importar fuera de su propio módulo.

**Estado:** `resilience` y `adapters/openfinance` implementados (HA-D01); `adapters/opendata` y `adapters/kyc` (HA-L01); el resto de carpetas sigue reservado.

| Paquete | Propósito |
|---|---|
| `adapters/{openfinance,opendata,kyc,payments,esignature,iot}` (hoy `openfinance`, `opendata` y `kyc`: `GET /profiles/{id}`, corte duro de 700 ms y un circuit breaker por proveedor; errores `ErrSourceUnavailable` / `ErrInvalidCustomerID`; `Config.HTTPClient` opcional para compartir un pool de conexiones por host) | Capa de adaptadores (*library* en proceso): aísla el modelo interno del formato de cada proveedor externo. Incluye circuit breaker y degradación (≤ 120 ms por dependencia, corte duro 700 ms). El presupuesto de 120 ms lo pone el llamador con su contexto (abanico de HA-L01) |
| `resilience` | Circuit breaker genérico sobre `sony/gobreaker` (`DefaultConfig`: 3 fallas, 5 s, 3 éxitos). **HA-L01:** opción `IgnoreCallerCancellation` + `ExecuteContext`: la llamada que falla porque el contexto del llamador terminó (presupuesto por dependencia) no cuenta como falla; `DefaultConfig` no cambia. Timeouts y reintentos con backoff llegarán con HA-D02 |
| `events` | Contratos de eventos y publicación/consumo sobre SNS/SQS |
| `outbox` | *Transactional outbox*: persiste el evento junto con la operación de negocio |
| `idempotency` | Claves de idempotencia y consumidor idempotente |
| `observability` | OpenTelemetry (CloudWatch + X-Ray), logs estructurados con identificador de correlación |
| `httpx` | Utilidades HTTP sobre `chi` (middleware, respuestas de error) |
| `config` | Configuración por variables de entorno |
| `security` | Validación de JWT (Cognito) y propagación de claims |
| `health` | Handler de `GET /health` consumido por los health checks del ALB |

Regla: `pkg/` no importa nada de `services/`.
