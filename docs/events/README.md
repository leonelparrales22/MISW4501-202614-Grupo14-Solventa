# Catálogo de eventos

Los servicios publican hechos de negocio en Amazon SNS (un tópico por dominio) y cada consumidor tiene su cola SQS con DLQ. Reglas: publicación con *transactional outbox*, consumidores idempotentes, entrega *at-least-once* sin orden estricto.

| Evento | Nombre en la Wiki | Publica | Consume | Propósito |
|---|---|---|---|---|
| `PolicyIssued` | `PolizaEmitida` | origination | origination (documento contractual, HU-W1.3-03), audit, notifications | Póliza emitida tras aceptación y cobro; es la «cobertura activada» que dispara el documento contractual y la notificación |
| `ChargeCompleted` | `CobroRealizado` | origination | audit | Cobro confirmado (idempotente) |
| `OfferGenerated` | `OfertaGenerada` | origination | audit | **Publicación pendiente (HU-W4.1-01).** Oferta personalizada generada (HU-W1.2-03); hoy solo queda en la tabla `offer` |
| `OfferRequoted` | `OfertaRecotizada` | origination | audit, notifications | **Publicación pendiente (HU-W4.1-01).** Una oferta provisional se reemplazó por una normal al volver la fuente (HU-W1.2-03); hoy el aviso al cliente es el correo `offer-requoted:<id>` |
| `ClaimApproved` | `SiniestroAprobado` | claims | notifications, audit | Dispara notificación y pago paramétrico |
| `ParametricPayoutCompleted` | `PagoParametricoRealizado` | claims | notifications (HU-M3.1-01) | El pago automático de un siniestro paramétrico quedó confirmado: `notifications` registra la notificación y envía el *push* en ≤ 60 s. **Lo publica el motor paramétrico de `claims` con *outbox* desde HU-M2.4-01**, cuando la pasarela confirma el pago del siniestro que resolvió (antes lo simulaba `tests/e2e/hu-m3.1`) |
| `ParametricTriggerObserved` | `ParametroObservado` | iot (telemetría; hoy `tests/e2e/hu-m2.4/nube.sh` y `app-local.sh`) | claims (HU-M2.4-01) | La telemetría observó el parámetro de una póliza paramétrica (el retraso del vuelo de un asegurado), ya asociado a la póliza. El motor paramétrico de `claims` radica, resuelve y paga el siniestro si el valor alcanza el umbral |
| `ClaimStageChanged` | `EtapaDelSiniestroCambiada` | claims | notifications (HU-M3.3-01) | Un siniestro llegó a una etapa que el asegurado espera: en evaluación, aprobado o pagado. `notifications` registra la notificación y envía el *push* en ≤ 60 s. Lo escribe en el *outbox* `claims/internal/stages` a partir del historial de estados |
| `PolicyExpiryApproaching` | `VencimientoDePolizaProximo` | origination | notifications (HU-M3.2-01) | Una póliza activa del asegurado (de originación o del núcleo de pólizas) llegó a 30, 15 o 5 días de su vencimiento. `notifications` registra la notificación con la fecha exacta de vencimiento y envía el *push*. Lo escribe en el *outbox* `origination/internal/expiryalert`, un barrido horario |
| `CustomerLinked` | `ClienteVinculado` | origination | audit | La cuenta del asegurado (`sub` de Cognito) quedó vinculada a su documento al terminar el registro (HU-M1.1-01); `audit` lo usa para reconocer al titular de sus consentimientos (HU-M1.3-01). **Primer evento publicado con *outbox*** |
| `PartnerCredentialsIssued`, `PartnerCredentialsRotated`, `PartnerCredentialsRevoked` | `CredencialesSocioEmitidas`, `CredencialesSocioRotadas`, `CredencialesSocioRevocadas` | partners | audit | Se generaron las credenciales de un socio al aprobarlo, se rotaron o se revocó la anterior al vencer la transición (HU-W3.1-01); `audit` las guarda en el registro de auditoría de suscripción con la fecha y el emisor. **Primer evento del tópico `partners`** |

