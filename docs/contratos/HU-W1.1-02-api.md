# Contrato de API — HU-W1.1-02 · Bandeja de solicitudes enrutadas a decisión humana

> **Fuente única para el front (Angular).** Generado a partir del código real del servicio `origination`
> (`internal/adapters/http/review.go`, `internal/underwriting`, `internal/adapters/memory/review_queue.go`,
> `internal/adapters/email/review_notifier.go`, `cmd/origination/main.go`) y de respuestas **capturadas del binario en ejecución**
> el 2026-10-17 (Cognito local de Moto con verificación JWT RS256 real de `pkg/security`, cobro de HA-D02 con PostgreSQL y el stub de
> correo de HU-W1.3-03). Los identificadores y las fechas de los ejemplos son los de esa ejecución. Contrato formal:
> `services/origination/api/openapi.yaml` (versión 0.8.0).

---

## 1. Resumen del flujo de punta a punta

```
Asegurado (embebido)                 origination                                         Asesor (portal)
────────────────────                 ───────────────────────────────────────────          ─────────────────────────────
POST /v1/offers/{id}/acceptance ──▶ suscripción enruta a revisión ─▶ in_review
                                     └─ solicitud pendiente en la bandeja ◀──────────── GET /v1/review-requests (S4)
                                                                                        GET /v1/review-requests/{id} (S5)
                                     decisión registrada (append-only) ◀─────────────── POST /v1/review-requests/{id}/decision
                                     ├─ accept ─▶ aceptación registered aprobada ─▶ issuance_pending (cobro HA-D02)
                                     ├─ adjust ─▶ oferta adj-{id} (+p %, 24 h) y aceptación adjusted ─▶ correo «nueva oferta»
                                     │            el asegurado acepta adj-{id} con el mismo POST ─▶ aprobada sin volver a la bandeja
                                     └─ reject ─▶ aceptación rejected (caso cerrado, sin cobro) ─▶ correo «no fue aprobada»
GET /v1/acceptances/{id} (E6b/E6c) ◀── objeto review con el resultado y la nueva oferta
```

| Criterio | Cubierto | Cómo |
|---|---|---|
| CA-1 La solicitud enrutada aparece en la bandeja con solicitante, oferta, motivo, llegada y estado pendiente | Sí | `GET /v1/review-requests` (por defecto `status=pending`) con `applicant`, `offer`, `reason`, `received_at`, `status` |
| CA-2 Tres decisiones; observaciones obligatorias al ajustar o rechazar | Sí | `POST …/decision` con `type` `accept` \| `adjust` \| `reject`; `400 observations_required` si faltan |
| CA-3 El flujo continúa solo según el tipo y el caso queda decidido | Sí | accept → emisión; adjust → oferta nueva que se acepta por el mismo `POST` de HU-W1.3-01; reject → cierre y correo al cliente. El caso pasa a `decided` y sale de pendientes |
| CA-4 Registro auditable con asesor, fecha y hora, tipo y observaciones | Sí, salvo reinicio | `decision` con `advisor` (`sub` y `email` del token), `decided_at`, `type`, `observations`, `premium_adjustment_pct` y `adjusted_offer`; *append-only*. **Se pierde si el servicio se reinicia** (memoria, §8) |
| CA-5 Filtro por estado y orden por llegada | Sí | `?status=pending\|decided&order=asc\|desc` (y `reason` opcional) |

**Dos asesores a la vez (corrección del 2026-10-17):** la decisión se registra primero con un *claim* atómico y solo quien lo gana
produce efectos. La oferta ajustada se crea después, con las condiciones de la decisión guardada; el otro asesor recibe `409`. Así la
oferta que el cliente acepta (y por la que se le cobra), la decisión, `review.adjusted_offer` (E6b) y el correo coinciden siempre.

---

## 2. Endpoints

Solo el rol **`advisor`** (grupo de Cognito) usa estas rutas; cualquier otro rol recibe `403` y sin token `401`. Todas responden con
`Cache-Control: no-store`.

### Cabeceras comunes

| Cabecera | Obligatoria | Valor |
|---|---|---|
| `Authorization` | Sí | `Bearer <ID token de Cognito>` (el **ID token**, no el access token) |
| `Content-Type` | Solo en `POST` | `application/json` |
| `Idempotency-Key` | **No se usa** | Un caso tiene una sola decisión: repetir el `POST` devuelve `409` (o completa la continuación pendiente, §3) |

