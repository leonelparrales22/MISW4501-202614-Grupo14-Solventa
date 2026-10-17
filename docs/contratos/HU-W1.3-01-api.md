# Contrato de API — HU-W1.3-01 · Aceptación de la oferta y disparo de la emisión

> **Fuente única para el front (Angular).** Generado a partir del código real del servicio `origination`
> (`internal/adapters/http/acceptance.go`, `internal/issuance`, `internal/adapters/memory`, `cmd/origination/main.go`) y de
> respuestas **capturadas del binario en ejecución** el 2026-10-11 (con verificación JWT RS256 real de `pkg/security`). Los
> identificadores y las fechas de los ejemplos son los que devolvió esa ejecución. Contrato formal: `services/origination/api/openapi.yaml`
> (versión 0.4.0).
>
> **Corrección del 2026-10-16 (regla provisional del puntaje):** la suscripción simulada enviaba al asesor los puntajes **menores
> a 0,4**, con el motivo `low_risk_score` y el escenario `demo-lowscore-*`, pero en Solventa el puntaje 0 es el **menor** riesgo.
> Ahora van al asesor los puntajes **mayores al umbral de revisión** (`UNDERWRITING_REVIEW_SCORE_THRESHOLD`, 0,8 por defecto), con
> el motivo `high_risk_score` y el escenario `demo-highrisk-*` (§2.4, §4, §5 y §8). Los ejemplos JSON capturados el 2026-10-11 no
> cambian.
>
> **Ampliación del 2026-10-17 (HU-W1.1-02, contrato 0.8.0):** la bandeja del asesor ya decide. La aceptación suma los estados
> `adjusted` y `rejected` y el objeto `review` (§2.4 y §4); el detalle de la bandeja está en
> [`HU-W1.1-02-api.md`](HU-W1.1-02-api.md). Es un cambio aditivo: los campos anteriores no cambian.

---

## 1. Resumen del flujo de punta a punta

```
Front (asegurado autenticado)                        origination
───────────────────────────────                      ───────────────────────────────────────────────────────────
Pantalla de oferta ─ «Aceptar» ──POST /v1/offers/{offer_id}/acceptance──▶ valida token, rol y dueño de la oferta
                                                     ├─ oferta vencida ─────────────▶ 422 offer_expired (no registra nada)
                                                     ├─ oferta ya aceptada ─────────▶ 200 aceptación existente
                                                     └─ oferta vigente: PERSISTE la aceptación ─▶ 202 status=registered
                                                                 │ (procesador asíncrono, en segundo plano)
                                                                 ▼
                                                        suscripción automática
                                                        ├─ aprueba ─────▶ inicia la emisión ─▶ status=issuance_pending
                                                        ├─ enruta ──────▶ bandeja del asesor ─▶ status=in_review
                                                        └─ falla ───────▶ sigue registered; reintenta solo (2 s, 4 s, … ≤ 60 s)
Pantalla de estado ◀──GET /v1/acceptances/{acceptance_id} (polling cada 2 s)──┘
```

**Qué cubre el backend de HU-W1.3-01:**

| Criterio | Cubierto | Cómo |
|---|---|---|
| CA-1 Aceptación persistida (oferta, fecha y hora, canal) antes de cualquier dependencia | Sí | `POST` guarda `offer_id`, `accepted_at`, `channel` y responde `202` antes de ejecutar la suscripción |
| CA-2 Suscripción aprueba → emisión inicia sin intervención | Sí | El procesador pasa a `issuance_pending` y llama al inicio de la emisión (simulado, ver §8) |
| CA-3 Enrutada a decisión humana → bandeja del asesor + cliente ve «en revisión» | Sí | Pasa a `in_review`; la solicitud (solicitante, oferta, motivo) se encola en la bandeja (simulada, ver §8) |
| CA-4 Oferta vencida → rechazo con motivo y opción de nueva oferta | Parcial | `422 offer_expired` con `expires_at`. **El endpoint para generar la nueva oferta no existe aún** (HU-W1.2-03) |
| CA-5 Falla posterior → la aceptación no se pierde y se reanuda sin repetir | Sí, salvo reinicio | Reintento automático con espera creciente; la aceptación se recupera por oferta. **Se pierde si el servicio se reinicia** (almacenamiento en memoria, ver §8) |