**HU-W1.3-01 (aceptación de la oferta) no publica ni consume eventos:** la aceptación se persiste en origination (PostgreSQL con `DATABASE_URL`, fix C5) y la suscripción, el envío a la bandeja del asesor (HU-W1.1-02) y el inicio de la emisión ocurren en proceso. `PolicyIssued` y `ChargeCompleted` en SNS siguen pendientes de HU-W1.3-02 (con `pkg/outbox` y `pkg/events`). Si la bandeja o la auditoría de la decisión de suscripción pasan a otro servicio, el evento correspondiente se registrará aquí.

**HU-W1.1-02 (bandeja de decisión humana) no publica ni consume eventos:** la bandeja recibe en proceso lo que la suscripción enruta, la decisión del asesor se guarda *append-only* en origination (tabla `human_decision`, fix C5) y la continuación (emisión, oferta ajustada o rechazo) ocurre en proceso. El correo que avisa al cliente el ajuste o el rechazo sale de origination por el mismo canal que el de HU-W1.3-03 (`pkg/adapters/email`, con un trabajador que reintenta y su propia clave de idempotencia `review-outcome:<acceptance_id>`), sin eventos. Si la auditoría de largo plazo (HU-W4.1-01) o las notificaciones pasan a otro servicio, el evento correspondiente (p. ej. `HumanDecisionRecorded`, con *outbox*) se registrará aquí.

**HU-W1.2-01 (consentimiento explícito) no publica ni consume eventos:** `audit` registra el consentimiento en su propia base y `origination` lo verifica con una llamada HTTP síncrona (`GET /v1/audit/consent-verifications`), no con mensajes. La revocación la construye HU-M1.3-02 (párrafo siguiente).

**HU-M1.3-02 (revocación del consentimiento) no publica ni consume eventos:** `audit` registra la revocación en su propia base y, en la misma transacción, la cola de avisos a las entidades (decisión D-2 a); un trabajador de `audit` los envía al ecosistema Open Finance por HTTP (`pkg/adapters/openfinance`, `POST /consent-revocations` con clave de idempotencia) y `origination` deja de consultar las fuentes porque la verificación síncrona responde `active=false`. Si el aviso pasa a un consumidor en otro servicio, los eventos `ConsentRevocationRequested` y `ConsentRevocationConfirmed` (con *outbox*) se registrarán aquí.

**HU-W4.2-02 (revocación desde el portal de cumplimiento) no publica ni consume eventos:** reutiliza la cola de avisos de HU-M1.3-02 en la base de `audit` (el registro inmutable, los eventos del consentimiento y un aviso por entidad se escriben en una transacción; el reintento abre una ventana nueva solo para las entidades no confirmadas). `origination` se entera por la verificación síncrona (`GET /v1/audit/consent-verifications` responde `active=false`), no por mensajes. Si el aviso pasa a un consumidor en otro servicio, los eventos `ConsentRevocationRequested` y `ConsentRevocationConfirmed` (con *outbox*) se registrarán aquí.

**HU-W4.1-01 (auditoría de decisiones) no publica ni consume eventos todavía:** origination registra cada decisión de suscripción en `audit` con una llamada HTTP interna e idempotente (`POST /v1/audit/decisions`). `OfferGenerated` y `OfferRequoted` siguen registrados arriba para cuando se migre a *outbox* + SNS/SQS.

**HU-W1.2-03 (oferta personalizada) no publica ni consume eventos todavía:** la oferta se persiste en `origination` y el reemplazo de una oferta provisional y su correo ocurren en proceso. `OfferGenerated` y `OfferRequoted` quedan **registrados** para cuando la auditoría de largo plazo (HU-W4.1-01) los necesite, con `pkg/outbox` y `pkg/events` (se escriben en la misma tabla `outbox_event` de originación). Payload previsto:

| Evento | Campos |
|---|---|
| `OfferGenerated` | `event_id`, `offer_id`, `customer_id`, `consent_id`, `partner_id`, `application_id`, `channel`, `mode`, `provisional_reason`, `profile_source`, `profile_category`, `rater_version`, `premium`, `currency`, `created_at`, `expires_at`. Nunca `score`, `rules` ni el documento |
| `OfferRequoted` | `event_id` (`offer-requoted:<offer_id>`), `offer_id` (la reemplazada), `new_offer_id`, `old_premium`, `new_premium`, `currency`, `requoted_at` |

