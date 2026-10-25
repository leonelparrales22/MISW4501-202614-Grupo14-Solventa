# `pkg` — librerías compartidas

Módulo Go (`.../pkg`) que enlazan los servicios. Es lo **único** que un servicio puede importar fuera de su propio módulo.

**Estado:** `resilience` y `adapters/openfinance` implementados (HA-D01); `adapters/opendata` y `adapters/kyc` (HA-L01); `security` (HA-S01); `observability` (T6); `idempotency`, `adapters/payments` y la política de reintentos de `resilience` (HA-D02); `events` y `outbox` (HU-M1.3-01); `adapters/policies` (HU-W2.1-01); el resto de carpetas sigue reservado.

| Paquete | Propósito |
|---|---|
| `adapters/{openfinance,opendata,kyc,payments,esignature,iot}` (hoy `openfinance`, `opendata` y `kyc`: `GET /profiles/{id}`, corte duro de 700 ms y un circuit breaker por proveedor; errores `ErrSourceUnavailable` / `ErrInvalidCustomerID`; `Config.HTTPClient` opcional para compartir un pool de conexiones por host. **`payments` (HA-D02):** `Client.Charge` hace `POST /charges` con la cabecera `Idempotency-Key`, corte duro de 700 ms y circuit breaker, y clasifica la respuesta: 2xx éxito; 402/422 y demás 4xx rechazo final (`RejectedError` con `decline_code`, no cuenta como falla del circuito); timeout, conexión rechazada o cortada, 5xx y 408/409/425/429 ambiguo (`ErrAmbiguous`: reintentar con la misma clave); circuito abierto `ErrUnavailable` (no se envió). Los errores envuelven los de `idempotency`. **`openfinance` (HU-M1.3-02):** `Client.RevokeConsent` hace `POST /consent-revocations` con la cabecera `Idempotency-Key` (el consentimiento, la entidad, el cliente del aliado y la hora efectiva; nunca documento ni nombre), corte duro de 700 ms y un circuit breaker propio de las revocaciones, separado del de los perfiles; clasifica igual que `payments`: 2xx confirmado, 4xx final `ErrRevocationRejected`, timeout, conexión cortada, 5xx y 408/409/425/429 `ErrRevocationAmbiguous`, circuito abierto `ErrRevocationNotSent`, envolviendo los de `idempotency`) | Capa de adaptadores (*library* en proceso): aísla el modelo interno del formato de cada proveedor externo. Incluye circuit breaker y degradación (≤ 120 ms por dependencia, corte duro 700 ms). El presupuesto de 120 ms lo pone el llamador con su contexto (abanico de HA-L01) |
| `adapters/policies` | **HU-W2.1-01:** cliente del core de pólizas (el sistema de registro de las pólizas de Autos, Hogar y Viaje). `Client.HolderPolicies` hace `GET /holders/{id}/policies` con corte duro de 700 ms y circuit breaker `policy-core`; un titular desconocido (`404`) no tiene pólizas; cualquier otra falla es `ErrSourceUnavailable` y un titular vacío `ErrInvalidHolder` (no cuenta para el circuito). En local y en pruebas lo atiende `stubs/policy-core` |
| `resilience` | Circuit breaker genérico sobre `sony/gobreaker` (`DefaultConfig`: 3 fallas consecutivas, 5 s, 3 éxitos). Toda llamada que termina en error cuenta como falla, incluida la que el llamador corta por su presupuesto de latencia. **HA-L01 (decisión D-3 c):** modo opcional por proporción (`FailureRatio`, `MinRequests`, `Window`): el circuito abre cuando, dentro de una ventana, hubo al menos `MinRequests` llamadas y las fallidas llegan a `FailureRatio`; así una dependencia lenta o caída de forma persistente queda aislada y la cola normal de latencia no abre el circuito. En ese modo, `ConsecutiveFailureLimit` es una red de seguridad para tráfico bajo: el circuito también abre tras esa cantidad de fallas seguidas, contadas por el propio `Breaker` (no se reinician al terminar cada ventana, solo con un éxito o un cambio de estado), aunque ninguna ventana llegue a `MinRequests` (origination usa 80 %, 50 llamadas, 5 s o 30 fallas seguidas). Half-open igual en los dos modos: una sonda fallida reabre y `HalfOpenSuccesses` éxitos seguidos cierran. Sin `FailureRatio`, `DefaultConfig` no cambia. **HA-D02:** `RetryPolicy{Base, Max, Window}` (espera creciente ×2 con tope y ventana opcional; `Backoff`, `WindowEnd` y `Next`, que ajusta el último intento al fin de la ventana e informa cuándo se agotó); `DefaultRetryPolicy` son los valores de EC-D02: 500 ms, tope 60 s, ventana de 24 h |
| `events` | Contratos de eventos y publicación/consumo sobre SNS/SQS. **HU-M1.3-01:** `Envelope` (`event_id`, `event_type`, `event_version`, `source`, `occurred_at` en UTC, `correlation_id` opcional y `payload` objeto; `New`, `Validate`, `Encode`, `Decode` y `DecodePayload` con lectura tolerante a campos nuevos), `Publisher`, `Handler` y `Router` por tipo (`ErrUnhandledType`). `snspublisher`: publica el sobre en un tópico con el atributo `event_type`. `sqsconsumer`: sondeo largo; borra lo procesado y los tipos sin manejador, deja en la cola lo que falla (llega a la DLQ por la política de la cola) y espera creciente (1 s, tope 30 s) si `ReceiveMessage` falla; nunca registra el *payload* |
| `outbox` | *Transactional outbox*: persiste el evento junto con la operación de negocio. **HU-M1.3-01:** `Store` que cada servicio implementa sobre **su** base (`Claim` atómico con *lease*; `MarkPublished` y `Reschedule` solo con el mismo reclamo, si no `ErrClaimLost`) y `Relay` (lotes de 10, *lease* 30 s, corte de 5 s por publicación, reintento 500 ms ×2 hasta 60 s **sin ventana**: un evento nunca se descarta; un sobre inválido se reprograma sin bloquear a los demás). Ejemplo ejecutable `ExampleRelay`. Lo usa originación (`outbox_event`) |
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