**Qué NO cubre:** mostrar el contenido de la oferta (prima, coberturas, vigencia) y generar ofertas (HU-W1.2-03), la decisión
del asesor sobre la solicitud en revisión (HU-W1.1-02), el cobro y la póliza emitida (HU-W1.3-02 / HA-D02).

---

## 2. Endpoints

Base URL: ver §7. Todas las rutas usan JSON (`Content-Type: application/json`) y devuelven `Cache-Control: no-store` en
**todas** sus respuestas (no cachear en el navegador ni en un interceptor).

### Cabeceras comunes

| Cabecera | Obligatoria | Valor |
|---|---|---|
| `Authorization` | Sí | `Bearer <ID token de Cognito>` (el **ID token**, no el access token) |
| `Content-Type` | Solo en `POST` | `application/json` |
| `Idempotency-Key` | **No se usa** | La idempotencia es por oferta: reenviar el `POST` de la misma oferta devuelve la misma aceptación |

### 2.1 `POST /v1/offers/{offer_id}/acceptance` — aceptar la oferta

- **Rol:** solo `insured` (asegurado) y solo el **dueño** de la oferta (`sub` del token = dueño).
- **Parámetro de ruta:** `offer_id` — string de 1 a 128 caracteres (codificar con `encodeURIComponent`).
- **Cuerpo:**

```json
{ "channel": "web" }
```

| Campo | Tipo | Obligatorio | Valores |
|---|---|---|---|
| `channel` | string | Sí | `web` \| `mobile` (el portal Angular envía siempre `web`) |

- **Respuesta `202 Accepted`** (aceptación nueva). Cabeceras: `Location: /v1/acceptances/{acceptance_id}`, `Cache-Control: no-store`.

```json
{
  "acceptance_id": "b310b02b-9820-4725-bded-586d263dd16e",
  "offer_id": "demo-active-002",
  "customer_id": "0f3c2a9e-1b7d-4c55-9a51-2f0d4e6b8c11",
  "channel": "web",
  "accepted_at": "2026-10-12T01:54:41.0909319Z",
  "status": "registered",
  "underwriting": null,
  "processing": {
    "attempts": 0,
    "last_failure_at": null,
    "next_attempt_at": "2026-10-12T01:54:41.0909319Z"
  },
  "updated_at": "2026-10-12T01:54:41.0909319Z"
}
```

- **Respuesta `200 OK`** (la oferta **ya** tenía aceptación: se devuelve la existente **en su estado actual**, aunque la oferta haya
  vencido después; nunca se crea otra). Misma forma y mismas cabeceras (`Location`). Ejemplo real al reenviar el `POST`:

```json
{
  "acceptance_id": "b310b02b-9820-4725-bded-586d263dd16e",
  "offer_id": "demo-active-002",
  "customer_id": "0f3c2a9e-1b7d-4c55-9a51-2f0d4e6b8c11",
  "channel": "web",
  "accepted_at": "2026-10-12T01:54:41.0909319Z",
  "status": "issuance_pending",
  "underwriting": { "outcome": "approved", "reason": null, "decided_at": "2026-10-12T01:54:41.0909319Z" },
  "processing": { "attempts": 0, "last_failure_at": null, "next_attempt_at": null },
  "updated_at": "2026-10-12T01:54:41.0909319Z"
}
```

> El `channel` del reenvío se ignora: la aceptación conserva el canal original.

### 2.2 `GET /v1/acceptances/{acceptance_id}` — consultar una aceptación (polling)

- **Roles:** `insured` (solo la suya), `advisor`, `operations`.
- **Parámetro de ruta:** `acceptance_id` (UUID devuelto por el `POST` o la cabecera `Location`).
- **Respuesta `200 OK`:** mismo objeto `Acceptance` (§2.4).

### 2.3 `GET /v1/offers/{offer_id}/acceptance` — consultar la aceptación de una oferta (reanudar)

- **Roles:** `insured` (solo la suya), `advisor`, `operations`.
- Sirve para **retomar el trámite** cuando el front solo conserva el `offer_id` (recarga, cambio de dispositivo, regreso al flujo).
- **Respuesta `200 OK`:** objeto `Acceptance`. **`404 acceptance_not_found`** si la oferta todavía no fue aceptada (mostrar el botón «Aceptar»).

