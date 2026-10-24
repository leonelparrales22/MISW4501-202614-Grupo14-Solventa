# Catálogo de eventos

Los servicios publican hechos de negocio en Amazon SNS (un tópico por dominio) y cada consumidor tiene su cola SQS con DLQ. Reglas: publicación con *transactional outbox*, consumidores idempotentes, entrega *at-least-once* sin orden estricto.

| Evento | Nombre en la Wiki | Publica | Consume | Propósito |
|---|---|---|---|---|
| `PolicyIssued` | `PolizaEmitida` | origination | origination (documento contractual, HU-W1.3-03), audit, notifications | Póliza emitida tras aceptación y cobro; es la «cobertura activada» que dispara el documento contractual y la notificación |
| `ChargeCompleted` | `CobroRealizado` | origination | audit | Cobro confirmado (idempotente) |
| `OfferGenerated` | `OfertaGenerada` | origination | audit | **Publicación pendiente (HU-W4.1-01).** Oferta personalizada generada (HU-W1.2-03); hoy solo queda en la tabla `offer` |
| `OfferRequoted` | `OfertaRecotizada` | origination | audit, notifications | **Publicación pendiente (HU-W4.1-01).** Una oferta provisional se reemplazó por una normal al volver la fuente (HU-W1.2-03); hoy el aviso al cliente es el correo `offer-requoted:<id>` |
| `ClaimApproved` | `SiniestroAprobado` | claims | notifications, audit | Dispara notificación y pago paramétrico |
| `CustomerLinked` | `ClienteVinculado` | origination | audit | La cuenta del asegurado (`sub` de Cognito) quedó vinculada a su documento al terminar el registro (HU-M1.1-01); `audit` lo usa para reconocer al titular de sus consentimientos (HU-M1.3-01). **Primer evento publicado con *outbox*** |

**HU-W1.3-01 (aceptación de la oferta) no publica ni consume eventos:** la aceptación se persiste en origination (PostgreSQL con `DATABASE_URL`, fix C5) y la suscripción, el envío a la bandeja del asesor (HU-W1.1-02) y el inicio de la emisión ocurren en proceso. `PolicyIssued` y `ChargeCompleted` en SNS siguen pendientes de HU-W1.3-02 (con `pkg/outbox` y `pkg/events`). Si la bandeja o la auditoría de la decisión de suscripción pasan a otro servicio, el evento correspondiente se registrará aquí.

**HU-W1.1-02 (bandeja de decisión humana) no publica ni consume eventos:** la bandeja recibe en proceso lo que la suscripción enruta, la decisión del asesor se guarda *append-only* en origination (tabla `human_decision`, fix C5) y la continuación (emisión, oferta ajustada o rechazo) ocurre en proceso. El correo que avisa al cliente el ajuste o el rechazo sale de origination por el mismo canal que el de HU-W1.3-03 (`pkg/adapters/email`, con un trabajador que reintenta y su propia clave de idempotencia `review-outcome:<acceptance_id>`), sin eventos. Si la auditoría de largo plazo (HU-W4.1-01) o las notificaciones pasan a otro servicio, el evento correspondiente (p. ej. `HumanDecisionRecorded`, con *outbox*) se registrará aquí.

**HU-W1.2-01 (consentimiento explícito) no publica ni consume eventos:** `audit` registra el consentimiento en su propia base y `origination` lo verifica con una llamada HTTP síncrona (`GET /v1/audit/consent-verifications`), no con mensajes.  La revocación (fuera de alcance) no existe todavía; si llega a publicar un evento, se registrará aquí.

**HU-W1.2-03 (oferta personalizada) no publica ni consume eventos todavía:** la oferta se persiste en `origination` y el reemplazo de una oferta provisional y su correo ocurren en proceso. `OfferGenerated` y `OfferRequoted` quedan **registrados** para cuando la auditoría de largo plazo (HU-W4.1-01) los necesite, con `pkg/outbox` y `pkg/events` (se escriben en la misma tabla `outbox_event` de originación). Payload previsto:

| Evento | Campos |
|---|---|
| `OfferGenerated` | `event_id`, `offer_id`, `customer_id`, `consent_id`, `partner_id`, `application_id`, `channel`, `mode`, `provisional_reason`, `profile_source`, `profile_category`, `rater_version`, `premium`, `currency`, `created_at`, `expires_at`. Nunca `score`, `rules` ni el documento |
| `OfferRequoted` | `event_id` (`offer-requoted:<offer_id>`), `offer_id` (la reemplazada), `new_offer_id`, `old_premium`, `new_premium`, `currency`, `requoted_at` |

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

Los esquemas de los demás eventos se agregarán aquí al implementarlos. Los consumidores exactos pueden cambiar; confirmar en la Wiki ([Arquitectura final](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Arquitectura_final)).
