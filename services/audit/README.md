# Servicio `audit` (auditoría y consentimientos)

Garantiza trazabilidad: cualquier decisión de suscripción o precio debe poder reconstruirse al 100 %, y los consentimientos de Open Finance deben ser verificables y revocables.

**Estado:** `GET /health` y, con `DATABASE_URL`, el módulo `consents` de **HU-W1.2-01** (consentimiento explícito e inmutable del cliente): `GET /v1/audit/consent-terms/current`, `POST /v1/audit/consents`, `GET /v1/audit/consent-verifications` y `GET /v1/audit/consents/{consent_id}`. El módulo `decisions` sigue reservado.

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `decisions` | Registro inmutable (*append-only*) de qué datos y reglas llevaron a un precio o decisión de aceptación/rechazo |
| `consents` | Registro, consulta y revocación de consentimientos de datos financieros (revocación efectiva ≤ 5 min). **HU-W1.2-01:** `consents.Service` publica los términos vigentes (`v1`), registra el consentimiento (idempotente por cliente, solicitud y versión; la fecha y hora la pone el servidor), lo consulta como evidencia y lo verifica para `origination`. La revocación sigue pendiente |

## Reglas de diseño

- La tabla de auditoría es *append-only*, particionada por fecha; no se permite `UPDATE` ni `DELETE` (HA-03).
- Consume eventos por SQS con consumidores **idempotentes** y DLQ.
- Mínimo privilegio y minimización de datos (Ley 1581 de 2012, Decreto 1297 de 2022, Circular 004 de 2024 SFC).

## Adaptadores (`internal/adapters`)

| Adaptador | Qué hace |
|---|---|
| `http` | Router `chi`: `GET /health` y, según las opciones, las rutas de consentimiento. `WithConsents`: términos vigentes, registro y verificación (sin token de Cognito: el canal del aliado entra con la API key del API Gateway y la verificación es una llamada entre servicios). `WithConsentReads`: la evidencia, con `security.Authenticate` + `RequireRole(operations, advisor)`, solo si `AUTH_ISSUER` está definido. Todas responden `Cache-Control: no-store` |
| `postgres` | Base de datos propia (`database/sql` + `pgx` v5). `Migrate` aplica al arrancar las migraciones embebidas de `migrations/` con `pg_advisory_lock` y `schema_migrations`. `ConsentStore`: tabla `consent_record` *append-only* (HU-W1.2-01) |

## Consentimiento (HU-W1.2-01)

`audit` es el **dueño del consentimiento**: `origination` ya no confía en el `consent_active` que afirma quien llama, sino que
pregunta aquí antes de consultar la caché o cualquier fuente. Contrato para el front y el aliado:
[`docs/contratos/HU-W1.2-01-api.md`](../../docs/contratos/HU-W1.2-01-api.md).

```
GET  /v1/audit/consent-terms/current ─▶ términos versionados (datos, fuentes, finalidad, vigencia)  [CA-1]
POST /v1/audit/consents ──────────────▶ consent_record: titular, copia del alcance, canal, granted_at del servidor  [CA-2]
GET  /v1/audit/consent-verifications ─▶ {active, consent_id, granted_at, expires_at, sources}  (origination, CA-3)
GET  /v1/audit/consents/{id} ─────────▶ evidencia completa (operations, advisor)  [CA-2]
```

- **Inmutable (CA-4):** `consent_record` rechaza `UPDATE`, `DELETE` y `TRUNCATE` con disparadores (`forbid_consent_mutation()`,
  error `restrict_violation`), y el puerto `ConsentStore` no tiene operaciones de modificación. Un superusuario de la base podría
  quitar los disparadores: el rol con el que se conecta el servicio no debe ser el dueño de la tabla (pendiente de la
  infraestructura).
- **Vigencia:** el consentimiento dura 12 meses desde `granted_at` (activo hasta justo antes de `expires_at`). El KYC es de un solo
  uso, pero su registro de uso aún no existe: mientras tanto dura lo que el consentimiento.
- **Todo o nada:** `accepted_sources` debe traer todas las fuentes de los términos y `accepted_data_policy` debe ser `true`. Con una
  versión de términos distinta de la vigente la respuesta es `409 terms_outdated`.
- **Idempotencia:** `(customer_id, application_id, terms_version)` es único; repetir el `POST` devuelve el consentimiento guardado con
  `200`.
- **Verificación sin autenticación:** `GET /v1/audit/consent-verifications` es una llamada entre servicios sin token. **Debe ser
  alcanzable solo desde la red interna** (ALB interno o grupo de seguridad), nunca publicarse a los aliados.
- **Pendiente (fuera de alcance):** revocación del consentimiento (será un registro nuevo, nunca un `UPDATE`; el Terraform cita
  «CA-5, revocación ≤ 5 min», pero la HU publicada, issue #64, tiene cuatro criterios), particionado de `consent_record` por fecha y
  registro del uso único del KYC. No publica ni consume eventos.

## Configuración (variables de entorno)

| Variable | Defecto | Descripción |
|---|---|---|
| `PORT` | `8080` | Puerto HTTP |
| `DATABASE_URL` | vacío | PostgreSQL de `audit` (secreto: viene del entorno o de Secrets Manager, nunca se registra). Vacía: solo se sirve `/health` y las rutas de consentimiento **no se montan** (aviso en el log). Definida: las migraciones se aplican al arrancar |
| `AUTH_ISSUER`, `AUTH_CLIENT_IDS`, `AUTH_JWKS_URL`, `AUTH_TOKEN_USE`, `AUTH_LEEWAY_SECONDS` | vacío | Verificación del JWT de Cognito (HA-S01, ver [docs/api](../../docs/api/README.md#autenticación-y-autorización-ha-s01)). Con `AUTH_ISSUER` vacío la lectura de la evidencia **no se monta**; si está definido, `AUTH_CLIENT_IDS` es obligatoria |
| `LOG_LEVEL`, `ENVIRONMENT`, `OTEL_EXPORTER_OTLP_ENDPOINT` | `info`, `local`, vacío | Observabilidad (`pkg/observability`): logs JSON con `service`, `correlation_id` y `trace_id`/`span_id`, trazas y métricas |

## Datos y eventos

- **BD propia:** PostgreSQL (auditoría, append-only); hoy la tabla `consent_record` (HU-W1.2-01), con migraciones en `migrations/`.
- **Consume:** eventos de negocio de originación y siniestros. Ver [catálogo de eventos](../../docs/events/README.md). HU-W1.2-01 no publica ni consume eventos.

## Historias asociadas

Épica web 4 (F4.1, F4.2), épica móvil 1 (F1.3), HA-03, EC-S01 y EC-S02.

## Desarrollo

```
go run ./services/audit/cmd/audit            # solo /health
DATABASE_URL='postgres://audit:<contraseña>@localhost:5432/audit?sslmode=disable' \
  go run ./services/audit/cmd/audit          # con los consentimientos (HU-W1.2-01)
```

Pruebas: `REQUIRE_DOCKER=1 go test ./services/audit/...` (PostgreSQL con Testcontainers; sin la variable, las pruebas que necesitan
Docker se saltan). `tests/integration/consent_test.go` ejercita cada criterio de aceptación de HU-W1.2-01 por HTTP;
[`tests/contract`](tests/contract/README.md) corre Schemathesis contra el servicio real y
[`tests/e2e/hu-w1.2`](../../tests/e2e/hu-w1.2/README.md) lo prueba junto con `origination`.
