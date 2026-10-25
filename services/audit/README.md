# Servicio `audit` (auditoría y consentimientos)

Garantiza trazabilidad: cualquier decisión de suscripción o precio debe poder reconstruirse al 100 %, y los consentimientos de Open Finance deben ser verificables y revocables.

**Estado:** `GET /health` y, con `DATABASE_URL`, el módulo `consents` de **HU-W1.2-01** (consentimiento explícito e inmutable del cliente): `GET /v1/audit/consent-terms/current`, `POST /v1/audit/consents`, `GET /v1/audit/consent-verifications` y `GET /v1/audit/consents/{consent_id}`. **HU-M1.3-01:** el asegurado consulta sus propios consentimientos (`GET /v1/consents` y `GET /v1/consents/{consent_id}`, con `AUTH_*`), con el estado y el historial derivados de `consent_event`; `audit` reconoce al titular por el vínculo `sub` ↔ documento que le llega con el evento `CustomerLinked` de originación (`EVENTS_QUEUE_URL`). **HU-M1.3-02:** el asegurado revoca un consentimiento (`POST /v1/consents/{consent_id}/revocations`) y descarga el comprobante en PDF (`GET /v1/consents/{consent_id}/revocation-receipt`); el registro es inmutable y queda antes de responder, el consentimiento deja de verificarse como activo en ese instante y, con `OPEN_FINANCE_URL`, un trabajador avisa a cada entidad dentro de 5 minutos. El módulo `decisions` sigue reservado.

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `decisions` | Registro inmutable (*append-only*) de qué datos y reglas llevaron a un precio o decisión de aceptación/rechazo |
| `consents` | Registro, consulta y revocación de consentimientos de datos financieros (revocación efectiva ≤ 5 min). **HU-W1.2-01:** `consents.Service` publica los términos vigentes (`v1`), registra el consentimiento (idempotente por cliente, solicitud y versión; la fecha y hora la pone el servidor), lo consulta como evidencia y lo verifica para `origination`. **HU-M1.3-01:** `consents.HolderService` guarda el vínculo de la cuenta con su documento (idempotente por `subject`; un documento distinto para el mismo `subject` conserva el primero y deja un aviso), resuelve el titular de un token (`ErrHolderNotLinked` sin vínculo) y lista u obtiene sus consentimientos con `domain.View` (estado derivado, entidad del catálogo, fin de vigencia, comprobante e historial); uno ajeno responde como inexistente. **HU-M1.3-02:** `consents.RevocationService` revoca (genérico `Revoke`, el que reutiliza el portal, y `RevokeAsHolder`), genera el comprobante (`Receipt`) y reintenta solo las entidades no confirmadas (`RetryPending`); `consents.NoticeWorker` avisa a cada entidad con `pkg/idempotency` (un reclamo por intento, la misma clave en cada intento, ventana de 5 min); `Service.Verify` responde con el estado derivado (`WithEvents`): revocado o parcial ⇒ `active=false` |

## Reglas de diseño

- La tabla de auditoría es *append-only*, particionada por fecha; no se permite `UPDATE` ni `DELETE` (HA-03).
- Consume eventos por SQS con consumidores **idempotentes** y DLQ (hoy `CustomerLinked`, HU-M1.3-01).
- El estado de un consentimiento nunca es una columna que cambia: se deriva de `consent_record` y de los eventos *append-only* de
  `consent_event` con una función determinista que recibe el instante (`domain.DeriveStatus`, D-4 a).
- Mínimo privilegio y minimización de datos (Ley 1581 de 2012, Decreto 1297 de 2022, Circular 004 de 2024 SFC).

## Adaptadores (`internal/adapters`)