**HU-W2.1-01 (radicación del siniestro con soportes desde el portal web) no publica ni consume eventos:** el caso, sus soportes y la copia de la póliza se guardan en `claims` y la póliza se consulta de forma síncrona al core de pólizas (`pkg/adapters/policies`). El triage (HU-W2.2-01) necesitará saber cuándo un caso entra a evaluación; ese evento (p. ej. `ClaimFiled`, o la aprobación de la radicación de HU-W2.1-03, con *outbox*) se registrará aquí con esa historia.

**HU-W2.2-01 (triage automático del siniestro) no publica ni consume eventos:** el triage corre dentro de `claims` y un barrido de su propia base encuentra los casos que entran a evaluación o cuya información cambió (monto estimado, tipo, cobertura o versión de la política), así que el evento que anticipaba la nota de HU-W2.1-01 no hizo falta. El nivel vigente de cada caso queda en la vista `claim_current_triage` del mismo servicio.

**HU-W2.3-01 (asignación del prestador más cercano) no publica ni consume eventos:** el caso, el registro de prestadores y la asignación están en la misma base de `claims`, y el aviso al prestador asignado es un correo (`pkg/adapters/email`, llave de idempotencia `asignacion-<claim_id>`) que envía el *worker* del módulo `assessors` desde la tabla `provider_notice`, escrita en la misma transacción que la asignación. La asistencia en sitio (HU-W2.3-03) usará la misma búsqueda en proceso (`assessors.Suggest`), sin evento.

**HU-W1.3-03 (documento contractual) consume la activación de la cobertura en proceso**, con `ports.CoverageActivationHandler` y un consumidor idempotente por póliza (el *outbox* y SNS/SQS ya existen en `pkg/outbox` y `pkg/events`). **La publica en proceso el módulo `billing` de origination (HA-D02) solo cuando el cobro de la prima queda confirmado** (CA-4): el evento se arma con la foto guardada en `charge_attempt` (`event_id` `evt-coverage-activated-<acceptance_id>`, `policy_id` `pol-<acceptance_id>`), así que cada reintento de la publicación entrega exactamente el mismo evento; si el consumidor falla, se reintenta con espera creciente y sin ventana. Sin pasarela de pagos configurada nada activa coberturas. La asignación del nombre `PolicyIssued` a la «cobertura activada» está **por confirmar con el equipo**.

Payload del evento tal como lo consume el documento contractual (`domain.CoverageActivated`):

| Campo | Descripción |
|---|---|
| `event_id` | Identifica el hecho; una redelivery lo repite |
| `policy_id` | Póliza (clave de idempotencia); letras, dígitos, `-` y `_` |
| `acceptance_id`, `customer_id` | Aceptación de origen y asegurado dueño de la póliza |
| `holder` | `full_name`, `document_id`, `email` (canal de contacto de la notificación) |
| `premium`, `currency`, `insured_amount`, `coverages[]` | Condiciones económicas que imprime el documento |
| `activated_at`, `coverage_start`, `coverage_end` | Activación (inicio del plazo de 60 s) y vigencia |

## `CustomerLinked` (HU-M1.3-01)

Lo escribe el registro del asegurado (`POST /v1/registrations`, HU-M1.1-01) **en la misma transacción** en que originación guarda el
`sub` de la cuenta recién creada (`CustomerRepository.Link`): no queda `sub` sin evento ni evento sin `sub`. La migración `0008` de
originación encola también uno por cada cliente que ya tenía `sub` antes de esta historia, con el mismo id y el mismo sobre.

**Sobre** (común a todos los eventos, `pkg/events.Envelope`):