### 2.1 `GET /v1/review-requests` — listar la bandeja (S4)

| Parámetro | Obligatorio | Valores | Defecto |
|---|---|---|---|
| `status` | No | `pending` \| `decided` | `pending` |
| `reason` | No | `provisional_offer` \| `high_risk_score` (los motivos del dominio) | todos |
| `order` | No | `asc` (la más antigua primero) \| `desc` | `asc` |

Respuesta `200`: `{"items":[ReviewRequest…],"counts":{"pending":n,"decided":m}}`. Los **conteos respetan `reason` e ignoran
`status`**, así que sirven para las dos pestañas. Sin paginación en este sprint. Ejemplo real (`?status=decided&reason=high_risk_score`):
`"counts": {"pending": 0, "decided": 1}`.

### 2.2 `GET /v1/review-requests/{acceptance_id}` — abrir un caso (S5)

`200` con el `ReviewRequest`; aquí `offer` trae además `mode` y `expires_at` (`null` si la oferta no se pudo leer). Ejemplo real:

```json
{
  "acceptance_id": "6af8ce61-2424-4ed2-8375-57eaf9f9ee67",
  "offer_id": "demo-review-1792212135",
  "customer_id": "ff5a40b9-19f8-413a-8665-f73d9196fc6d",
  "applicant": {"full_name": "ff5a40b9-19f8-413a-8665-f73d9196fc6d", "document_id": "demo-document", "email": "asegurado.local@example.com"},
  "offer": {"premium": 89500, "currency": "COP", "mode": "provisional", "expires_at": "2026-10-18T04:42:19.9766934Z"},
  "reason": "provisional_offer",
  "received_at": "2026-10-17T04:42:19.9766934Z",
  "status": "pending",
  "decision": null
}
```

### 2.3 `POST /v1/review-requests/{acceptance_id}/decision` — registrar la decisión (S5 → S4b)

| Campo | Tipo | Obligatorio | Reglas |
|---|---|---|---|
| `type` | string | Sí | `accept` \| `adjust` \| `reject` |
| `observations` | string | Al ajustar o rechazar | No vacío tras recortar espacios; máximo 1 000 caracteres. Opcional al aceptar |
| `premium_adjustment_pct` | número | Solo al ajustar | Recargo: mayor que 0 y hasta 100, con máximo dos decimales. Prohibido al aceptar o rechazar |

Respuesta `201` con el caso decidido. Ejemplo real (ajustar 10 %):

```json
{
  "acceptance_id": "6af8ce61-2424-4ed2-8375-57eaf9f9ee67",
  "offer_id": "demo-review-1792212135",
  "customer_id": "ff5a40b9-19f8-413a-8665-f73d9196fc6d",
  "applicant": {"full_name": "ff5a40b9-19f8-413a-8665-f73d9196fc6d", "document_id": "demo-document", "email": "asegurado.local@example.com"},
  "offer": {"premium": 89500, "currency": "COP"},
  "reason": "provisional_offer",
  "received_at": "2026-10-17T04:42:19.9766934Z",
  "status": "decided",
  "decision": {
    "decision_id": "a37c997a-e061-4042-8621-2d365ed021bc",
    "type": "adjust",
    "advisor": {"id": "413ed637-0f56-4b36-b181-70fe3ee8e578", "email": "asesor.local@example.com"},
    "decided_at": "2026-10-17T04:42:20.022742Z",
    "observations": "Ingreso declarado sin verificar",
    "premium_adjustment_pct": 10,
    "adjusted_offer": {"offer_id": "adj-6af8ce61-2424-4ed2-8375-57eaf9f9ee67", "premium": 98450, "currency": "COP", "expires_at": "2026-10-18T04:42:20.022742Z"},
    "notification": {"channel": "email", "status": "pending", "attempts": 0, "sent_at": null}
  }
}
```

Un rechazo real tiene `"type": "reject"`, `"premium_adjustment_pct": null`, `"adjusted_offer": null` y `notification` igual; una
aceptación, además, `"notification": null` (el correo de emisión de HU-W1.3-03 ya avisa al cliente).