| Adaptador | Qué hace |
|---|---|
| `http` | Router `chi`: `GET /health` y, según las opciones, las rutas de consentimiento. `WithConsents`: términos vigentes, registro y verificación (sin token de Cognito: el canal del aliado entra con la API key del API Gateway y la verificación es una llamada entre servicios). `WithConsentReads`: la evidencia, con `security.Authenticate` + `RequireRole(operations, advisor)`, solo si `AUTH_ISSUER` está definido. **HU-M1.3-01:** `WithHolderConsents`: `GET /v1/consents` y `GET /v1/consents/{consent_id}` con `security.Authenticate` + `RequireRole(insured)`; el titular es el `sub` del token (`403 holder_not_linked` sin vínculo, `404 not_found` para lo ajeno), solo `GET`. **HU-M1.3-02:** `WithHolderRevocations`, con los mismos guardias: `POST /v1/consents/{consent_id}/revocations` (`Idempotency-Key` UUID obligatoria) y `GET /v1/consents/{consent_id}/revocation-receipt`; el consentimiento gana `revocable` y `revocation`. Todas responden `Cache-Control: no-store`; `catalog_test.go` tiene la matriz 401/403/200 de todas las rutas humanas |
| `postgres` | Base de datos propia (`database/sql` + `pgx` v5). `Migrate` aplica al arrancar las migraciones embebidas de `migrations/` con `pg_advisory_lock` y `schema_migrations`. `ConsentStore`: tabla `consent_record` *append-only* (HU-W1.2-01); **HU-M1.3-01:** `ListByHolder` (por `upper(holder_document_id)`), `ConsentEventStore` (lector de `consent_event`, `0002`) y `HolderLinkStore` (`holder_link`, `0003`: `INSERT … ON CONFLICT DO NOTHING` y relectura). **HU-M1.3-02 (`0004`):** `RevocationStore` (registro, eventos y primer paso de cada aviso en una transacción; número de comprobante de una secuencia) y `NoticeStore` (cola de avisos **solo con inserciones**: cada cambio es un paso nuevo y el reclamo es un `INSERT … SELECT … ON CONFLICT DO NOTHING` sobre el último paso; `Confirm` y `Fail` escriben además el evento de la entidad). Ninguno tiene sentencias de modificación |
| `openfinance` | **HU-M1.3-02:** puente entre `pkg/adapters/openfinance` (`RevokeConsent`) y el puerto `EntityNotifier`: envía solo el consentimiento, la entidad, el cliente del aliado y la hora efectiva, con la clave de idempotencia del aviso |
| `pdf` | **HU-M1.3-02:** comprobante de la revocación con `go-pdf/fpdf` v0.9.0: número, consentimiento, entidad, titular con el documento enmascarado, alcance, solicitud y fecha y hora exactas de finalización (Colombia y UTC), canal y dispositivo. Determinista; español por defecto e inglés con `Accept-Language: en` |
| `messaging` | **HU-M1.3-01:** manejador idempotente de `CustomerLinked` (decodifica el *payload*, normaliza y valida el vínculo y llama a `HolderService.Link`; un *payload* inválido es error, así que el mensaje termina en la DLQ tras los reintentos; registra «holder linked» con `event_id` y `customer_id`, nunca el documento). Lo sirve `pkg/events/sqsconsumer` desde `cmd/audit` |

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
- **En AWS (T5, `infra/terraform/modules/platform`):** los términos y el registro del consentimiento
  (`GET /v1/audit/consent-terms/current` y `POST /v1/audit/consents`) se publican en el API Gateway como rutas de socio, con la API
  key del aliado (en el portal web la pone CloudFront). La verificación **no se publica** (el gateway responde `403`): originación la
  pide dentro de la VPC, a `audit` registrado en Cloud Map (`AUDIT_URL=http://audit.<nombre>.local:8080`). La evidencia
  (`GET /v1/audit/consents/{consent_id}`) tampoco se publica (queda para HU-W4.2-02); desde HU-M1.3-01 `audit` recibe `AUTH_*` en AWS
  para `GET /v1/consents*`, que sí se publica como ruta humana con Cognito.
- **Revocación:** la construye HU-M1.3-02 (sección siguiente) como un registro nuevo, nunca un `UPDATE`; cubre además el CA-5 de
  HU-W1.2-01 («revocar en cualquier momento… efectiva en un máximo de 5 minutos»).
- **Pendiente (fuera de alcance):** particionado de `consent_record` por fecha y registro del uso único del KYC. HU-W1.2-01 no publica
  ni consume eventos.