| Campo | Descripción |
|---|---|
| `event_id` | Determinista por hecho: `customer-linked-<customer_id>` (un vínculo por cliente) |
| `event_type` | `CustomerLinked` |
| `event_version` | `1` (un cambio incompatible sube la versión; un campo opcional nuevo no) |
| `source` | `origination` |
| `occurred_at` | Instante del vínculo, RFC 3339 en UTC |
| `correlation_id` | Opcional |
| `payload` | Objeto JSON con los campos de abajo |

**Payload** (minimización: nunca nombre, correo, fecha de nacimiento ni llaves de las imágenes del documento):

| Campo | Descripción |
|---|---|
| `customer_id` | Cliente de originación |
| `subject` | `sub` de la cuenta en el *user pool* (la clave del vínculo) |
| `document_id` | Documento de identidad del registro |
| `linked_at` | Instante del vínculo (UTC) |

**Cómo se publica:** con `pkg/outbox` y `pkg/events`. El evento queda en la tabla `outbox_event` de la base de originación (o en el
*outbox* en memoria sin `DATABASE_URL`) y un relevo por tarea lo reclama con *lease* (`FOR UPDATE SKIP LOCKED`), lo publica en el tópico
`origination` (`EVENTS_TOPIC_ARN`) con el atributo de mensaje `event_type` y lo marca publicado; si SNS falla lo reprograma con espera
creciente (500 ms, tope 60 s) y sin ventana: nunca se descarta. Sin `EVENTS_TOPIC_ARN` los eventos quedan en el *outbox*. Las filas
publicadas no se purgan todavía (retención pendiente).

**Cómo se consume:** `audit` lee su cola (`EVENTS_QUEUE_URL`, suscrita a los tópicos `origination` y `claims` con entrega *raw*) con
sondeo largo (`pkg/events/sqsconsumer`). El manejador es **idempotente por `subject`** (tabla `holder_link`, *append-only*): el mismo
evento otra vez no cambia nada; un documento distinto para el mismo `subject` conserva el primero y deja un aviso «holder link conflict»
sin el documento. Los tipos de evento que `audit` aún no consume se borran sin efecto. Un mensaje que no se puede procesar (sobre o
*payload* inválido, base caída) no se borra: reaparece al vencer la visibilidad y, tras `maxReceiveCount` (5), pasa a la DLQ de la cola.
Mientras el vínculo no llega, `GET /v1/consents` responde `403 holder_not_linked`.

## `PartnerCredentialsIssued`, `PartnerCredentialsRotated` y `PartnerCredentialsRevoked` (HU-W3.1-01)

Los escribe `partners` **en la misma transacción** que el cambio de la credencial: la aprobación del socio (`Issued`), la rotación
(`Rotated`) y la revocación de la credencial anterior cuando vence su periodo de transición (`Revoked`, la hace el trabajador de
`partners`). Contrato de las rutas: [`docs/contratos/HU-W3.1-01-api.md`](../contratos/HU-W3.1-01-api.md).

**Sobre:** el común de `pkg/events.Envelope`, con `source` = `partners`, `event_version` = `1` y `event_id` determinista por hecho:
`partner-credentials-issued-<credential_id>`, `partner-credentials-rotated-<credential_id>` (la credencial nueva) y
`partner-credentials-revoked-<credential_id>` (la revocada). `occurred_at` es el instante del cambio.

**Payload** (los tres tipos; minimización: nunca el secreto ni su pista):

| Campo | Descripción |
|---|---|
| `partner_id`, `partner_code`, `partner_name` | Socio (`ptn-…`), su código de *tenant* y su nombre |
| `credential_id`, `client_id` | Credencial a la que se refiere el hecho (la emitida, la nueva o la revocada) |
| `previous_credential_id`, `previous_client_id`, `previous_expires_at` | Solo en `Rotated`: la credencial anterior y el fin de su transición |
| `grace_period_hours` | Solo en `Rotated` |
| `reason` | `approval` (`Issued`), `rotation` (`Rotated`), `grace_period_ended` (`Revoked`) |
| `quota` | `{daily_limit, rate_limit, burst_limit}` vigente del socio |
| `actor` | `{type, id, label}`: `operations` con el `sub` y el correo o usuario del token del analista, o `system` / `partners` / `Sistema` |
| `occurred_at` | Instante del cambio (UTC) |

