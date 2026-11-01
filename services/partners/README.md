# Servicio `partners` (socios B2B)

Gestiona a los socios de distribución (seguro embebido): alta, credenciales, límites de consumo y desempeño de sus integraciones.

**Estado:** `GET /health` y, con `DATABASE_URL` y `AUTH_*`, **HU-W3.1-01** (generación de credenciales para socio nuevo): el portal de
operaciones (rol `operations`) lista los socios con su cuota y el consumo de hoy (`GET /v1/partners`), registra uno pendiente de
aprobación (`POST /v1/partners`), consulta su detalle (`GET /v1/partners/{partner_id}`), lo aprueba generando sus credenciales
(`POST …/approval`) y las rota con periodo de transición (`POST …/credential-rotations`). Las credenciales son API keys de Amazon API
Gateway en un usage plan propio por socio (cuota diaria y ráfaga), que crea el servicio con `GATEWAY_REST_API_ID`; cada emisión, rotación
y revocación llega a `audit` por el tópico `partners`. El módulo `dashboard` sigue reservado (HU-W3.2). Contrato:
[`docs/contratos/HU-W3.1-01-api.md`](../../docs/contratos/HU-W3.1-01-api.md).

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `onboarding` | Incorporación de nuevos socios (objetivo: integrar un socio en ≤ 1 semana sin tocar el núcleo). **HU-W3.1-01:** `onboarding.Service` registra el socio en `pending_approval` (validación de todos los campos a la vez, `code` único), lo aprueba (crea el usage plan y la primera credencial y, en una transacción, deja el socio `active`, la credencial, la entrada del historial y el evento `PartnerCredentialsIssued` en el *outbox*; si algo falla borra lo creado en el gateway), y lo muestra con el consumo de hoy leído del gateway (en paralelo, 2 s como máximo; sin dato, `usage = null`) y los indicadores del listado |
| `credentials` | Generación y rotación de credenciales/tokens y asignación de límites de consumo. **HU-W3.1-01:** `credentials.Issuer` genera el `client_id` (`sol_…`) y el secreto (32 bytes de `crypto/rand`, 43 caracteres base64url), crea la API key con ese valor y la asocia al usage plan; el secreto se devuelve una sola vez y solo se guardan sus 4 últimos caracteres. `credentials.Service.Rotate` emite una key nueva en el mismo plan y deja la anterior `retiring` hasta `expires_at` (`CREDENTIAL_GRACE_PERIOD_HOURS`, 1–168 h; una sola rotación en curso por socio). `credentials.Worker` revoca las vencidas: las quita del plan, las elimina y registra `credentials_revoked` con actor `system` y el evento `PartnerCredentialsRevoked` |
| `dashboard` | Volumen de cotizaciones, pólizas emitidas, conversión, disponibilidad y tiempos de respuesta por socio (reservado, HU-W3.2) |

El aislamiento y las cuotas por socio se aplican en Amazon API Gateway (*usage plans* y *API keys*, HA-01); este servicio administra su ciclo de vida.

## Adaptadores (`internal/adapters`)

| Adaptador | Qué hace |
|---|---|
| `http` | Router `chi`: `GET /health` y, con `WithPartners`, las cinco rutas de HU-W3.1-01 con `security.Authenticate` + `RequireRole(operations)` y `Cache-Control: no-store`. El analista se registra con su `sub` y, como etiqueta, el correo o el usuario del token. El detalle nunca trae el secreto; solo las respuestas `201` de aprobación y rotación |
| `postgres` | Base propia (`database/sql` + `pgx` v5, migraciones embebidas con `pg_advisory_lock`). `PartnerStore`: `partner`, `partner_credential` (una `active` y una `retiring` por socio, índices parciales únicos), `partner_history` (*append-only*, disparadores que rechazan `UPDATE`, `DELETE` y `TRUNCATE`) y `outbox_event`. `Mutate` bloquea la fila del socio (`FOR UPDATE`) mientras se llama al gateway, así que dos aprobaciones o rotaciones del mismo socio corren una tras otra. `OutboxStore`: el mismo relevo con *lease* de originación |
| `apigateway` | `ports.Gateway` sobre el API de administración de API Gateway (SDK v2): usage plan con cuota `DAY` y *throttle*, ligado al *stage* de la plataforma; API key habilitada; asociación y retiro; `GetUsage` del día. Borrar algo que ya no existe no es error; cualquier otro error es `gateway_unavailable` |
| `messaging` | Reservado (el servicio publica con el *outbox*; no consume eventos) |