## Cómo publicar un evento con *outbox* (HU-M1.3-01)

El primer caso es `CustomerLinked` de originación ([catálogo](../docs/events/README.md#customerlinked-hu-m13-01)); cualquier servicio
publica igual, sin importar a otro:

1. **Contrato:** el evento en `docs/events/README.md` (publicador, consumidores, *payload* mínimo) y un tipo en el dominio del servicio
   que arme su `events.Envelope` con un `event_id` **determinista** por hecho (p. ej. `customer-linked-<customer_id>`).
2. **Tabla propia:** una tabla `outbox_event` en la base del servicio (modelo: `services/origination/migrations/0008_outbox.sql`) con
   el sobre completo, intentos, `next_attempt_at`, `claimed_until` y `published_at`, y un índice parcial de lo pendiente.
3. **Misma transacción:** la operación de negocio escribe la fila con `INSERT … ON CONFLICT (event_id) DO NOTHING` en su transacción
   (modelo: `CustomerStore.Link`); nunca se publica dentro de ella.
4. **`outbox.Store`** sobre esa tabla (modelo: `services/origination/internal/adapters/postgres/outbox.go`: `Claim` = `UPDATE … WHERE
   event_id IN (SELECT … FOR UPDATE SKIP LOCKED) RETURNING …`; las escrituras posteriores exigen el mismo `claimed_until`).
5. **Relevo:** en `cmd/<servicio>/main.go`, con el tópico del dominio (`EVENTS_TOPIC_ARN`, que inyecta `modules/platform`), un
   `snspublisher` y un `outbox.Relay` en segundo plano (modelo: `services/origination/cmd/origination/events.go`). El rol de la tarea ya
   tiene `sns:Publish` sobre el tópico de su dominio (T4).
6. **Consumidor:** en el servicio que lo recibe, un `events.Router` con un manejador **idempotente** por la clave del hecho y un
   `sqsconsumer` sobre su cola (`EVENTS_QUEUE_URL`; modelo: `services/audit/internal/adapters/messaging`).

**HU-W4.1-01** puede publicar `OfferGenerated` y `OfferRequoted` escribiéndolos en la **misma** tabla `outbox_event` de originación: el
relevo que ya corre los publica en el tópico `origination` sin más cambios.