**Cómo se publica:** igual que `CustomerLinked`: tabla `outbox_event` de la base de `partners`, relevo con *lease* y tópico `partners`
(`EVENTS_TOPIC_ARN`). Sin `EVENTS_TOPIC_ARN` los eventos quedan en el *outbox*.

**Cómo se consume:** la cola de `audit` también se suscribe al tópico `partners`. El manejador es **idempotente por `event_id`** (tabla
`partner_credential_record`, *append-only*, `UNIQUE (event_id)`): el mismo evento otra vez no cambia nada. Un *payload* inválido es error
y el mensaje termina en la DLQ tras los reintentos.

## `ParametricPayoutCompleted` (HU-M3.1-01)

Lo publica `claims` (motor paramétrico, HU-M2.4-01) en su tópico, con *outbox*, **cuando la pasarela confirma el pago automático**: la
misma sentencia que registra el pago confirmado escribe el evento en `outbox_event`, así que se publica si y solo si el pago se confirmó.
El instante de esa confirmación (`paid_at`) es el inicio de los 60 s del criterio 1. Lo consume `notifications` desde su cola (suscrita al tópico `claims`,
entrega *raw*, DLQ con 5 recepciones).

**Sobre:** el común de `pkg/events.Envelope`, con `event_id` = `parametric-payout-completed-<payout_id>`, `event_type` =
`ParametricPayoutCompleted`, `event_version` = `1`, `source` = `claims` y `occurred_at` = `paid_at`.

**Payload:**

| Campo | Descripción |
|---|---|
| `payout_id` | Identifica el pago; clave de idempotencia del consumidor (una notificación por pago, `ntf-<payout_id>`) |
| `claim_id`, `policy_id` | Siniestro paramétrico (su número de radicado `SIN-<año>-<5 dígitos>`, el que ve el asegurado en M10b) y póliza que lo pagó |
| `subject` | `sub` de la cuenta del asegurado: el destinatario del centro y del *push* |
| `amount_minor`, `currency` | Monto pagado en unidades menores (centavos) y moneda ISO 4217 |
| `trigger` | `parameter` (`flight_delay`…), `event_reference` (el vuelo `AV-8421`), `threshold`, `observed`, `unit` (`minutes`…), `observed_at` |
| `payment_method` | `brand` y `last4`; nunca el número ni el token. En el pago de `claims`, `brand` es el banco de la cuenta de la indemnización y `last4`, sus últimos cuatro dígitos |
| `idempotency_key` | Clave con la que se hizo el pago (HA-D02) |
| `paid_at` | Confirmación del pago (UTC); si falta, el consumidor usa `occurred_at` |

**Cómo se consume:** idempotente por `payout_id`; el mismo evento otra vez no crea otra notificación ni otro *push*. La notificación y una
entrega por cada dispositivo del asegurado con permiso se escriben en una transacción; un trabajador envía los *push* con la misma clave de
idempotencia y los reintenta hasta 24 h. Varios pagos el mismo día son notificaciones distintas (criterio 6). Un *payload* inválido o la base
caída no borran el mensaje: reaparece al vencer la visibilidad y termina en la DLQ.

Con el motor de `claims`, `event_id` = `parametric-payout-completed-ppo-<id del siniestro>`, `payout_id` = `ppo-<id del siniestro>` (un pago por siniestro)
e `idempotency_key` = `indemnizacion-<id del siniestro>`, la clave del pago en la pasarela (HU-W2.2-03).

## `ParametricTriggerObserved` (HU-M2.4-01)

Lo publica la telemetría (el *stub* `iot`, todavía reservado; hoy `tests/e2e/hu-m2.4/nube.sh` en la nube y `app-local.sh --payout` en
local) en el tópico **`iot`**. Lo consume `claims` desde su cola (suscrita solo al tópico `iot`, entrega *raw*, DLQ con 5 recepciones).

**Sobre:** `event_id` = `parametric-trigger-observed-<observation_id>`, `event_type` = `ParametricTriggerObserved`, `event_version` = `1`,
`source` = `iot` y `occurred_at` = el instante de la observación.

