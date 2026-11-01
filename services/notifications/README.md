# Servicio `notifications` (notificaciones)

Envía notificaciones push al asegurado: pago paramétrico automático, vencimientos/renovaciones y cambios de estado del siniestro.

**Estado:** `GET /health`. **HU-M3.1-01:** el centro de notificaciones del asegurado y el *push* del pago paramétrico automático. Consume
`ParametricPayoutCompleted` de su cola SQS (`EVENTS_QUEUE_URL`), registra la notificación en **PostgreSQL** aunque el *push* nunca llegue,
la envía a cada dispositivo del asegurado que dio el permiso con reintentos idempotentes (`pkg/idempotency`, ventana de 24 h) y expone el
centro (`GET /v1/notifications`, `GET /v1/notifications/{id}`, lectura) y el registro del dispositivo (`PUT /v1/devices/{id}`). **El
proveedor de *push* es el simulador `stubs/push`** (decisión del equipo para esta historia): Firebase Cloud Messaging queda para cuando se
cree el proyecto de Firebase, y entra cambiando solo el adaptador. Contrato para el móvil:
[`docs/contratos/HU-M3.1-01-api.md`](../../docs/contratos/HU-M3.1-01-api.md).

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `domain` | **HU-M3.1-01:** `ParametricPayout` (el pago con su disparador —parámetro, evento, umbral, valor observado— y el medio de pago sin número), `Notification` (una por pago, `ntf-<payout_id>`; nunca se sobrescribe), `Device` (instalación de la app: token, idioma y si dio el permiso) y `Delivery` (el *push* de una notificación a un dispositivo, con el estado de `pkg/idempotency`) |
| `application` | **HU-M3.1-01:** `Service` registra la notificación de un pago y sus entregas (idempotente por pago; sin dispositivo con permiso, la notificación queda en el centro), lista y lee el centro del que llama, marca leídas y registra dispositivos (sin permiso no guarda el token) |
| `push` | Reglas de envío *push* (destinatarios, preferencias, reintentos). **HU-M3.1-01:** `Worker` toma las entregas vencidas con *lease* y las envía con la misma clave en cada intento (`pkg/idempotency.Runner`): espera creciente de 1 s a 1 min, rechazo definitivo si el proveedor no conoce el dispositivo y abandono a las 24 h. Se despierta en cuanto se registra un pago (`Wake`) y registra la latencia desde la confirmación del pago (`push delivered … latency_ms`) |
| `templates` | Plantillas de mensajes por tipo de evento e idioma. **HU-M3.1-01:** el título y el cuerpo del *push* del pago paramétrico en español e inglés, con el monto, la moneda y el evento («Pago automático recibido: $ 180.000 COP» / «Tu vuelo AV-8421 se retrasó 215 min…») |

## Adaptadores (`internal/adapters`)

| Adaptador | Qué hace |
|---|---|
| `http` | Router `chi`: `GET /health` y, con `AUTH_ISSUER`, las rutas humanas del centro (rol `insured`, `Cache-Control: no-store`; la notificación de otro asegurado responde 404) |
| `messaging` | Consumidor de `ParametricPayoutCompleted` (`pkg/events`): traduce el *payload* al dominio; un *payload* inválido o una base caída es un error, así que el mensaje reaparece y termina en la DLQ |
| `postgres` | Base propia (`database/sql` + `pgx` v5), migraciones embebidas con `pg_advisory_lock`. `Store`: tablas `notification`, `device` y `push_delivery`; la notificación y sus entregas se escriben en una transacción y el protocolo de reclamo de `pkg/idempotency` es una sentencia condicional por paso |
| `pushprovider` | Puente entre `pkg/adapters/push` (corte de 2 s, circuit breaker, clasificación rechazado/ambiguo/no enviado) y el puerto `PushSender`: escribe el *push* en el idioma del dispositivo, lo agrupa en `parametric_payouts` y le pone en `data` qué abrir (`notification_id`, `kind`, `claim_id`) |
| `fcm` | Reservado para el cliente de Firebase Cloud Messaging |

## Configuración (variables de entorno)

| Variable | Por omisión | Qué hace |
|---|---|---|
| `PORT` | `8080` | Puerto HTTP |
| `DATABASE_URL` | — | PostgreSQL propio (secreto). Sin ella solo se sirve `/health` |
| `AUTH_ISSUER`, `AUTH_JWKS_URL`, `AUTH_CLIENT_IDS` | — | Verificación de los ID tokens de Cognito (`pkg/security`). Sin `AUTH_ISSUER` no se montan las rutas del centro |
| `EVENTS_QUEUE_URL` | — | Cola SQS del servicio, suscrita al tópico `claims`. Sin ella no se consume ningún pago |
| `PUSH_PROVIDER_URL` | — | Proveedor de *push* (`stubs/push`, puerto 9070). Sin él no se envía ningún *push*: las notificaciones esperan en el centro |
| `PUSH_PROVIDER_TIMEOUT_MS` | `2000` | Corte duro de cada envío |
| `PUSH_POLL_INTERVAL_MS` | `1000` | Cada cuánto el trabajador busca entregas vencidas (un pago nuevo lo despierta enseguida) |
| `PUSH_RETRY_BASE_MS`, `PUSH_RETRY_MAX_MS` | `1000`, `60000` | Espera tras el primer intento fallido (se duplica) y su tope |

## Datos y eventos

- **BD propia:** PostgreSQL (notificaciones): `notification`, `device` y `push_delivery` (migración `0001`).
- **Consume:** `ParametricPayoutCompleted` de `claims` (HU-M3.1-01) mediante una cola SQS propia (con DLQ), con un consumidor idempotente
  por pago. Ver [catálogo de eventos](../../docs/events/README.md). Los demás tipos de evento se borran sin efecto.
- **Salida:** HTTPS hacia el proveedor de *push* (asíncrona); hoy `stubs/push`.

## Lo que queda fuera de HU-M3.1-01

- **El motor paramétrico:** `claims/internal/parametric` sigue reservado; el evento lo publica hoy el ensayo local
  ([`tests/e2e/hu-m3.1`](../../tests/e2e/hu-m3.1/README.md)) en lugar de `claims`.
- **Firebase Cloud Messaging:** sin proyecto de Firebase, el *push* llega al simulador y no al teléfono; la app consulta el centro (ver la
  guía del contrato).
- **Infraestructura:** la cola `notifications` en `infra/terraform` y la tarea de ECS del servicio.

## Historias asociadas

Épica móvil 3 (F3.1–F3.3). Implementada: HU-M3.1-01.

## Desarrollo

```
go run ./services/notifications/cmd/notifications
```