### 2.4 Objeto `Acceptance`

| Campo | Tipo | Nulo | Descripción |
|---|---|---|---|
| `acceptance_id` | string (UUID v4) | No | Identificador de la aceptación |
| `offer_id` | string | No | Oferta aceptada |
| `customer_id` | string | No | Dueño de la oferta = claim `sub` del token |
| `channel` | `web` \| `mobile` | No | Canal donde se aceptó |
| `accepted_at` | string (fecha-hora) | No | Momento en que quedó registrada la aceptación (punto de no pérdida) |
| `status` | `registered` \| `issuance_pending` \| `in_review` \| `adjusted` \| `rejected` | No | Ver §4 (`adjusted` y `rejected` los pone el asesor, HU-W1.1-02) |
| `underwriting` | objeto | **Sí** | `null` hasta que la suscripción decide |
| `underwriting.outcome` | `approved` \| `human_review` | No | Decisión de la suscripción automática |
| `underwriting.reason` | `provisional_offer` \| `high_risk_score` | **Sí** | Motivo del enrutamiento a revisión; `null` si `approved` |
| `underwriting.decided_at` | string (fecha-hora) | No | Momento de la decisión |
| `review` | objeto | **Sí** | Decisión del asesor sobre una solicitud enrutada (HU-W1.1-02); `null` hasta que decide |
| `review.outcome` | `accept` \| `adjust` \| `reject` | No | Tras `accept`, `underwriting.outcome` pasa a `approved` |
| `review.decided_at` | string (fecha-hora) | No | Momento de la decisión del asesor |
| `review.adjusted_offer` | `{offer_id, premium, currency, expires_at}` | **Sí** | Solo en `adjust`: la nueva oferta (`adj-{acceptance_id}`, vigencia de 24 h) que el cliente acepta con el mismo `POST /v1/offers/{offer_id}/acceptance` |
| `processing.attempts` | entero ≥ 0 | No | Intentos fallidos de procesamiento. **> 0 con `status=registered` = trámite reanudándose tras una falla** |
| `processing.last_failure_at` | string (fecha-hora) | **Sí** | Última falla; `null` si nunca falló |
| `processing.next_attempt_at` | string (fecha-hora) | **Sí** | Próximo intento automático; `null` cuando el estado ya no es `registered` |
| `updated_at` | string (fecha-hora) | No | Último cambio |

**Fechas:** RFC 3339 en UTC con fracción de segundo de longitud variable (hasta 9 dígitos, p. ej. `2026-10-12T01:54:41.0909319Z`).
`new Date(valor)` de JavaScript las interpreta bien (trunca a milisegundos). Mostrarlas en la zona horaria del usuario.

---

## 3. Códigos HTTP y catálogo de errores

Cuerpo de error (todas las rutas): `{"error": "<código estable>", "message": "<texto técnico en inglés>"}`. **El front decide el
texto a mostrar por `error`**, nunca por `message` (puede cambiar). `offer_expired` agrega `offer_id` y `expires_at`.

### Por endpoint

| Endpoint | Éxito | Errores posibles |
|---|---|---|
| `POST /v1/offers/{offer_id}/acceptance` | `202` nueva · `200` ya existía | `400`, `401`, `403`, `404 offer_not_found`, `422 offer_expired`, `503 acceptance_not_registered` |
| `GET /v1/acceptances/{acceptance_id}` | `200` | `400`, `401`, `403`, `404 acceptance_not_found`, `503 acceptance_unavailable` |
| `GET /v1/offers/{offer_id}/acceptance` | `200` | `400`, `401`, `403`, `404 acceptance_not_found`, `503 acceptance_unavailable` |

### Catálogo