**Payload:**

| Campo | Descripción |
|---|---|
| `observation_id` | Identifica la observación; clave de idempotencia del consumidor. Si falta, se usa `event_id` |
| `subject` | `sub` de la cuenta del asegurado, titular de la póliza |
| `policy_id` | Póliza paramétrica a la que la telemetría ya asoció la observación (la de Viaje, `VJ-…`) |
| `parameter` | `flight_delay` (el único con regla en `parametric-rules-v1`) |
| `event_reference` | Lo observado, por ejemplo el vuelo `AV-8421` |
| `observed`, `unit` | El valor observado y su unidad (`minutes`); obligatorio |
| `observed_at` | Instante de la observación (UTC); si falta, se usa `occurred_at` |

**Cómo se consume:** el motor lee la póliza y la cuenta del asegurado en el núcleo de pólizas y aplica `parametric-rules-v1`: un retraso de
**180 minutos o más** en una póliza de Viaje vigente que cubre `flight_delay` paga **$ 180.000 COP** (como máximo el valor asegurado, sin
deducible). En una sola transacción radica el siniestro (canal `parametric`), registra la resolución automática con su disparador (parámetro,
umbral, valor observado) y la aprobación con su pago pendiente. El trabajador de pagos de HU-W2.2-03 lo paga a la pasarela y la
confirmación publica `ParametricPayoutCompleted`. Es **idempotente por `observation_id`**: la misma observación otra vez no radica un segundo
siniestro. Una observación que no paga (bajo el umbral, póliza no vigente, parámetro no cubierto, póliza o asegurado que el núcleo no conoce)
se descarta y queda en el log como `parametric observation does not pay`. Un *payload* inválido o el núcleo caído no borran el mensaje:
reaparece al vencer la visibilidad y termina en la DLQ.

## `ClaimStageChanged` (HU-M3.3-01)

Lo publica `claims` en su tópico, con *outbox*. El historial de estados (`claim_status_change`) registra cada cambio, lo haga quien lo haga
(la revisión del asesor, la decisión de operaciones, el pago); `internal/stages` lo mira cada 2 s y escribe el evento de cada cambio que el
asegurado espera. Lo consume `notifications` desde su cola (la misma del pago paramétrico, suscrita al tópico `claims`).

**Qué cambios lo producen:** la entrada a evaluación (`in_evaluation`), la aprobación viniendo de evaluación (`approved`; la vuelta a
`approved` al reintentar un pago con novedad no es otra aprobación) y el pago (`paid`). **No** lo producen la radicación, la devolución, el
rechazo ni la novedad del pago, ni los siniestros paramétricos (canal `parametric`), que ya avisan con `ParametricPayoutCompleted`. Los
cambios anteriores a la migración `0012` de `claims` son historia y no se avisan.

**Sobre:** `event_id` = `claim-stage-changed-<change_id>` (uno por cambio), `event_type` = `ClaimStageChanged`, `event_version` = `1`,
`source` = `claims` y `occurred_at` = `changed_at`.

**Payload:**

| Campo | Descripción |
|---|---|
| `change_id` | Identifica el cambio en el historial; clave de idempotencia del consumidor (una notificación por cambio, `ntf-stage-<change_id>`) |
| `claim_id` | Id del siniestro: con él la app abre la línea de tiempo (`GET /v1/claims/{claim_id}`) |
| `filing_number` | Número de radicado `SIN-<año>-<5 dígitos>`, el que ve el asegurado |
| `policy_id`, `subject` | Póliza del siniestro y `sub` de la cuenta del asegurado (el destinatario) |
| `stage`, `previous_stage` | La etapa a la que llegó (`in_evaluation`, `approved`, `paid`) y la anterior |
| `changed_at` | Cuándo llegó a la etapa (UTC): el inicio de los 60 s y la hora que muestra la notificación |
| `amount_minor`, `currency` | Solo en `approved` y `paid`: lo que recibe el asegurado (el monto aprobado menos el deducible), en centavos |
| `paid_at` | Solo en `paid`: la fecha del desembolso |

