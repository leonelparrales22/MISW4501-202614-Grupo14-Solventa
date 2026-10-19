# `pkg` — librerías compartidas

Módulo Go (`.../pkg`) que enlazan los servicios. Es lo **único** que un servicio puede importar fuera de su propio módulo.

**Estado:** `resilience` y `adapters/openfinance` implementados (HA-D01); `adapters/opendata` y `adapters/kyc` (HA-L01); `security` (HA-S01); `observability` (T6); `idempotency`, `adapters/payments` y la política de reintentos de `resilience` (HA-D02); el resto de carpetas sigue reservado.

| Paquete | Propósito |
|---|---|
| `adapters/{openfinance,opendata,kyc,payments,esignature,iot}` (hoy `openfinance`, `opendata` y `kyc`: `GET /profiles/{id}`, corte duro de 700 ms y un circuit breaker por proveedor; errores `ErrSourceUnavailable` / `ErrInvalidCustomerID`; `Config.HTTPClient` opcional para compartir un pool de conexiones por host. **`payments` (HA-D02):** `Client.Charge` hace `POST /charges` con la cabecera `Idempotency-Key`, corte duro de 700 ms y circuit breaker, y clasifica la respuesta: 2xx éxito; 402/422 y demás 4xx rechazo final (`RejectedError` con `decline_code`, no cuenta como falla del circuito); timeout, conexión rechazada o cortada, 5xx y 408/409/425/429 ambiguo (`ErrAmbiguous`: reintentar con la misma clave); circuito abierto `ErrUnavailable` (no se envió). Los errores envuelven los de `idempotency`) | Capa de adaptadores (*library* en proceso): aísla el modelo interno del formato de cada proveedor externo. Incluye circuit breaker y degradación (≤ 120 ms por dependencia, corte duro 700 ms). El presupuesto de 120 ms lo pone el llamador con su contexto (abanico de HA-L01) |
| `resilience` | Circuit breaker genérico sobre `sony/gobreaker` (`DefaultConfig`: 3 fallas consecutivas, 5 s, 3 éxitos). Toda llamada que termina en error cuenta como falla, incluida la que el llamador corta por su presupuesto de latencia. **HA-L01 (decisión D-3 c):** modo opcional por proporción (`FailureRatio`, `MinRequests`, `Window`): el circuito abre cuando, dentro de una ventana, hubo al menos `MinRequests` llamadas y las fallidas llegan a `FailureRatio`; así una dependencia lenta o caída de forma persistente queda aislada y la cola normal de latencia no abre el circuito. En ese modo, `ConsecutiveFailureLimit` es una red de seguridad para tráfico bajo: el circuito también abre tras esa cantidad de fallas seguidas, contadas por el propio `Breaker` (no se reinician al terminar cada ventana, solo con un éxito o un cambio de estado), aunque ninguna ventana llegue a `MinRequests` (origination usa 80 %, 50 llamadas, 5 s o 30 fallas seguidas). Half-open igual en los dos modos: una sonda fallida reabre y `HalfOpenSuccesses` éxitos seguidos cierran. Sin `FailureRatio`, `DefaultConfig` no cambia. **HA-D02:** `RetryPolicy{Base, Max, Window}` (espera creciente ×2 con tope y ventana opcional; `Backoff`, `WindowEnd` y `Next`, que ajusta el último intento al fin de la ventana e informa cuándo se agotó); `DefaultRetryPolicy` son los valores de EC-D02: 500 ms, tope 60 s, ventana de 24 h |
| `events` | Contratos de eventos y publicación/consumo sobre SNS/SQS |
| `outbox` | *Transactional outbox*: persiste el evento junto con la operación de negocio |
| `idempotency` | Claves de idempotencia y consumidor idempotente. **HA-D02 (ADR-01, EC-D02):** `Key(prefix, id)` (clave determinística, nunca aleatoria); `Store[T]` que cada servicio implementa sobre **su propia** base de datos (`Claim` debe ser una sola sentencia condicional con *lease*; `Confirm`, `Reschedule` y `Fail` solo si el reclamo sigue siendo el mismo, si no `ErrClaimLost`); `Runner[T].Process` reclama, llama al proveedor solo si ganó y clasifica: éxito → confirmado; `ErrRejected` → fallido; `ErrAmbiguous`/`ErrNotSent` → reprogramado con la misma clave y espera creciente hasta vencer la ventana (`ReasonWindowExpired`, nunca limbo). Sin base de datos ni HTTP. La usa el cobro de la prima de origination |
| `observability` | **Implementado.** `Setup` (logger `slog` JSON + proveedores OpenTelemetry con exportación OTLP/HTTP solo si `OTEL_EXPORTER_OTLP_ENDPOINT` está definido), `Middleware` (id de correlación `X-Correlation-Id`, span de servidor, métricas de peticiones y access log) y los atajos `Tracer`/`Meter`. El colector (ADOT) lleva los datos a CloudWatch y X-Ray |
| `httpx` | Utilidades HTTP sobre `chi` (middleware, respuestas de error) |
| `config` | Configuración por variables de entorno |
| `security` | Validación de JWT (Cognito) y propagación de claims. **HA-S01:** `JWKSVerifier` (RS256, `kid`, `iss`, `aud`/`client_id`, `token_use` y vigencia; llaves del user pool descargadas una vez al arrancar, sin red por petición), middleware `Authenticate` (401) y `RequireRole` (403) con cuerpo `{"error","message"}`, roles `insured`/`advisor`/`operations`/`partner` (grupos de Cognito), `Claims` en el contexto (`WithClaims`/`ClaimsFromContext`) y ayudas para casos de uso `RequireAnyRole`, `RequireOwnerOrRole` y `RequireSamePartner` (solo biblioteca estándar). Configuración `AUTH_*` y contrato en [docs/api](../docs/api/README.md#autenticación-y-autorización-ha-s01) |
| `health` | Handler de `GET /health` consumido por los health checks del ALB |

Regla: `pkg/` no importa nada de `services/`.

## Cómo reutilizar el cobro idempotente en `claims` (HA-D02 CA-6, HU-W2.2-03)

El pago de una indemnización usa el mismo mecanismo que el cobro de la prima, sin copiar código ni importar `origination`:

1. Clave determinística por operación: `idempotency.Key("indemnizacion", claimID)`.
2. Una tabla propia en la base de `claims` con una fila por pago (estado, intentos, `next_attempt_at`, `claimed_until`,
   `window_ends_at`) y un `idempotency.Store[T]` sobre ella: el `Claim` es un `UPDATE … WHERE (status='pending' AND
   next_attempt_at<=$now) OR (status='in_progress' AND claimed_until<$now) RETURNING …` y cada escritura posterior exige el mismo
   `claimed_until` (modelo: `services/origination/internal/adapters/postgres/charges.go`).
3. Un `idempotency.Runner` con `resilience.DefaultRetryPolicy()` (o la ventana que fije la historia) y un *lease* mayor que el
   timeout de la llamada; el ejecutor llama al proveedor con la clave y devuelve los errores clasificados (`ErrRejected`,
   `ErrAmbiguous`, `ErrNotSent`), como hace `adapters/payments`.
4. Un trabajador que recorre los pagos vencidos y llama a `Runner.Process`; el ejemplo `ExampleRunner_disbursement` de
   `idempotency` muestra un desembolso con la pasarela caída y luego disponible.

La garantía ante una respuesta ambigua depende de que el proveedor respete la clave (decisión D-10).