| HTTP | `error` | Cuándo | Ejemplo real | Acción que debe ofrecer el front |
|---|---|---|---|---|
| 400 | `invalid_request` | `channel` ausente o distinto de `web`/`mobile`; cuerpo no JSON; id vacío o > 128 caracteres | `{"error":"invalid_request","message":"channel must be web or mobile"}` · `{"error":"invalid_request","message":"the request body is not valid JSON"}` | Error de programación del front: registrar y mostrar «No pudimos procesar tu solicitud» |
| 401 | `unauthorized` | Sin token (`WWW-Authenticate: Bearer`) o token inválido/vencido (`WWW-Authenticate: Bearer error="invalid_token"`) | `{"error":"unauthorized","message":"a bearer token is required"}` · `{"error":"unauthorized","message":"the token is invalid or expired"}` | Renovar el token (`REFRESH_TOKEN_AUTH`) y reintentar una vez; si vuelve a fallar → pantalla «Sesión expirada» |
| 403 | `forbidden` | Rol no permitido (p. ej. `advisor` o `partner` en el `POST`, `partner` en los `GET`) u oferta/aceptación de otro cliente | `{"error":"forbidden","message":"the caller is not allowed to perform this operation"}` | Pantalla «Acceso no autorizado»; no reintentar |
| 404 | `offer_not_found` | La oferta no existe (solo `POST`) | `{"error":"offer_not_found","message":"the offer does not exist"}` | «Esta oferta no existe o ya no está disponible» + volver a cotizar |
| 404 | `acceptance_not_found` | La aceptación o la oferta consultada no tiene aceptación (solo `GET`) | `{"error":"acceptance_not_found","message":"the acceptance does not exist"}` | En `GET /v1/offers/{id}/acceptance`: la oferta aún no se aceptó → mostrar la oferta con «Aceptar». En `GET /v1/acceptances/{id}`: id desconocido (o el servicio se reinició, §8) → volver a consultar por oferta |
| 422 | `offer_expired` | La vigencia de la oferta terminó (`ahora ≥ expires_at`) y no tenía aceptación previa. **No se registra nada** | `{"error":"offer_expired","message":"the offer validity is over; request a new offer","offer_id":"demo-expired-002","expires_at":"2026-10-12T00:54:44.4210077Z"}` | Pantalla «Oferta vencida» con el motivo («La oferta venció el {expires_at}») y botón **«Generar nueva oferta»** (ver §5.3) |
| 503 | `acceptance_not_registered` | No se pudo guardar la aceptación (falla de almacenamiento) | `{"error":"acceptance_not_registered","message":"the acceptance could not be registered; retrying is safe"}` | «No pudimos registrar tu aceptación, intenta de nuevo» + botón Reintentar (reenviar el mismo `POST`: **es seguro**, nunca duplica) |
| 503 | `acceptance_unavailable` | No se pudo leer la aceptación | `{"error":"acceptance_unavailable","message":"the acceptance could not be read; retry later"}` | Mantener la última pantalla conocida y seguir con el polling (§6) |

> Con el almacenamiento actual en memoria los dos `503` no ocurren en la práctica, pero el front debe manejarlos: aparecerán
> cuando la persistencia pase a PostgreSQL. Ante un error de red o `5xx` sin cuerpo JSON (p. ej. del API Gateway), tratarlo como
> `503` del mismo endpoint.

---

## 4. Estados de la aceptación

```
                ┌──────────────── falla de un paso posterior (attempts+1, reintento automático) ──┐
                ▼                                                                                 │
(POST 202) ─▶ registered ──────────────────────────────────────────────────────────────────────────┘
                │
                ├── suscripción aprueba ──────────▶ issuance_pending   (final en esta HU)
                └── suscripción enruta a humano ──▶ in_review
                                                      ├─ asesor acepta ──▶ registered (aprobada) ─▶ issuance_pending
                                                      ├─ asesor ajusta ──▶ adjusted  (final; nueva oferta en review.adjusted_offer)
                                                      └─ asesor rechaza ─▶ rejected  (final; sin cobro)
                                                      (HU-W1.1-02)

(POST 422 offer_expired) ─▶ no se crea ninguna aceptación
```