**Cómo se consume:** idempotente por `change_id`. La notificación y una entrega por cada dispositivo con permiso se escriben en una
transacción, y el *push* va en su propio grupo (`claim_stages`) con `claim_id` en `data`. Un *payload* inválido o la base caída no borran el
mensaje: reaparece y termina en la DLQ.

## `PolicyExpiryApproaching` (HU-M3.2-01)

Lo publica `origination` en su tópico, con *outbox*. Un barrido en proceso (`internal/expiryalert`, cada `EXPIRY_ALERT_INTERVAL_MS`, por
omisión 1 h, en cada tarea) mira las pólizas **activas** de las dos fuentes de la billetera: las de originación (`life`) y las del núcleo de
pólizas, consultadas **por titular vinculado** (cada cliente de originación con `cognito_sub`; el titular en el núcleo es ese `sub`). Lo
consume `notifications` desde su cola (la misma de los siniestros, suscrita también al tópico `origination`).

**Regla del hito (último cruzado):** `days_left` = días calendario entre hoy y la fecha de fin de la cobertura, ambas en Colombia
(America/Bogota). Con `days_left < 0` no hay aviso; si no, el hito es el menor de {30, 15, 5} que no sea menor que `days_left` (ninguno con
más de 30). Se emite solo si `(policy_id, fin del periodo, hito)` no se avisó antes (tabla `policy_expiry_notice`, escrita en la misma
transacción que el evento con `ON CONFLICT DO NOTHING`, así que varias tareas a la vez no duplican nada). Nunca salen varios a la vez: una
póliza vista por primera vez a 14 días recibe solo el de 15, y el de 30 ya no sale.

**Qué no lo produce:** una póliza de originación cuyo periodo ya tiene una fila `renewal` (aceptada, renovada o no renovada: el asegurado
ya actuó) o que ya tiene sucesora (`renewal_of`); una póliza del núcleo que no está `active`; una cobertura que ya terminó. Una renovación
en el núcleo extiende `valid_to`: el periodo nuevo empieza sus hitos de cero. Si el núcleo falla para un titular, ese titular se salta y se
reintenta en el siguiente barrido.

**Sobre:** `event_id` = `policy-expiry-approaching-<policy_id>-<fin del periodo en Colombia, AAAA-MM-DD>-<milestone_days>`, `event_type` =
`PolicyExpiryApproaching`, `event_version` = `1`, `source` = `origination` y `occurred_at` = `detected_at`.

**Payload:**

| Campo | Descripción |
|---|---|
| `policy_id` | Póliza (la de originación o el número del núcleo, p. ej. `AU-2025-118204`) |
| `subject` | `sub` de la cuenta del asegurado: el destinatario |
| `source` | `origination` o `policy_core` |
| `line` | `life`, `auto`, `home` o `travel` |
| `product`, `insured_object` | Producto y objeto asegurado; `null` cuando la fuente no los nombra (las de vida de originación) |
| `coverage_end` | Fin de la cobertura (RFC 3339, UTC), tal como lo guarda o devuelve la fuente: la fecha exacta del aviso |
| `milestone_days` | Hito alcanzado: `30`, `15` o `5` |
| `days_left` | Días que faltaban al detectarlo (`>= 0`) |
| `detected_at` | Cuándo se detectó (UTC) |

**Cómo se consume:** idempotente por póliza, periodo e hito (notificación `ntf-expiry-<policy_id>-<AAAAMMDD>-<milestone_days>`, de tipo
`policy_expiry`). La notificación y una entrega por cada dispositivo con permiso se escriben en una transacción; el *push* va en el grupo
`policy_expiry` con `policy_id` en `data`. Un *payload* inválido o la base caída no borran el mensaje: reaparece y termina en la DLQ.
Contrato para el móvil: [`docs/contratos/HU-M3.2-01-api.md`](../contratos/HU-M3.2-01-api.md).

Los esquemas de los demás eventos se agregarán aquí al implementarlos. Los consumidores exactos pueden cambiar; confirmar en la Wiki ([Arquitectura final](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Arquitectura_final)).