## Consulta del asegurado (HU-M1.3-01)

Contrato para el móvil: [`docs/contratos/HU-M1.3-01-api.md`](../../docs/contratos/HU-M1.3-01-api.md). Las rutas de `audit` quedan en dos
familias, un prefijo por recurso (D-3 a, [docs/api](../../docs/api/README.md#convención-de-rutas)):

```
Personas (Cognito):        GET /v1/consents, GET /v1/consents/{id}       (insured, el titular es el sub)
                           GET /v1/audit/consents/{id}                   (operations, advisor; el gateway aún no la publica)
Bancos y aliados (API key): GET /v1/audit/consent-terms/current, POST /v1/audit/consents
Interna (no se publica):   GET /v1/audit/consent-verifications
```

```
origination ──CustomerLinked (SNS origination → SQS audit)──▶ messaging ──▶ HolderService.Link ──▶ holder_link (append-only)
móvil ──GET /v1/consents (token)──▶ sub ──holder_link──▶ documento ──▶ consent_record + consent_event ──▶ domain.View
```

- **Estado (D-4 a):** revocado si un evento del consentimiento dice `revoked` antes de `expires_at` (fin = esa hora, comprobante = su
  `receipt_id`); si no, vencido desde `expires_at` (calculado, sin evento); si no, el último evento si es un estado conocido; si no, activo.
  Solo `active` es vigente. Desde HU-M1.3-02, `partially_revoked` es un revocado que alguna entidad no confirmó; `revocation_pending`
  sigue reservado y el servidor no lo usa.
- **Entidad (D-2 a):** el aliado del consentimiento, con el nombre de `PARTNER_DISPLAY_NAMES`.
- **CA-6:** el documento se compara normalizado (recorte y mayúsculas) y un id ajeno responde `404`; la respuesta no repite documento ni
  nombre del titular.
- `consent_event` no tiene llave foránea a `consent_record` a propósito: PostgreSQL rechazaría el `TRUNCATE` de `consent_record` por la
  llave antes de su disparador y cambiaría el error *append-only* de HU-W1.2-01. La referencia la hace cumplir el disparador
  `consent_event_requires_consent` (`BEFORE INSERT … FOR EACH ROW`, migración `0002`) con la función genérica
  `require_existing_consent()`: un hecho cuyo `consent_id` no existe en `consent_record` se rechaza con `23503`
  (`foreign_key_violation`, el mismo error de una llave foránea) y, si la sentencia trae varias filas, no se guarda ninguna. No toma
  bloqueo de fila porque `consent_record` no admite borrados. **Regla para tablas nuevas de esta base:** las tablas *append-only* no se
  referencian entre sí con llaves foráneas; la referencia se valida con un disparador `BEFORE INSERT` (para un `consent_id`, con
  `require_existing_consent()`).
- Pruebas: `internal/domain/consent_view_test.go`, `internal/consents/holder_test.go`, `internal/adapters/http/holder_consents_test.go`,
  `tests/integration/holder_consents_test.go` y `customer_linked_test.go` (LocalStack: duplicados, conflicto, tipo desconocido y DLQ), el
  contrato con un token de asegurado y [`tests/e2e/hu-m1.3`](../../tests/e2e/hu-m1.3/README.md).

## Revocación del consentimiento (HU-M1.3-02)

Contrato para el móvil: [`docs/contratos/HU-M1.3-02-api.md`](../../docs/contratos/HU-M1.3-02-api.md).

```
app ──POST /v1/consents/{id}/revocations (Idempotency-Key)──▶ RevocationService.Revoke, en una transacción y antes de responder:
       consent_revocation (inmutable) + consent_event revocation_requested y revoked + un aviso por entidad (ventana de 5 min)
origination ──GET /v1/audit/consent-verifications──▶ Verify con el estado derivado ⇒ active=false desde effective_at
NoticeWorker ──pkg/idempotency (clave revocation-<consentimiento>-<entidad>, esperas de 1 s a 30 s)──▶ pkg/adapters/openfinance
       confirmada ⇒ revocation_confirmed · rechazo o ventana vencida ⇒ revocation_not_confirmed ⇒ partially_revoked
```

- **Estado (D-1 a):** con un `revoked` anterior a `expires_at`, el consentimiento deja de estar vigente desde `effective_at`. Es
  `partially_revoked` si alguna entidad tiene `revocation_not_confirmed` sin un `revocation_confirmed` posterior, o sigue sin confirmar y
  ya pasó el fin de su ventana (derivado por fecha, como el vencimiento, para no depender de que el trabajador haya corrido). La
  ventana de cada entidad viaja en los `details` del evento `revoked` (y de cada `revocation_retry_requested`). Un `revoked` sin
  entidades (por ejemplo, de una semilla anterior a esta historia) no tiene nada que avisar.
- **Inmutable (CA-4):** `consent_revocation`, `revocation_notice` y `revocation_notice_step` rechazan `UPDATE`, `DELETE` y `TRUNCATE`
  (`forbid_append_only_mutation()`). Sin llaves foráneas (decisión #89): `consent_revocation` y `revocation_notice` usan
  `require_existing_consent()` y `revocation_notice_step` usa `require_existing_notice()` hacia su cabecera, con el error de una llave
  foránea (`23503`). Un `23503` al revocar es un defecto (el servicio ya leyó el consentimiento): se responde `503`, nunca `404`.
- **Idempotencia y CA-6:** `UNIQUE (consent_id)`, `UNIQUE (request_id)` y `UNIQUE (receipt_number)`. La misma `Idempotency-Key`
  devuelve la revocación guardada (`200`); otra clave sobre un revocado es `409 consent_already_revoked`; un vencido,
  `409 consent_not_active`.
- **Comprobante (D-4 a, D-5 a):** `REV-<año en Colombia>-<consecutivo>` de la secuencia `revocation_receipt_seq` (un número tomado por
  una transacción que no se confirma se pierde: puede haber huecos, nunca repetidos); el PDF se genera a pedido desde el registro.
- **Cola de avisos (D-2 a, D-3 a):** en la propia base de `audit`, escrita en la misma transacción que la revocación (no se pierde ante
  un reinicio). Varias tareas pueden correr el trabajador a la vez: el reclamo atómico deja una sola llamada por intento (EC-D02).
- **Minimización:** al ecosistema solo viaja el consentimiento, la entidad, el cliente del aliado y la hora efectiva; los logs del
  trabajador llevan el consentimiento y la entidad, nunca el documento ni el nombre.
- Pruebas: `internal/domain/revocation_test.go`, `internal/consents/{revocation_test.go,notices_test.go,service_test.go}`,
  `internal/adapters/postgres/{revocations_test.go,notices_test.go}`, `internal/adapters/{http/revocations_test.go,openfinance,pdf}`,
  `tests/integration/revocation_test.go` (PostgreSQL real: atomicidad, tablas inmutables, disparadores, comprobantes sin repetir, 20
  trabajadores por paso, reclamo perdido y HTTP de punta a punta), el contrato con el token del asegurado y
  [`tests/e2e/hu-m1.3`](../../tests/e2e/hu-m1.3/README.md) (R1–R11).

### Cómo reutilizar la revocación (HU-W4.2-02)

El portal de cumplimiento solo agrega sus rutas; el proceso ya existe:

1. Una ruta del portal (rol de cumplimiento, `operations`) que construye un `consents.RevokeCommand` con
   `Actor{Type: "operations", ID: <sub>}`, `Channel` = una constante nueva `compliance_portal` en `domain`, `Reason` y `Reference`
   obligatorios en la ruta, `Request.RequestedAt` = hora del servidor (`ConfirmationMethod` y `Device` pueden ir vacíos) y su
   `RequestID`, y llama a `RevocationService.Revoke`. Recibe la misma vista (`domain.ConsentView` con `Revocation`).
2. Una ruta que llama a `RevocationService.RetryPending(consentID, actor)`: inicia una ventana nueva solo para las entidades no
   confirmadas (las confirmadas nunca se vuelven a avisar), deja un evento `revocation_retry_requested` con el actor y despierta al
   trabajador.
3. El estado por entidad, el resumen (`in_progress`, `completed`, `partial`), la ventana de 5 minutos, la revocación parcial y el
   registro inmutable no cambian. Mostrar «Revocación en curso» en el portal es solo leer `notice_status`.

## Configuración (variables de entorno)

| Variable | Defecto | Descripción |
|---|---|---|
| `PORT` | `8080` | Puerto HTTP |
| `DATABASE_URL` | vacío | PostgreSQL de `audit` (secreto: viene del entorno o de Secrets Manager, nunca se registra). Vacía: solo se sirve `/health` y las rutas de consentimiento **no se montan** (aviso en el log). Definida: las migraciones se aplican al arrancar |
| `AUTH_ISSUER`, `AUTH_CLIENT_IDS`, `AUTH_JWKS_URL`, `AUTH_TOKEN_USE`, `AUTH_LEEWAY_SECONDS` | vacío | Verificación del JWT de Cognito (HA-S01, ver [docs/api](../../docs/api/README.md#autenticación-y-autorización-ha-s01)). Con `AUTH_ISSUER` vacío la lectura de la evidencia y `GET /v1/consents*` (HU-M1.3-01) **no se montan**; si está definido, `AUTH_CLIENT_IDS` es obligatoria |
| `EVENTS_QUEUE_URL` | vacío | Cola SQS de `audit` (HU-M1.3-01; la plataforma inyecta la cola `audit`). Con ella y `DATABASE_URL` se consume `CustomerLinked`; vacía, aviso en el log y los asegurados reciben `403 holder_not_linked`. En local, `AWS_ENDPOINT_URL_SQS` apunta a LocalStack |
| `AWS_REGION` | — | Región del SDK de AWS para la cola (la plataforma la fija, `us-east-2`) |
| `PARTNER_DISPLAY_NAMES` | vacío | Objeto JSON de código de aliado a nombre visible (`{"aliado-banco-1":"Banco Aliado"}`, D-2 a); un código sin nombre se muestra tal cual. Inválido: no arranca |
| `OPEN_FINANCE_URL` | vacío | Proveedor de Open Finance (el stub en local y en AWS, por Cloud Map) al que se avisan las revocaciones (HU-M1.3-02). Vacía: las revocaciones se registran y bloquean igual, el trabajador no arranca (aviso en el log) y las entidades quedan como no confirmadas al vencer el plazo |
| `OPEN_FINANCE_TIMEOUT_MS` | `700` | Corte duro de cada aviso |
| `REVOCATION_NOTICE_DEADLINE_SECONDS` | `300` | Plazo de RN-02 para que cada entidad confirme; las pruebas lo acortan |
| `REVOCATION_NOTICE_POLL_INTERVAL_MS` | `1000` | Sondeo del trabajador de avisos, además de despertarse con cada revocación |
| `LOG_LEVEL`, `ENVIRONMENT`, `OTEL_EXPORTER_OTLP_ENDPOINT` | `info`, `local`, vacío | Observabilidad (`pkg/observability`): logs JSON con `service`, `correlation_id` y `trace_id`/`span_id`, trazas y métricas |

## Datos y eventos

- **BD propia:** PostgreSQL (auditoría, append-only); hoy las tablas `consent_record` (HU-W1.2-01), `consent_event` y `holder_link`
  (HU-M1.3-01, migraciones `0002` y `0003`, con las funciones genéricas `forbid_append_only_mutation()` y
  `require_existing_consent()`) y `consent_revocation`, `revocation_notice` y `revocation_notice_step` con la secuencia
  `revocation_receipt_seq` (HU-M1.3-02, migración `0004`, con `require_existing_notice()`), en `migrations/`.
- **Consume:** `CustomerLinked` de originación (HU-M1.3-01) desde su cola, idempotente por `subject`; los demás tipos que llegan a la cola
  se descartan hasta que una historia los consuma. Ver [catálogo de eventos](../../docs/events/README.md). No publica eventos (HU-M1.3-02
  avisa a las entidades por HTTP desde su propia cola, decisión D-2 a).

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