| Estado / condición | Significado | Qué mostrar (mockup) | ¿Seguir haciendo polling? |
|---|---|---|---|
| `registered` y `processing.attempts == 0` | Aceptación guardada; suscripción en curso | «Aceptación de la oferta»: confirmación de que la aceptación quedó registrada (fecha/hora de `accepted_at`) y un indicador «Estamos validando tu solicitud…» | Sí |
| `registered` y `processing.attempts > 0` | Un paso posterior falló; el sistema reintenta solo | «Aceptación conservada ante falla posterior»: «Tu aceptación del {accepted_at} está guardada. Tuvimos un inconveniente y estamos retomando el trámite automáticamente; no necesitas repetirlo». Opcional: «Próximo intento: {next_attempt_at}» | Sí |
| `in_review` | La suscripción enrutó el caso a un asesor | «Solicitud en revisión»: «Tu solicitud está siendo revisada por un asesor». Para el cliente basta un texto genérico; `underwriting.reason` puede usarse solo para variar el texto: `provisional_offer` → «tu oferta se calculó con información provisional», `high_risk_score` → «tu perfil requiere una revisión adicional» | No: la decisión del asesor (HU-W1.1-02) se ve al volver a consultar la aceptación y llega también por correo |
| `issuance_pending` | Riesgo aprobado; la emisión ya inició | «Suscripción aprobada, estamos emitiendo tu póliza». El paso siguiente (pago de la prima, HU-W1.3-02) aún no existe | No |
| `adjusted` | Un asesor ajustó la prima (HU-W1.1-02) | E6b «Solicitud ajustada»: nueva prima y vigencia de `review.adjusted_offer`, «Ver la nueva oferta» → la oferta `review.adjusted_offer.offer_id`; también se avisa por correo | No |
| `rejected` | Un asesor no aceptó el riesgo (HU-W1.1-02) | E6c «Solicitud rechazada»: no se cobró nada, «Continuar con mi crédito»; también se avisa por correo | No |

Transiciones posibles: `registered → registered` (reintento), `registered → issuance_pending`, `registered → in_review` y, por la
decisión del asesor (HU-W1.1-02), `in_review → registered` (aceptar), `in_review → adjusted` e `in_review → rejected`.
`issuance_pending`, `adjusted` y `rejected` no retroceden.

---

## 5. Guía por escenario de mockup

Ofertas de prueba (simuladas, ver §8): el id tiene la forma `demo-<escenario>-<sufijo>`; usar un **sufijo nuevo** para repetir un
escenario desde cero (p. ej. `demo-active-20261011a`).

| Escenario | `offer_id` de prueba |
|---|---|
| Aceptación aprobada | `demo-active-<sufijo>` |
| En revisión por oferta provisional | `demo-review-<sufijo>` |
| En revisión por puntaje de riesgo alto | `demo-highrisk-<sufijo>` |
| Oferta vencida | `demo-expired-<sufijo>` |
| Falla posterior y reanudación | `demo-flaky-<sufijo>` |

### 5.1 Aceptación de la oferta y disparo de la suscripción

1. El asegurado pulsa «Aceptar» → `POST /v1/offers/demo-active-002/acceptance` con `{"channel":"web"}`.
2. Respuesta `202` con `status: "registered"` (ejemplo de §2.1). Mostrar la confirmación de aceptación registrada.
3. Polling `GET /v1/acceptances/{acceptance_id}` (§6). En la ejecución capturada, a los ~1,5 s:

```json
{
  "acceptance_id": "b310b02b-9820-4725-bded-586d263dd16e",
  "offer_id": "demo-active-002",
  "customer_id": "0f3c2a9e-1b7d-4c55-9a51-2f0d4e6b8c11",
  "channel": "web",
  "accepted_at": "2026-10-12T01:54:41.0909319Z",
  "status": "issuance_pending",
  "underwriting": { "outcome": "approved", "reason": null, "decided_at": "2026-10-12T01:54:41.0909319Z" },
  "processing": { "attempts": 0, "last_failure_at": null, "next_attempt_at": null },
  "updated_at": "2026-10-12T01:54:41.0909319Z"
}
```

4. Con `issuance_pending`: detener el polling y mostrar «Suscripción aprobada / emisión iniciada».

Errores a manejar aquí: `401`, `403`, `404 offer_not_found`, `422 offer_expired` (§5.3), `503 acceptance_not_registered`.
Deshabilitar el botón mientras el `POST` está en curso; si se pierde la respuesta, reenviar el mismo `POST` es seguro.

### 5.2 Solicitud en revisión (decisión humana)

