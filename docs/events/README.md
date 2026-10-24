# Catálogo de eventos

Los servicios publican hechos de negocio en Amazon SNS (un tópico por dominio) y cada consumidor tiene su cola SQS con DLQ. Reglas: publicación con *transactional outbox*, consumidores idempotentes, entrega *at-least-once* sin orden estricto.

| Evento | Nombre en la Wiki | Publica | Consume | Propósito |
|---|---|---|---|---|
| `PolicyIssued` | `PolizaEmitida` | origination | origination (documento contractual, HU-W1.3-03), audit, notifications | Póliza emitida tras aceptación y cobro; es la «cobertura activada» que dispara el documento contractual y la notificación |
| `ChargeCompleted` | `CobroRealizado` | origination | audit | Cobro confirmado (idempotente) |
| `OfferGenerated` | `OfertaGenerada` | origination | audit | **Publicación pendiente (HU-W4.1-01).** Oferta personalizada generada (HU-W1.2-03); hoy solo queda en la tabla `offer` |
| `OfferRequoted` | `OfertaRecotizada` | origination | audit, notifications | **Publicación pendiente (HU-W4.1-01).** Una oferta provisional se reemplazó por una normal al volver la fuente (HU-W1.2-03); hoy el aviso al cliente es el correo `offer-requoted:<id>` |
| `ClaimApproved` | `SiniestroAprobado` | claims | notifications, audit | Dispara notificación y pago paramétrico |

**HU-W1.3-01 (aceptación de la oferta) no publica ni consume eventos:** la aceptación se persiste en origination (PostgreSQL con `DATABASE_URL`, fix C5) y la suscripción, el envío a la bandeja del asesor (HU-W1.1-02) y el inicio de la emisión ocurren en proceso. `PolicyIssued` y `ChargeCompleted` en SNS siguen pendientes de HU-W1.3-02 (con el *outbox* de `pkg/events`). Si la bandeja o la auditoría de la decisión de suscripción pasan a otro servicio, el evento correspondiente se registrará aquí.

**HU-W1.1-02 (bandeja de decisión humana) no publica ni consume eventos:** la bandeja recibe en proceso lo que la suscripción enruta, la decisión del asesor se guarda *append-only* en origination (tabla `human_decision`, fix C5) y la continuación (emisión, oferta ajustada o rechazo) ocurre en proceso. El correo que avisa al cliente el ajuste o el rechazo sale de origination por el mismo canal que el de HU-W1.3-03 (`pkg/adapters/email`, con un trabajador que reintenta y su propia clave de idempotencia `review-outcome:<acceptance_id>`), sin eventos. Si la auditoría de largo plazo (HU-W4.1-01) o las notificaciones pasan a otro servicio, el evento correspondiente (p. ej. `HumanDecisionRecorded`, con *outbox*) se registrará aquí.

**HU-W1.2-01 (consentimiento explícito) no publica ni consume eventos:** `audit` registra el consentimiento en su propia base y `origination` lo verifica con una llamada HTTP síncrona (`GET /v1/audit/consent-verifications`), no con mensajes.  La revocación (fuera de alcance) no existe todavía; si llega a publicar un evento, se registrará aquí.

**HU-W1.2-03 (oferta personalizada) no publica ni consume eventos todavía:** la oferta se persiste en `origination` y el reemplazo de una oferta provisional y su correo ocurren en proceso. `OfferGenerated` y `OfferRequoted` quedan **registrados** para cuando la auditoría de largo plazo (HU-W4.1-01) los necesite, con *outbox* de `pkg/events`. Payload previsto:

| Evento | Campos |
|---|---|
| `OfferGenerated` | `event_id`, `offer_id`, `customer_id`, `consent_id`, `partner_id`, `application_id`, `channel`, `mode`, `provisional_reason`, `profile_source`, `profile_category`, `rater_version`, `premium`, `currency`, `created_at`, `expires_at`. Nunca `score`, `rules` ni el documento |
| `OfferRequoted` | `event_id` (`offer-requoted:<offer_id>`), `offer_id` (la reemplazada), `new_offer_id`, `old_premium`, `new_premium`, `currency`, `requoted_at` |

**HU-W1.3-03 (documento contractual) consume la activación de la cobertura en proceso**, con `ports.CoverageActivationHandler` y un consumidor idempotente por póliza (el *outbox* y SNS/SQS llegan con `pkg/events`). **La publica en proceso el módulo `billing` de origination (HA-D02) solo cuando el cobro de la prima queda confirmado** (CA-4): el evento se arma con la foto guardada en `charge_attempt` (`event_id` `evt-coverage-activated-<acceptance_id>`, `policy_id` `pol-<acceptance_id>`), así que cada reintento de la publicación entrega exactamente el mismo evento; si el consumidor falla, se reintenta con espera creciente y sin ventana. Sin pasarela de pagos configurada nada activa coberturas. La asignación del nombre `PolicyIssued` a la «cobertura activada» está **por confirmar con el equipo**.

Payload del evento tal como lo consume el documento contractual (`domain.CoverageActivated`):

| Campo | Descripción |
|---|---|
| `event_id` | Identifica el hecho; una redelivery lo repite |
| `policy_id` | Póliza (clave de idempotencia); letras, dígitos, `-` y `_` |
| `acceptance_id`, `customer_id` | Aceptación de origen y asegurado dueño de la póliza |
| `holder` | `full_name`, `document_id`, `email` (canal de contacto de la notificación) |
| `premium`, `currency`, `insured_amount`, `coverages[]` | Condiciones económicas que imprime el documento |
| `activated_at`, `coverage_start`, `coverage_end` | Activación (inicio del plazo de 60 s) y vigencia |

Los esquemas de cada evento se agregarán aquí al implementarlos. Los consumidores exactos pueden cambiar; confirmar en la Wiki ([Arquitectura final](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Arquitectura_final)).