### 2.4 Objetos

**`ReviewRequest`**

| Campo | Tipo | Nulo | Descripción |
|---|---|---|---|
| `acceptance_id` | string (UUID v4) | No | Aceptación enrutada; es también el **número de la solicitud** (E6 lo muestra así) |
| `offer_id` | string | No | Oferta implicada |
| `customer_id` | string | No | Dueño de la oferta (claim `sub`) |
| `applicant.full_name` / `document_id` / `email` | string | No | Datos del solicitante que existen en el dominio (ver §8) |
| `offer.premium` / `offer.currency` | número / string | No | Prima mensual y moneda de la oferta enrutada |
| `offer.mode` / `offer.expires_at` | `normal` \| `provisional` / fecha-hora | **Sí** | Solo en el detalle (§2.2) |
| `reason` | string | No | `provisional_offer` \| `high_risk_score`; el front debe mostrar un texto genérico para un motivo desconocido |
| `received_at` | fecha-hora | No | Llegada a la bandeja |
| `status` | `pending` \| `decided` | No | |
| `decision` | `HumanDecision` | **Sí** | `null` mientras está pendiente |

**`HumanDecision`** (registro *append-only*, CA-4)

| Campo | Tipo | Nulo | Descripción |
|---|---|---|---|
| `decision_id` | string (UUID v4) | No | |
| `type` | `accept` \| `adjust` \| `reject` | No | |
| `advisor.id` / `advisor.email` | string | No | `sub` y `email` del token del asesor |
| `decided_at` | fecha-hora | No | |
| `observations` | string | No | Puede ser `""` en una aceptación |
| `premium_adjustment_pct` | número | **Sí** | Solo en `adjust` |
| `adjusted_offer` | `{offer_id, premium, currency, expires_at}` | **Sí** | Solo en `adjust`: `offer_id = adj-{acceptance_id}`, prima con el recargo (2 decimales), vigencia de 24 h desde la decisión |
| `notification` | `{channel, status, attempts, sent_at}` | **Sí** | Correo al cliente del ajuste o el rechazo. `status`: `pending` (programado o reintentando: 1 s, 2 s, … hasta 15 s), `sent`, `failed` (tras 12 intentos; la decisión no cambia). `attempts` = envíos fallidos. `null` en `accept` o sin `EMAIL_URL`. **No se muestra en S5** (evidencia por API) |

**Cambio aditivo en `Acceptance` (HU-W1.3-01):** `status` suma `adjusted` y `rejected`, y aparece el objeto `review`
(`null` hasta que un asesor decide):

```json
"status": "adjusted",
"review": {
  "outcome": "adjust",
  "decided_at": "2026-10-17T04:42:20.022742Z",
  "adjusted_offer": {"offer_id": "adj-6af8ce61-2424-4ed2-8375-57eaf9f9ee67", "premium": 98450, "currency": "COP", "expires_at": "2026-10-18T04:42:20.022742Z"}
}
```

En `rejected`: `"review": {"outcome": "reject", "decided_at": "…", "adjusted_offer": null}`. Tras un `accept` la aceptación vuelve a
`registered` con `underwriting.outcome = "approved"` (y `reason: null`) y `review.outcome = "accept"`, y en segundos pasa a
`issuance_pending`.

---

## 3. Códigos HTTP y catálogo de errores

| Endpoint | Éxito | Errores posibles |
|---|---|---|
| `GET /v1/review-requests` | `200` | `400 invalid_request`, `401`, `403`, `503 review_unavailable` |
| `GET /v1/review-requests/{acceptance_id}` | `200` | `400`, `401`, `403`, `404 review_request_not_found`, `503 review_unavailable` |
| `POST /v1/review-requests/{acceptance_id}/decision` | `201` | `400 invalid_request` · `observations_required` · `premium_adjustment_invalid`, `401`, `403`, `404 review_request_not_found`, `409 review_request_already_decided`, `503 decision_not_registered` · `decision_continuation_pending` · `review_unavailable` |