1. `POST /v1/offers/demo-review-002/acceptance` → `202 registered`.
2. Polling → `in_review` (capturado ~1,5 s después):

```json
{
  "acceptance_id": "7131ceb6-2050-4c83-ab99-f47a4b870fb9",
  "offer_id": "demo-review-002",
  "customer_id": "0f3c2a9e-1b7d-4c55-9a51-2f0d4e6b8c11",
  "channel": "web",
  "accepted_at": "2026-10-12T01:54:42.7390974Z",
  "status": "in_review",
  "underwriting": { "outcome": "human_review", "reason": "provisional_offer", "decided_at": "2026-10-12T01:54:42.7390974Z" },
  "processing": { "attempts": 0, "last_failure_at": null, "next_attempt_at": null },
  "updated_at": "2026-10-12T01:54:42.7390974Z"
}
```

   Con `demo-highrisk-<sufijo>` el resultado es igual con `"reason": "high_risk_score"`.
3. Detener el polling y mostrar «Solicitud en revisión» dentro del mismo flujo. Si el cliente vuelve más tarde, consultar
   `GET /v1/offers/{offer_id}/acceptance` para volver a pintar el estado.
4. Un asesor autenticado (`advisor`) puede leer la misma aceptación con los dos `GET` (respuesta idéntica). La bandeja del asesor
   (listar y decidir) **no existe aún** (HU-W1.1-02).

### 5.3 Oferta vencida, con opción de generar nueva oferta

1. `POST /v1/offers/demo-expired-002/acceptance` → `422`:

```json
{
  "error": "offer_expired",
  "message": "the offer validity is over; request a new offer",
  "offer_id": "demo-expired-002",
  "expires_at": "2026-10-12T00:54:44.4210077Z"
}
```

2. No se registró nada: `GET /v1/offers/demo-expired-002/acceptance` → `404 acceptance_not_found`.
3. Mostrar «Oferta vencida»: motivo («La vigencia de tu oferta terminó el {expires_at}») y botón **«Generar nueva oferta»**.
4. **Endpoint para generar la nueva oferta: PENDIENTE (HU-W1.2-03).** Hoy no existe ningún endpoint que cree ofertas. El front debe
   dejar la acción del botón configurable (p. ej. navegar al inicio del flujo de cotización) para conectarla cuando HU-W1.2-03
   publique su endpoint. No reintentar el `POST` de aceptación con la misma oferta: seguirá respondiendo `422`.

> Una oferta que **ya** había sido aceptada antes de vencer no da `422`: el `POST` devuelve `200` con su aceptación.

### 5.4 Falla posterior: reanudar sin que el cliente repita el proceso

Capturado con `demo-flaky-002` (la suscripción simulada falla los 2 primeros intentos):

1. `POST` → `202 registered`, `attempts: 0`.
2. Polling a los ~0,7 s — el primer intento falló, la aceptación sigue guardada:

```json
{
  "acceptance_id": "f88568fd-4f80-4532-83e3-c6f1ad1f2945",
  "offer_id": "demo-flaky-002",
  "customer_id": "0f3c2a9e-1b7d-4c55-9a51-2f0d4e6b8c11",
  "channel": "web",
  "accepted_at": "2026-10-12T01:54:44.4842859Z",
  "status": "registered",
  "underwriting": null,
  "processing": {
    "attempts": 1,
    "last_failure_at": "2026-10-12T01:54:44.4842859Z",
    "next_attempt_at": "2026-10-12T01:54:46.4842859Z"
  },
  "updated_at": "2026-10-12T01:54:44.4842859Z"
}
```

   → mostrar «Aceptación conservada ante falla posterior».
3. ~2 s después: `attempts: 2`, `next_attempt_at` = última falla + 4 s (la espera se duplica).
4. ~4 s después: el trámite se reanudó solo:

```json
{
  "acceptance_id": "f88568fd-4f80-4532-83e3-c6f1ad1f2945",
  "offer_id": "demo-flaky-002",
  "customer_id": "0f3c2a9e-1b7d-4c55-9a51-2f0d4e6b8c11",
  "channel": "web",
  "accepted_at": "2026-10-12T01:54:44.4842859Z",
  "status": "issuance_pending",
  "underwriting": { "outcome": "approved", "reason": null, "decided_at": "2026-10-12T01:54:50.5655195Z" },
  "processing": { "attempts": 2, "last_failure_at": "2026-10-12T01:54:46.5654819Z", "next_attempt_at": null },
  "updated_at": "2026-10-12T01:54:50.5655195Z"
}
```