## Límite por socio (CA-2)

Lo aplica el gateway: un socio que agota su cuota diaria o su ráfaga recibe `429` con el cuerpo estándar
`{"error":"rate_limited","message":"…"}` (respuestas `QUOTA_EXCEEDED` y `THROTTLED` del módulo `infra/terraform/modules/apigateway`), y
los demás socios siguen con su propio usage plan. Mientras dura la transición de una rotación, las dos keys comparten el plan y API
Gateway lleva la cuota de cada una por separado (riesgo aceptado, acotado al periodo de transición).

## Configuración (variables de entorno)

| Variable | Defecto | Descripción |
|---|---|---|
| `PORT` | `8080` | Puerto HTTP |
| `DATABASE_URL` | vacío | PostgreSQL de `partners` (secreto). Vacía: solo `/health`. Las migraciones se aplican al arrancar |
| `AUTH_ISSUER`, `AUTH_CLIENT_IDS`, `AUTH_JWKS_URL`, `AUTH_TOKEN_USE`, `AUTH_LEEWAY_SECONDS` | vacío | Verificación del JWT de Cognito (HA-S01). Sin `AUTH_ISSUER` las rutas de socios no se montan |
| `GATEWAY_REST_API_ID`, `GATEWAY_STAGE_NAME` | vacío | REST API y *stage* de la plataforma donde se crean los usage plans (van juntas). Vacías: la aprobación y la rotación responden `503 gateway_unavailable` y el trabajador de revocación no arranca. En local, `AWS_ENDPOINT_URL_API_GATEWAY` puede apuntar a LocalStack |
| `AWS_REGION` | — | Región del SDK (la plataforma la fija) |
| `CREDENTIAL_GRACE_PERIOD_HOURS` | `24` | Periodo de transición por defecto de una rotación (1–168) |
| `CREDENTIAL_REVOCATION_POLL_INTERVAL_MS` | `60000` | Cada cuánto el trabajador revoca las credenciales cuya transición venció |
| `EVENTS_TOPIC_ARN` | vacío | Tópico SNS `partners`. Vacío: los eventos quedan en el *outbox* |
| `OUTBOX_POLL_INTERVAL_MS` | `1000` | Sondeo del relevo del *outbox* |
| `ENVIRONMENT`, `LOG_LEVEL`, `OTEL_EXPORTER_OTLP_ENDPOINT` | `local`, `info`, vacío | Observabilidad (`pkg/observability`); `ENVIRONMENT` también se muestra en el detalle del socio y prefija el nombre del usage plan |

## Datos y eventos

- **BD propia:** PostgreSQL (migración `0001_partner.sql`: `partner`, `partner_credential`, `partner_history`, `outbox_event`).
- **Publica:** `PartnerCredentialsIssued`, `PartnerCredentialsRotated` y `PartnerCredentialsRevoked` en el tópico `partners` (consumidor:
  `audit`, registro de auditoría de suscripción). Nunca llevan el secreto. Ver [catálogo de eventos](../../docs/events/README.md).
- **Consume:** nada.

## Historias asociadas

Épica web 3 (F3.1, F3.2) y HA-01. **Fuera de HU-W3.1-01:** la baja del socio (HU-W3.1-02, estado `offboarded` reservado), la pantalla de
credenciales vista por el socio (B4), el alta del usuario del portal del socio en Cognito y las métricas de uso reciente (HU-W3.2-01).

## Desarrollo

```
go run ./services/partners/cmd/partners          # solo /health
DATABASE_URL='postgres://partners:<contraseña>@localhost:5432/partners?sslmode=disable' \
AUTH_ISSUER=… AUTH_CLIENT_IDS=… \
GATEWAY_REST_API_ID=<id> GATEWAY_STAGE_NAME=<stage> AWS_ENDPOINT_URL_API_GATEWAY=http://localhost:4566 \
  go run ./services/partners/cmd/partners
```

Pruebas: `go test ./services/partners/...`; `internal/adapters/postgres` usa PostgreSQL con Testcontainers (sin Docker se salta; con
`REQUIRE_DOCKER=1` falla si no hay Docker).