| HTTP | `error` | Cuándo | Ejemplo real | Acción del front |
|---|---|---|---|---|
| 400 | `invalid_request` | Filtro inválido (`status`, `order`, `reason`), cuerpo no JSON, `type` desconocido, observaciones de más de 1 000 caracteres o id > 128 | `{"error":"invalid_request","message":"status must be pending or decided, order asc or desc and reason one of the routing reasons"}` | Error de programación: registrar y mostrar un error genérico |
| 400 | `observations_required` | Ajustar o rechazar sin observaciones (o solo espacios) | `{"error":"observations_required","message":"observations are required to adjust or reject"}` | Marcar el campo Observaciones con su error |
| 400 | `premium_adjustment_invalid` | Falta el porcentaje al ajustar, viene al aceptar o rechazar, o está fuera de 0 < p ≤ 100 / con más de dos decimales | `{"error":"premium_adjustment_invalid","message":"invalid premium adjustment: premium_adjustment_pct is required to adjust"}` | Marcar el campo «Ajuste de prima (%)» |
| 401 | `unauthorized` | Sin token o token inválido/vencido | `{"error":"unauthorized","message":"a bearer token is required"}` | Renovar la sesión |
| 403 | `forbidden` | Rol distinto de `advisor` | `{"error":"forbidden","message":"the caller is not allowed to perform this operation"}` | «Acceso no autorizado» (el *guard* ya lo evita) |
| 404 | `review_request_not_found` | La aceptación no tiene solicitud en la bandeja (o el servicio se reinició, §8) | `{"error":"review_request_not_found","message":"the review request does not exist"}` | «La solicitud no existe» + volver a la bandeja |
| 409 | `review_request_already_decided` | El caso ya tiene decisión (otro asesor se adelantó o se repitió el `POST`) | `{"error":"review_request_already_decided","message":"the review request is already decided"}` | Recargar el detalle y mostrar la vista decidida |
| 503 | `decision_not_registered` | No se pudo registrar la decisión; nada cambió | `{"error":"decision_not_registered","message":"the decision could not be registered; retrying is safe"}` | «No pudimos registrar la decisión» + Reintentar |
| 503 | `decision_continuation_pending` | La decisión quedó registrada pero el flujo no continuó aún (p. ej. no se pudo crear la oferta ajustada o mover la aceptación) | `{"error":"decision_continuation_pending","message":"the decision is registered but the origination flow did not continue yet; retrying is safe"}` | Reintentar el **mismo** `POST`: completa la continuación **con la decisión guardada** y responde `409` con el caso ya decidido |
| 503 | `review_unavailable` | No se pudo leer la bandeja | `{"error":"review_unavailable","message":"the review requests could not be read; retry later"}` | Error con Reintentar |

---

## 4. Estados

```
caso:        pending ──(decisión)──▶ decided          (final; un solo registro, sin modificaciones)
aceptación:  in_review ─ accept ─▶ registered ─▶ issuance_pending   (procesador de HU-W1.3-01, cobro de HA-D02)
                       ─ adjust ─▶ adjusted   (final; el cliente acepta adj-{id}: aceptación nueva que se aprueba sola)
                       ─ reject ─▶ rejected   (final; sin cobro)
```

---

## 5. Guía por pantalla

| Pantalla | Datos |
|---|---|
| **S4** Bandeja | Pestañas «Pendientes (`counts.pending`)» / «Decididas (`counts.decided`)» → `status`; selector de motivo → `reason`; orden por llegada → `order`. Columnas: `acceptance_id`, `applicant.full_name`, `offer.premium`/`currency`, `reason`, `received_at`, «Revisar» → S5. En «Decididas», además `decision.type`, `decision.advisor.email`, `decision.decided_at` |
| **S4b** «Decisión registrada» | Tras el `201` del `POST`: volver a la bandeja con el aviso |
| **S5** Detalle | Número (`acceptance_id`), estado, solicitante (`applicant`), llegada, oferta (`offer_id`, `premium`, `currency`, `expires_at`, `mode`), motivo; formulario: `type`, «Ajuste de prima (%)» (solo `adjust`), observaciones (obligatorias en `adjust`/`reject`). Caso decidido: vista de solo lectura con todo `decision` |
| **E6b** «Solicitud ajustada» (embebido) | `GET /v1/acceptances/{id}` con `status=adjusted`: `review.decided_at`, `review.adjusted_offer.premium`/`currency`/`expires_at`; «Ver la nueva oferta» → `/embebido/oferta/{review.adjusted_offer.offer_id}` |
| **E6c** «Solicitud rechazada» (embebido) | `status=rejected`: `review.decided_at`; «Continuar con mi crédito» |