Reglas para el front:

- **Nunca** pedir al cliente que vuelva a aceptar: el backend reintenta solo (esperas de 2 s, 4 s, 8 s… hasta 60 s entre intentos, sin límite de intentos).
- Si el front pierde el `acceptance_id` (recarga, otra pestaña, otro dispositivo), retomar con `GET /v1/offers/{offer_id}/acceptance`.
  Reenviar el `POST` de la misma oferta también es seguro: devuelve `200` con la **misma** aceptación (verificado: mismo `acceptance_id`).
- Guardar `offer_id` y `acceptance_id` en `sessionStorage` al recibir el `202`, para retomar tras recargar.

---

## 6. Consulta del estado y polling

- **Sí, el front debe hacer polling**: la suscripción es asíncrona y no hay push ni WebSocket para esta HU.
- Endpoint: `GET /v1/acceptances/{acceptance_id}` (o `GET /v1/offers/{offer_id}/acceptance` si solo se tiene la oferta).
- **Intervalo recomendado: 2 s**, con la primera consulta ~1 s después del `202`. En condiciones normales el estado cambia en
  menos de 1 s (la aceptación nueva despierta al procesador de inmediato; además revisa pendientes cada 1 s).
- **Detener** el polling al recibir `issuance_pending` o `in_review` (estados finales en esta HU), o un `401`/`403`/`404`.
- Mientras `status = registered` y `processing.attempts > 0`, mostrar la pantalla de aceptación conservada y seguir consultando.
  Si `next_attempt_at` está a más de 2 s, se puede esperar hasta `next_attempt_at` + 1 s antes de la siguiente consulta.
- Si tras **2 minutos** sigue en `registered`, bajar la frecuencia a **cada 15 s** (sin abandonar: el backend sigue reintentando) y
  mantener el mensaje de aceptación conservada.
- Ante `503 acceptance_unavailable` o error de red: mantener la pantalla actual y seguir con el mismo intervalo.
- Las respuestas llevan `Cache-Control: no-store`; no cachear ni deduplicar las consultas.

---

## 7. Autenticación, CORS y variables de entorno

### Autenticación (HA-S01, sin cambios para esta HU)

- Rutas humanas: `Authorization: Bearer <ID token>` del user pool de Cognito (`token_use = id`). Sin token o con token inválido o
  vencido → `401`; con rol no permitido o recurso ajeno → `403`.
- Roles (grupo de Cognito en `cognito:groups`): `POST` solo `insured`; `GET` `insured` (lo suyo), `advisor`, `operations`. `partner` no tiene acceso.
- La titularidad usa el claim `sub`: `customer_id` de la respuesta = `sub` del asegurado.
- Sesión del portal (ya definida en `docs/api/README.md`): ID token de 5 min, renovar con `REFRESH_TOKEN_AUTH` antes de que venza;
  ante `401` renovar y reintentar una vez, luego «Sesión expirada»; ante `403` «Acceso no autorizado».
- **En local:** Cognito emulado con Moto (`tests/security/ha-s01/local`), usuario asegurado `asegurado.local@example.com` (grupo
  `insured`) y asesor `asesor.local@example.com` (grupo `advisor`); la contraseña la define quien siembra (`MOTO_USER_PASSWORD`).
  El ambiente de desarrollo del front ya apunta a ese pool (`src/environments/environment.development.ts`).

### CORS

- **El servicio no responde CORS** (verificado: un `OPTIONS` devuelve `405`). Igual que en HA-S01, en AWS el preflight `OPTIONS` y
  las cabeceras CORS las resuelve el API Gateway.
- **En local:** usar el proxy de desarrollo de Angular (`proxy.conf.json`) para que el navegador llame al mismo origen, p. ej.:

```json
{ "/v1": { "target": "http://localhost:8080", "secure": false, "changeOrigin": true } }
```

### URL base y variables

