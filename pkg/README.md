# `pkg` — librerías compartidas

Módulo Go (`.../pkg`) que enlazan los servicios. Es lo **único** que un servicio puede importar fuera de su propio módulo.

**Estado:** `resilience` y `adapters/openfinance` implementados (HA-D01); `adapters/opendata` y `adapters/kyc` (HA-L01); el resto de carpetas sigue reservado.

| Paquete | Propósito |
|---|---|
| `adapters/{openfinance,opendata,kyc,payments,esignature,iot}` (hoy `openfinance`, `opendata` y `kyc`: `GET /profiles/{id}`, corte duro de 700 ms y un circuit breaker por proveedor; errores `ErrSourceUnavailable` / `ErrInvalidCustomerID`; `Config.HTTPClient` opcional para compartir un pool de conexiones por host) | Capa de adaptadores (*library* en proceso): aísla el modelo interno del formato de cada proveedor externo. Incluye circuit breaker y degradación (≤ 120 ms por dependencia, corte duro 700 ms). El presupuesto de 120 ms lo pone el llamador con su contexto (abanico de HA-L01) |
| `resilience` | Circuit breaker genérico sobre `sony/gobreaker` (`DefaultConfig`: 3 fallas consecutivas, 5 s, 3 éxitos). Toda llamada que termina en error cuenta como falla, incluida la que el llamador corta por su presupuesto de latencia. **HA-L01 (decisión D-3 c):** modo opcional por proporción (`FailureRatio`, `MinRequests`, `Window`): el circuito abre cuando, dentro de una ventana, hubo al menos `MinRequests` llamadas y las fallidas llegan a `FailureRatio`; así una dependencia lenta o caída de forma persistente queda aislada y la cola normal de latencia no abre el circuito. En ese modo, `ConsecutiveFailureLimit` es una red de seguridad para tráfico bajo: el circuito también abre tras esa cantidad de fallas seguidas, contadas por el propio `Breaker` (no se reinician al terminar cada ventana, solo con un éxito o un cambio de estado), aunque ninguna ventana llegue a `MinRequests` (origination usa 80 %, 50 llamadas, 5 s o 30 fallas seguidas). Half-open igual en los dos modos: una sonda fallida reabre y `HalfOpenSuccesses` éxitos seguidos cierran. Sin `FailureRatio`, `DefaultConfig` no cambia. Timeouts y reintentos con backoff llegarán con HA-D02 |
| `events` | Contratos de eventos y publicación/consumo sobre SNS/SQS |
| `outbox` | *Transactional outbox*: persiste el evento junto con la operación de negocio |
| `idempotency` | Claves de idempotencia y consumidor idempotente |
| `observability` | OpenTelemetry (CloudWatch + X-Ray), logs estructurados con identificador de correlación |
| `httpx` | Utilidades HTTP sobre `chi` (middleware, respuestas de error) |
| `config` | Configuración por variables de entorno |
| `security` | Validación de JWT (Cognito) y propagación de claims |
| `health` | Handler de `GET /health` consumido por los health checks del ALB |

Regla: `pkg/` no importa nada de `services/`.