---

## 6. Escenarios de prueba

| Escenario | Cómo obtenerlo |
|---|---|
| Solicitud pendiente por perfil provisional | El asegurado acepta `demo-review-<sufijo>` → `in_review` con `provisional_offer` |
| Solicitud pendiente por riesgo alto | El asegurado acepta `demo-highrisk-<sufijo>` (puntaje 0,9) → `in_review` con `high_risk_score` |
| Ajuste | Decidir `adjust` (p. ej. 10 % → prima 98 450 COP) → la aceptación queda `adjusted`; aceptar `adj-<acceptance_id>` con el mismo `POST` → `issuance_pending` |
| Rechazo | Decidir `reject` con observaciones → `rejected` |
| Aceptar | Decidir `accept` → `issuance_pending` |
| Correo con fallas | Stub de correo con `FAIL_FIRST=2` → `decision.notification.attempts = 2` y un solo mensaje en `GET /messages?to=` |

---

## 7. Cómo correrlo en local

Igual que HA-D02 (`services/origination/README.md` → «Desarrollo»): Cognito de Moto (`tests/security/ha-s01/local`), PostgreSQL, la
pasarela y el correo, y originación con `AUTH_*`, `PAYMENT_GATEWAY_URL`, `DATABASE_URL`, `EMAIL_URL`, `DOCUMENTS_DIR` y
`DOCUMENT_LINK_SECRET`. Sin `EMAIL_URL` la bandeja funciona igual, pero no se envían los correos del ajuste y el rechazo (lo dice el
log al arrancar). Los correos que recibiría el asegurado se ven en `GET http://localhost:<puerto del stub>/messages?to=<correo>`.

Correos reales capturados (sin enlace y sin las observaciones del asesor; clave `review-outcome:<acceptance_id>`):

- Ajuste — asunto «Revisamos tu solicitud de seguro: tienes una nueva oferta»: «Un asesor de Solventa revisó tu solicitud
  6af8ce61-… el 17/10/2026 a las 04:42 (UTC) y ajustó la prima. Nueva prima mensual: $ 98.450 COP. Vigencia de la nueva oferta:
  hasta el 18/10/2026 a las 04:42 (UTC) (24 horas). Tu aceptación anterior no se cobra…».
- Rechazo — asunto «Tu solicitud de seguro no fue aprobada»: «… y el riesgo no fue aceptado. No se realizó ningún cobro. Puedes
  continuar con tu crédito y el banco te indicará los siguientes pasos…».

---

## 8. Qué está simulado y qué está pendiente

| Pieza | Estado hoy | Efecto visible para el front | Pendiente de |
|---|---|---|---|
| Bandeja y decisiones | **En memoria** (con *claim* atómico) | Se pierden al reiniciar el servicio, igual que las aceptaciones | PostgreSQL de origination |
| Registro de auditoría de largo plazo | En origination, *append-only* | Se consulta con estas rutas | HU-W4.1-01 (servicio `audit`) |
| Datos del solicitante | Los de `domain.Applicant` (nombre, documento, correo). Con las ofertas de demostración, el nombre es el `cognito:username` (en Moto, el `sub`) y el documento `demo-document` | Edad, ciudad, ingresos, valor asegurado y consentimiento de S5 no existen | HU-W1.2-03, HU-W1.2-01 |
| Detalle de la oferta ajustada en E3 | **No hay endpoint de detalle de oferta** | E3 muestra la oferta de demostración; E6b sí muestra la prima ajustada (`review.adjusted_offer`) | HU-W1.2-03 |
| Motor de suscripción | Reglas provisionales de HU-W1.3-01; la oferta ajustada se aprueba siempre (decisión de un asesor) | — | Motor de suscripción |
| Correo | Proveedor simulado (`stubs/email`) | — | Proveedor real |
| SMS y push | No existen | — | — |
| Paginación | No hay | La lista trae todos los casos del filtro | Cuando el volumen lo pida |
| Rutas en el API Gateway (AWS) | No registradas | Solo se puede probar en local | Infraestructura |