| Dónde | URL base de origination |
|---|---|
| Local (servicio en el portátil) | `http://localhost:8080` (vía proxy de Angular: rutas relativas `/v1/...`) |
| AWS (API Gateway) | **Pendiente:** las rutas de esta HU todavía no están registradas en el módulo `infra/terraform/modules/apigateway` |

Variables del servicio relevantes para levantarlo en local (las define quien corre el backend, no el front):

| Variable | Valor en local | Nota |
|---|---|---|
| `PORT` | `8080` (defecto) | |
| `REDIS_ADDR`, `OPEN_FINANCE_URL` | obligatorias para arrancar (perfilamiento) | La aceptación no las usa, pero el servicio no arranca sin ellas |
| `AUTH_ISSUER` | `https://cognito-idp.us-east-2.amazonaws.com/<user pool>` | **Sin esta variable las rutas de aceptación no se montan (responden `404`)** |
| `AUTH_JWKS_URL` | `http://localhost:5100/<user pool>/.well-known/jwks.json` | Obligatoria con Moto |
| `AUTH_CLIENT_IDS` | `<cliente web>,<cliente móvil>` | Los imprime `seed.sh` |
| `ACCEPTANCE_POLL_INTERVAL_MS` / `ACCEPTANCE_RETRY_BASE_MS` / `ACCEPTANCE_RETRY_MAX_MS` | `1000` / `2000` / `60000` | Ritmo de procesamiento y reintentos |
| `UNDERWRITING_STUB_FLAKY_FAILURES` | `2` | Intentos fallidos simulados en `demo-flaky-*` |
| `UNDERWRITING_REVIEW_SCORE_THRESHOLD` | `0.8` (defecto) | Una oferta normal con puntaje mayor a este valor va al asesor (`high_risk_score`) |

Variables que el front necesita en su `environment`: URL base de la API (o el proxy), región, user pool y cliente web de Cognito
(las mismas de HA-S01). Esta HU no agrega variables nuevas al front.

---

## 8. Qué está simulado y qué está pendiente

| Pieza | Estado hoy | Efecto visible para el front | Pendiente de |
|---|---|---|---|
| Almacenamiento de aceptaciones | **En memoria** | Sobrevive a fallas de dependencias, pero **se pierde al reiniciar el servicio**: un `acceptance_id` anterior responde `404` y la oferta puede volver a aceptarse | PostgreSQL de origination |
| Ofertas | **Simuladas** (`demo-<escenario>-<sufijo>`): prima 89 500 COP, vigencia de 24 h desde la **primera consulta**; la oferta pertenece al **primer usuario que la usa**; otros ids → `404 offer_not_found` | No hay endpoint para leer el detalle de la oferta (prima, coberturas, vigencia): la pantalla de oferta debe usar datos de prueba hasta HU-W1.2-03 | HU-W1.2-03 |
| Generar nueva oferta | **No existe** | El botón de §5.3 queda sin endpoint | HU-W1.2-03 |
| Suscripción automática | **Reglas provisionales**: oferta provisional → `in_review` (`provisional_offer`); puntaje mayor al umbral de revisión (`UNDERWRITING_REVIEW_SCORE_THRESHOLD`, 0,8 por defecto; 0 es el menor riesgo) → `in_review` (`high_risk_score`); si no, `approved`. `demo-flaky-*` falla los primeros intentos | Los motivos y las proporciones de revisión cambiarán con el motor real | Motor de suscripción |
| Bandeja del asesor | **En memoria**; HU-W1.1-02 expone `GET /v1/review-requests`, `GET /v1/review-requests/{acceptance_id}` y `POST …/decision` (solo `advisor`) | `in_review` avanza a `registered`/`issuance_pending`, `adjusted` o `rejected` según el asesor ([`HU-W1.1-02-api.md`](HU-W1.1-02-api.md)) | PostgreSQL de origination |
| Inicio de la emisión | **Simulado**: solo registra que inició | Tras `issuance_pending` no hay cobro ni póliza | HU-W1.3-02, HA-D02 |
| Eventos | Ninguno publicado | — | HU-W1.3-02 (`PolicyIssued`, `ChargeCompleted`) |
| Rutas en el API Gateway (AWS) | No registradas | Solo se puede probar en local | Infraestructura |
