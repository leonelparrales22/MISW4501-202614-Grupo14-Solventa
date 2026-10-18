# Contrato de API — HU-W1.2-03 · Oferta personalizada en el flujo de originación

> **Fuente única para el front (Angular).** Generado a partir del código real del servicio `origination`
> (`internal/adapters/http/offers.go`, `internal/orchestrator/offer.go` y `requote.go`, `internal/domain/offer.go`,
> `internal/rating/explain.go`, `internal/adapters/postgres/offers.go`, `migrations/0003_offer.sql`) y de respuestas **capturadas del
> binario en ejecución** el 2026-10-18 (`audit` y `origination` en Docker, PostgreSQL de cada uno, Redis, los stubs de Open Finance, Open
> Data y KYC detrás de Toxiproxy, el stub de correo y Cognito local de Moto con verificación JWT RS256 real de `pkg/security`). Los
> identificadores y las fechas de los ejemplos son los de esa ejecución. Contrato formal: `services/origination/api/openapi.yaml`
> (versión 0.10.0).

---

## 1. Resumen del flujo de punta a punta

```
Front embebido del aliado        origination                                         audit
─────────────────────────        ─────────────────────────────────────────────       ──────────────
POST /v1/offers ───────────────▶ valida ─▶ ¿consentimiento activo y es este consent_id? ─▶ GET /v1/audit/consent-verifications
 (sin sesión; API key en          ├─ no / otro / audit no responde ─▶ 422 / 422 / 503  (no se consulta ninguna fuente)
  el API Gateway)                 └─ sí ─▶ perfil (fuentes en paralelo, caché, base) ─▶ prima (rating v1) ─▶ fila inmutable en `offer`
  ◀── 201 {offer_id, ...}  · redirige al front web con el offer_id

Front web (con sesión de Cognito)
GET  /v1/offers/{id} ────────▶ el primer asegurado que la abre la RECLAMA (queda como dueño); otro asegurado: 403
POST /v1/offers/{id}/acceptance ▶ (HU-W1.3-01) solo el dueño; vencida o reemplazada: 422 offer_expired
POST /v1/offers/{id}/renewal ──▶ solo el dueño y solo si venció: oferta nueva (idempotente)

En segundo plano (cada OFFER_REQUOTE_POLL_INTERVAL_MS):
oferta provisional vigente y sin aceptar + la fuente volvió ─▶ oferta NUEVA normal (la anterior pasa a `superseded`) + correo (una vez)
```

| Criterio | Cubierto | Cómo |
|---|---|---|
| CA-1 Oferta con prima, coberturas, condiciones y vigencia de 24 h dentro del recorrido embebido | Sí | `POST /v1/offers` y `GET /v1/offers/{offer_id}` devuelven prima mensual, `terms.coverages`, `conditions[]` y `expires_at` (24 h después de `created_at`) |
| CA-2 Persistida y ligada al perfil, las reglas y la versión del motor | Sí | Fila inmutable en `offer` (los `UPDATE`/`DELETE`/`TRUNCATE` fallan) con la copia del perfil, las reglas aplicadas y `rater_version`. El cliente no recibe `score` ni `rules` |
| CA-3 Provisional distinguiendo caché (con fecha) y perfil base | Sí | `mode: provisional` + `provisional_reason` (`cached_profile` con `profile_date`, o `base_actuarial_profile`) + `unavailable_source` + condición `provisional_requote_when_source_restored` |
| CA-4 Provisional aceptada se respeta; vigente y no aceptada se recotiza al volver la fuente y se notifica | Sí | Trabajador en segundo plano: oferta nueva (`superseded_by`), correo único, aceptar la anterior da `422 offer_expired`; una provisional ya aceptada no se toca |
| CA-5 Vencida no se acepta; se informa el motivo y se ofrece una nueva | Sí | `422 offer_expired` (HU-W1.3-01) y `POST /v1/offers/{offer_id}/renewal` |

---

## 2. Endpoints

### Cabeceras comunes

- `Content-Type: application/json` en los cuerpos. Todas las respuestas llevan `Cache-Control: no-store`.
- `Authorization: Bearer <ID token de Cognito>` en `GET` y `renewal` (rutas humanas). `POST /v1/offers` **no lleva token**: es del canal del
  aliado y el API Gateway la protege con la API key (igual que `/v1/risk-profiles`). Desde `localhost` no hay Gateway: la llamada va directa.
- Error: `{"error": "<código estable>", "message": "<texto en inglés, no mostrar>"}`. Mostrar el texto según `error`.

### 2.1 `POST /v1/offers` — generar la oferta (canal del aliado)

El aliado envía la identidad del titular y los datos del crédito (decisión D3) y el `consent_id` que devolvió `audit` al registrar el
consentimiento (HU-W1.2-01).

| Campo | Tipo | Reglas |
|---|---|---|
| `customer_id` | string | 1–128, `[A-Za-z0-9_-]`. El mismo con que se registró el consentimiento |
| `consent_id` | string | 1–128, `[A-Za-z0-9_-]`. Debe ser **el consentimiento activo** de ese cliente |
| `partner_id`, `application_id` | string | 1–128, `[A-Za-z0-9_-]` |
| `document_id` | string | 5–20 letras o dígitos |
| `full_name` | string | 3–120 caracteres, con al menos una letra |
| `email` | string | Dirección simple. A ella llega el aviso de recotización |
| `birth_date` | string `YYYY-MM-DD` | Edad entre 18 y 70 años cumplidos |
| `insured_amount` | número | 10 000 000 a 2 000 000 000 |
| `term_months` | entero | 12 a 360 |

Solicitud:

```json
{"customer_id":"cust-demo-a-r1792338656","consent_id":"cns-…","partner_id":"partner-demo","application_id":"app-cust-demo-a-r1792338656",
 "document_id":"1020304050","full_name":"Ana María Pérez","email":"ana.demo@example.com","birth_date":"1985-03-14",
 "insured_amount":250000000,"term_months":120}
```

Respuesta normal:

`HTTP 201`

```json
{
  "offer_id": "off-b948aa0f4d51e923a67c7cee087e94ed",
  "kind": "personalized",
  "status": "active",
  "premium": 132000,
  "currency": "COP",
  "mode": "normal",
  "expires_at": "2026-10-19T15:53:01.627931Z",
  "can_accept": true,
  "can_renew": false,
  "channel": "embedded",
  "created_at": "2026-10-18T15:53:01.627931Z",
  "provisional_reason": null,
  "profile_date": null,
  "unavailable_source": null,
  "profile": {
    "category": "standard",
    "source": "external_sources"
  },
  "terms": {
    "insured_amount": 250000000,
    "term_months": 120,
    "coverages": [
      {
        "code": "death",
        "insured_amount": 250000000
      },
      {
        "code": "total_permanent_disability",
        "insured_amount": 250000000
      }
    ]
  },
  "conditions": [
    "validity_24h",
    "onerous_beneficiary_lender",
    "monthly_premium",
    "max_insured_age_70"
  ],
  "rater_version": "v1",
  "applicant": {
    "full_name": "Ana María Pérez"
  },
  "credit": {
    "partner_id": "partner-demo",
    "application_id": "app-cust-demo-a-r1792338656"
  },
  "superseded_by": null,
  "renewed_by": null
}
```

Cabecera `Location: /v1/offers/off-b948aa0f4d51e923a67c7cee087e94ed`. **El `offer_id` es lo único que el aliado pasa al front web** (128 bits
aleatorios; ver el riesgo del *claim* en §8).

Respuesta **provisional** con Open Finance caído y sin perfil en caché (el perfil base):

`HTTP 201`

```json
{
  "offer_id": "off-a25586ff7b84d2bf27e1e34e7dc0aab4",
  "kind": "personalized",
  "status": "active",
  "premium": 147400,
  "currency": "COP",
  "mode": "provisional",
  "expires_at": "2026-10-19T15:53:01.815352Z",
  "can_accept": true,
  "can_renew": false,
  "channel": "embedded",
  "created_at": "2026-10-18T15:53:01.815352Z",
  "provisional_reason": "base_actuarial_profile",
  "profile_date": null,
  "unavailable_source": "open_finance",
  "profile": {
    "category": "base_actuarial",
    "source": "base_actuarial"
  },
  "terms": {
    "insured_amount": 250000000,
    "term_months": 120,
    "coverages": [
      {
        "code": "death",
        "insured_amount": 250000000
      },
      {
        "code": "total_permanent_disability",
        "insured_amount": 250000000
      }
    ]
  },
  "conditions": [
    "validity_24h",
    "onerous_beneficiary_lender",
    "monthly_premium",
    "max_insured_age_70",
    "provisional_requote_when_source_restored"
  ],
  "rater_version": "v1",
  "applicant": {
    "full_name": "Ana María Pérez"
  },
  "credit": {
    "partner_id": "partner-demo",
    "application_id": "app-cust-demo-c-r1792338656"
  },
  "superseded_by": null,
  "renewed_by": null
}
```

Con un perfil parcial en caché la respuesta es igual con `"provisional_reason": "cached_profile"`, `"profile_date": "<fecha del perfil>"` y
`"profile": {"category": "standard", "source": "cache"}`.

### 2.2 `GET /v1/offers/{offer_id}` — leer la oferta (E3, E3b, E3c)

- **Roles:** `insured`, `advisor`, `operations`. Cualquier otro rol: `403`.
- **Primer acceso = *claim*.** La oferta nace sin dueño. El **primer asegurado que la abre con sesión** queda como su dueño; desde entonces otro
  asegurado recibe `403`. `advisor` y `operations` leen sin reclamar.
- Parámetro de ruta: `offer_id`, 1–128 caracteres `[A-Za-z0-9_-]`; otro formato es `400 invalid_request`.

Primer acceso del asegurado (reclama la oferta), misma forma que §2.1:

`HTTP 200`

```json
{"offer_id": "off-b948aa0f4d51e923a67c7cee087e94ed", "kind": "personalized", "status": "active", "premium": 132000, "currency": "COP", "mode": "normal", "expires_at": "2026-10-19T15:53:01.627931Z", "can_accept": true, "can_renew": false, "channel": "embedded", "created_at": "2026-10-18T15:53:01.627931Z", "provisional_reason": null, "profile_date": null, "unavailable_source": null, "profile": {"category": "standard", "source": "external_sources"}, "terms": {"insured_amount": 250000000, "term_months": 120, "coverages": [{"code": "death", "insured_amount": 250000000}, {"code": "total_permanent_disability", "insured_amount": 250000000}]}, "conditions": ["validity_24h", "onerous_beneficiary_lender", "monthly_premium", "max_insured_age_70"], "rater_version": "v1", "applicant": {"full_name": "Ana María Pérez"}, "credit": {"partner_id": "partner-demo", "application_id": "app-cust-demo-a-r1792338656"}, "superseded_by": null, "renewed_by": null}
```

Una oferta provisional **reemplazada** (`status: superseded`, apunta a la nueva con `superseded_by`; la nueva se lee igual):

`HTTP 200`

```json
{"offer_id": "off-a25586ff7b84d2bf27e1e34e7dc0aab4", "kind": "personalized", "status": "superseded", "premium": 147400, "currency": "COP", "mode": "provisional", "expires_at": "2026-10-19T15:53:01.815352Z", "can_accept": false, "can_renew": false, "channel": "embedded", "created_at": "2026-10-18T15:53:01.815352Z", "provisional_reason": "base_actuarial_profile", "profile_date": null, "unavailable_source": "open_finance", "profile": {"category": "base_actuarial", "source": "base_actuarial"}, "terms": {"insured_amount": 250000000, "term_months": 120, "coverages": [{"code": "death", "insured_amount": 250000000}, {"code": "total_permanent_disability", "insured_amount": 250000000}]}, "conditions": ["validity_24h", "onerous_beneficiary_lender", "monthly_premium", "max_insured_age_70", "provisional_requote_when_source_restored"], "rater_version": "v1", "applicant": {"full_name": "Ana María Pérez"}, "credit": {"partner_id": "partner-demo", "application_id": "app-cust-demo-c-r1792338656"}, "superseded_by": "off-7618b94e3329fa1b05c7d6b5f4ab2297", "renewed_by": null}
```

Una oferta **vencida** (`status: expired`, `can_renew: true`):

`HTTP 200`

```json
{"offer_id": "off-seedr1792338656", "kind": "personalized", "status": "expired", "premium": 132000, "currency": "COP", "mode": "normal", "expires_at": "2026-10-17T15:53:06.182906Z", "can_accept": false, "can_renew": true, "channel": "embedded", "created_at": "2026-10-16T15:53:06.182906Z", "provisional_reason": null, "profile_date": null, "unavailable_source": null, "profile": {"category": "standard", "source": "external_sources"}, "terms": {"insured_amount": 250000000, "term_months": 120, "coverages": [{"code": "death", "insured_amount": 250000000}, {"code": "total_permanent_disability", "insured_amount": 250000000}]}, "conditions": ["validity_24h", "onerous_beneficiary_lender", "monthly_premium", "max_insured_age_70"], "rater_version": "v1", "applicant": {"full_name": "Ana María Pérez"}, "credit": {"partner_id": "partner-demo", "application_id": "app-cust-demo-a-r1792338656"}, "superseded_by": null, "renewed_by": null}
```

**Ofertas de otras historias.** Este mismo `GET` sirve las ofertas ajustadas (`adj-…`, HU-W1.1-02) y de demostración (`demo-…`, HU-W1.3-01)
con una **vista reducida**: `kind` `adjusted`/`demo`, solo `premium`, `currency`, `mode`, `expires_at`, `status`, `can_accept` y `can_renew`
(siempre `false`), y todo lo demás en `null`. Su dueño es el cliente de la oferta; nunca se reclaman:

`HTTP 200`

```json
{"offer_id": "demo-active-guia", "kind": "demo", "status": "active", "premium": 89500, "currency": "COP", "mode": "normal", "expires_at": "2026-10-19T15:53:06.279821462Z", "can_accept": true, "can_renew": false, "channel": null, "created_at": null, "provisional_reason": null, "profile_date": null, "unavailable_source": null, "profile": null, "terms": null, "conditions": null, "rater_version": null, "applicant": null, "credit": null, "superseded_by": null, "renewed_by": null}
```

### 2.3 `POST /v1/offers/{offer_id}/renewal` — renovar una oferta vencida (E3c)

- **Rol:** solo `insured` y solo el dueño. Sin cuerpo.
- Vuelve a verificar el consentimiento, recalcula la edad, perfila y califica con los mismos datos del crédito y hereda el dueño.
- **Idempotente:** `201` la primera vez; después `200` con la misma oferta nueva (mismo `offer_id`).

`HTTP 201`

```json
{"offer_id": "off-e34fc49dc81f3ba02a7874552d985837", "kind": "personalized", "status": "active", "premium": 132000, "currency": "COP", "mode": "normal", "expires_at": "2026-10-19T15:53:06.244565Z", "can_accept": true, "can_renew": false, "channel": "embedded", "created_at": "2026-10-18T15:53:06.244565Z", "provisional_reason": null, "profile_date": null, "unavailable_source": null, "profile": {"category": "standard", "source": "cache"}, "terms": {"insured_amount": 250000000, "term_months": 120, "coverages": [{"code": "death", "insured_amount": 250000000}, {"code": "total_permanent_disability", "insured_amount": 250000000}]}, "conditions": ["validity_24h", "onerous_beneficiary_lender", "monthly_premium", "max_insured_age_70"], "rater_version": "v1", "applicant": {"full_name": "Ana María Pérez"}, "credit": {"partner_id": "partner-demo", "application_id": "app-cust-demo-a-r1792338656"}, "superseded_by": null, "renewed_by": null}
```

Repetir la llamada: `200` con el mismo `offer_id` (`off-e34fc49dc81f3ba02a7874552d985837`) y `renewed_by` en la oferta vencida apunta a ella.

Mientras la oferta se pueda aceptar:

`HTTP 409`

```json
{"error": "offer_still_valid", "message": "the offer can still be accepted; renew it once its validity is over", "offer_id": "off-b948aa0f4d51e923a67c7cee087e94ed", "expires_at": "2026-10-19T15:53:01.627931Z"}
```

Si una recotización la reemplazó, se usa la nueva (no se renueva):

`HTTP 409`

```json
{"error": "offer_superseded", "message": "a newer offer replaced this one; use it", "offer_id": "off-a25586ff7b84d2bf27e1e34e7dc0aab4", "superseded_by": "off-7618b94e3329fa1b05c7d6b5f4ab2297"}
```

### 2.4 Objeto `Offer`

| Campo | Tipo | Nulo | Descripción |
|---|---|---|---|
| `offer_id` | string | No | `off-` + 32 hex (personalizada), `adj-…` o `demo-…` |
| `kind` | `personalized` \| `adjusted` \| `demo` | No | Origen. Con `adjusted`/`demo` solo vienen los campos de la primera parte |
| `status` | `active` \| `expired` \| `superseded` | No | `active` hasta `expires_at`; `superseded` si una recotización la reemplazó |
| `premium`, `currency` | número, string | No | **Prima mensual**, en `COP` |
| `mode` | `normal` \| `provisional` | No | `provisional`: se calculó sin el perfil real (E3b) |
| `expires_at` | fecha ISO 8601 UTC | No | 24 h después de `created_at` |
| `can_accept` | boolean | No | `true` solo si está `active` |
| `can_renew` | boolean | No | `true` si venció, no la reemplazaron y no se renovó |
| `channel` | `embedded` \| `assisted` | **Sí** | Hoy solo `embedded` |
| `created_at` | fecha | **Sí** | |
| `provisional_reason` | `cached_profile` \| `base_actuarial_profile` | **Sí** | Solo si `mode` es `provisional` |
| `profile_date` | fecha | **Sí** | Cuándo se calculó el perfil en caché (`cached_profile`) |
| `unavailable_source` | string | **Sí** | Fuente caída (`open_finance`), solo si es provisional |
| `profile` | `{category, source}` | **Sí** | `category`: `preferred`, `standard`, `high_risk` o `base_actuarial`; `source`: `external_sources`, `open_finance`, `cache` o `base_actuarial`. **Nunca el puntaje** |
| `terms` | `{insured_amount, term_months, coverages[{code, insured_amount}]}` | **Sí** | `code`: `death` y `total_permanent_disability` |
| `conditions` | `string[]` | **Sí** | Códigos que el front traduce: `validity_24h`, `onerous_beneficiary_lender`, `monthly_premium`, `max_insured_age_70` y, si es provisional, `provisional_requote_when_source_restored` |
| `rater_version` | string | **Sí** | Versión del motor de precio (`v1`) |
| `applicant` | `{full_name}` | **Sí** | Sin documento ni correo |
| `credit` | `{partner_id, application_id}` | **Sí** | |
| `superseded_by` | string | **Sí** | La oferta que la reemplazó |
| `renewed_by` | string | **Sí** | Su renovación |

---

## 3. Códigos HTTP y catálogo de errores

| Endpoint | Éxito | Errores |
|---|---|---|
| `POST /v1/offers` | `201` | `400 invalid_request` · `400 validation_failed` · `422 consent_required` · `422 consent_mismatch` · `503 consent_unverifiable` · `503 offer_unavailable` |
| `GET /v1/offers/{offer_id}` | `200` | `400`, `401`, `403`, `404 offer_not_found`, `503 offer_unavailable` |
| `POST /v1/offers/{offer_id}/renewal` | `201` nueva · `200` ya existía | `400`, `401`, `403`, `404`, `409 offer_still_valid`, `409 offer_superseded`, `422 consent_required`, `422 validation_failed`, `503 consent_unverifiable`, `503 offer_unavailable` |

| HTTP | `error` | Cuándo | Ejemplo real | Acción que debe ofrecer el front |
|---|---|---|---|---|
| 400 | `invalid_request` | Cuerpo no JSON; `offer_id` vacío, de más de 128 caracteres o con caracteres fuera de `[A-Za-z0-9_-]` | `{"error":"invalid_request","message":"the request body is not valid JSON"}` | Error del aliado o del enlace: pantalla «Esta oferta no existe o ya no está disponible» |
| 400 | `validation_failed` | Campos ausentes o inválidos (la lista completa, para corregirlos de una vez). Códigos: `required`, `invalid_format`, `out_of_range` | ver abajo | Lo atiende el aliado (el front web no genera ofertas) |
| 401 | `unauthorized` | Sin token o token inválido/vencido | {"error": "unauthorized","message": "a bearer token is required"} | Redirigir al inicio de sesión |
| 403 | `forbidden` | Rol no permitido, oferta de otro asegurado o sin dueño en la renovación | {"error": "forbidden","message": "the caller is not allowed to perform this operation"} | «No tienes acceso a esta oferta» |
| 404 | `offer_not_found` | El id no existe | {"error": "offer_not_found","message": "the offer does not exist"} | «Esta oferta no existe» + volver al aliado |
| 409 | `offer_still_valid` | Renovar una oferta que aún se puede aceptar (`expires_at` en el cuerpo) | ver §2.3 | Volver a la oferta |
| 409 | `offer_superseded` | Renovar una oferta que reemplazó una recotización (`superseded_by` en el cuerpo) | ver §2.3 | Abrir `superseded_by` |
| 422 | `consent_required` | El cliente no tiene consentimiento activo. **No se consultó ninguna fuente** | {"error": "consent_required","message": "an active consent is required to profile the customer"} | El aliado debe volver a pedir el consentimiento (HU-W1.2-01) |
| 422 | `consent_mismatch` | El `consent_id` no es el consentimiento activo de ese `customer_id`. No se consultó ninguna fuente | {"error": "consent_mismatch","message": "the consent does not belong to the customer"} | Error del aliado |
| 422 | `validation_failed` (renovación) | El titular ya no es asegurable (`birth_date` fuera de 18–70) | `{"error":"validation_failed",…,"fields":[{"field":"birth_date","code":"out_of_range"}]}` | «Ya no puedes renovar esta oferta» |
| 503 | `consent_unverifiable` | `audit` no respondió a tiempo o su circuito está abierto. **Falla cerrado: no se consultó ninguna fuente**. Sin `AUDIT_URL` esta ruta siempre responde así | {"error": "consent_unverifiable","message": "the consent could not be verified; retry later"} | «Intenta de nuevo en unos minutos» (reintentar es seguro) |
| 503 | `offer_unavailable` | La oferta no se pudo producir o leer | `{"error":"offer_unavailable","message":"the offer is unavailable; retry later"}` | Mantener la última pantalla y reintentar |

Ejemplo de `validation_failed` (`POST /v1/offers` con datos incompletos):

```json
{
  "error": "validation_failed",
  "message": "one or more fields are missing or invalid",
  "fields": [
    {
      "field": "customer_id",
      "code": "invalid_format"
    },
    {
      "field": "consent_id",
      "code": "required"
    },
    {
      "field": "partner_id",
      "code": "required"
    },
    {
      "field": "application_id",
      "code": "required"
    },
    {
      "field": "document_id",
      "code": "required"
    },
    {
      "field": "full_name",
      "code": "required"
    },
    {
      "field": "email",
      "code": "required"
    },
    {
      "field": "birth_date",
      "code": "required"
    },
    {
      "field": "insured_amount",
      "code": "out_of_range"
    },
    {
      "field": "term_months",
      "code": "out_of_range"
    }
  ]
}
```

**Aceptar una oferta reemplazada o vencida** (HU-W1.3-01, `POST /v1/offers/{offer_id}/acceptance`) responde `422 offer_expired`. Para una oferta
reemplazada, `expires_at` es el **momento del reemplazo**:

`HTTP 422`

```json
{"error": "offer_expired", "message": "the offer validity is over; request a new offer", "offer_id": "off-a25586ff7b84d2bf27e1e34e7dc0aab4", "expires_at": "2026-10-18T15:53:02.442028Z"}
```

---

## 4. Estados de la oferta

```
                 ┌─ pasan 24 h ───────────────▶ expired ──(POST renewal)──▶ oferta NUEVA (renewed_by)
(POST 201) active┤
                 └─ si es provisional, no aceptada, y la fuente volvió ─▶ superseded ─▶ oferta NUEVA normal (superseded_by)
```

| `status` | Significado | `can_accept` | `can_renew` | Qué mostrar |
|---|---|---|---|---|
| `active` | Se puede aceptar hasta `expires_at` | `true` | `false` | E3 (normal) o E3b (provisional: aviso con el motivo y la fecha del perfil) |
| `expired` | Pasó `expires_at` | `false` | `true` (si no se renovó) | E3c: «La oferta venció el {expires_at}» + **«Generar nueva oferta»** (`renewal`) |
| `superseded` | Una recotización la reemplazó | `false` | `false` | «Actualizamos tu oferta»: abrir `superseded_by` |

Una oferta renovada sigue `expired`; el front sigue `renewed_by`.

---

## 5. Guía por pantalla

### 5.1 E3 · oferta normal

1. El aliado hace `POST /v1/offers` y redirige a `/embebido/oferta/{offer_id}`.
2. El front inicia sesión y hace `GET /v1/offers/{offer_id}` (**esto reclama la oferta**). Muestra `premium` (mensual), `terms`, `conditions`
   traducidas y `expires_at` (cuenta regresiva de 24 h).
3. «Aceptar» → `POST /v1/offers/{offer_id}/acceptance` con `{"channel":"web"}` → `202` (HU-W1.3-01). **El front siempre lee la oferta antes
   de aceptar**: una oferta sin dueño responde `403` al aceptar.

### 5.2 E3b · oferta provisional

`mode: provisional`: mostrar el aviso según `provisional_reason`: `cached_profile` («usamos tu último perfil, del {profile_date}») o
`base_actuarial_profile` («no pudimos consultar tu información financiera; tu oferta se calculó con un perfil general»), y la condición
`provisional_requote_when_source_restored` («si la información vuelve a estar disponible, recalcularemos tu oferta y te avisaremos»). La oferta
**se puede aceptar** y entonces se respeta tal cual (con la regla de revisión de HU-W1.3-01 para ofertas provisionales).

### 5.3 Aviso al reingresar tras una recotización

Si el cliente abre una oferta con `status: superseded`, ir a `superseded_by` y mostrar «Actualizamos tu oferta»: la prima anterior y la nueva. El
cliente también recibe un correo (una sola vez) con ambas primas, la vigencia y el enlace:

```json
{
  "to": "ana.c@example.com",
  "subject": "Tu oferta de seguro se actualizó",
  "idempotency_key": "offer-requoted:off-a25586ff7b84d2bf27e1e34e7dc0aab4",
  "body": "Hola Ana María Pérez,\n\nLa fuente de datos financieros ya está disponible y recalculamos tu oferta con tu perfil real. Tu oferta anterior era provisional.\n\nPrima mensual anterior: $ 147.400 COP\nPrima mensual nueva: $ 135.400 COP\nVigencia de la nueva oferta: hasta el 19/10/2026 a las 15:53 (UTC) (24 horas)\n\nRevisa y acepta la nueva oferta en este enlace: http://localhost:4200/embebido/oferta/off-7618b94e3329fa1b05c7d6b5f4ab2297\n\nLa oferta anterior ya no se puede aceptar.\n\nSolventa"
}
```

El enlace usa `OFFER_LINK_BASE_URL` (`/embebido/oferta/<id nuevo>`). Reemplazada la oferta, aceptar la anterior da `422 offer_expired`.

### 5.4 E3c · oferta vencida

`GET` → `status: expired`, `can_renew: true`. Mostrar el motivo y **«Generar nueva oferta»** → `POST …/renewal`. Si responde `409 offer_still_valid`
volver a la oferta; si `409 offer_superseded`, abrir `superseded_by`.

---

## 6. Escenarios de prueba

El `POST /v1/offers` real necesita un consentimiento registrado en `audit` (`POST /v1/audit/consents`, HU-W1.2-01). Para la pantalla sin
aliado, las ofertas `demo-<escenario>-<sufijo>` de HU-W1.3-01 siguen funcionando con el mismo `GET`. Los escenarios de punta a punta
(oferta normal, fuente caída, perfil parcial en caché, recotización con un solo correo, provisional aceptada, vencida sembrada, reinicio) están
automatizados en [`tests/e2e/hu-w1.2`](../../tests/e2e/hu-w1.2/README.md).

## 7. Cómo correrlo en local

```
./tests/e2e/hu-w1.2/run.sh                          # servicios reales en Docker, 13 escenarios
./services/origination/tests/contract/run-offers.sh # Schemathesis contra el openapi.yaml
REQUIRE_DOCKER=1 go test ./services/origination/... # unitarias e integración (PostgreSQL de Testcontainers)
```

Para probar a mano: `audit` con su PostgreSQL, `origination` con `DATABASE_URL`, `AUDIT_URL`, `AUTH_*` (Moto) y, para el correo de
recotización, `EMAIL_URL` (+ `DOCUMENTS_DIR` y `DOCUMENT_LINK_SECRET`). Con `OFFER_REQUOTE_POLL_INTERVAL_MS=1000` la recotización se ve en segundos.
Para simular la caída de Open Finance, desactivar su proxy en Toxiproxy (`POST /proxies/openfinance {"enabled":false}`).

## 8. Qué está simulado y qué está pendiente

| Pieza | Estado hoy | Efecto visible para el front | Pendiente de |
|---|---|---|---|
| Persistencia de la oferta | **PostgreSQL** con `DATABASE_URL`; sin ella, memoria (se pierde al reiniciar) | Con `DATABASE_URL` la oferta y su dueño sobreviven a un reinicio | — |
| Dueño de la oferta (*claim*) | El primer asegurado con sesión que la abre. **Riesgo:** quien conozca el `offer_id` y lo abra antes que el cliente se queda con ella | El id debe viajar solo por canales del cliente | Que el aliado conozca el `sub` del asegurado y lo fije al generarla |
| Aceptaciones, suscripción, bandeja e emisión | **En memoria** (HU-W1.3-01, HU-W1.1-02) | Una aceptación se pierde al reiniciar | PostgreSQL de esas historias |
| Documento contractual | Muestra el valor asegurado y el plazo de **demostración** (250 000 000 COP, 120 meses) aunque el cobro ya usa la prima real | El PDF puede no coincidir con la oferta | `billing.go` `demoCoverageTerms` (HA-D02) |
| Oferta ajustada `adj-*` | No usa los datos de la oferta personalizada | `terms`/`conditions` en `null` | HU-W1.1-02 |
| Correo de recotización | Reintentos en el proceso; si se detiene mientras reintenta, el aviso se pierde | El cliente ve el reemplazo al volver (§5.3) | Cola persistente |
| Eventos `OfferGenerated`, `OfferRequoted` | Registrados en `docs/events/README.md`; **no se publican** | — | HU-W4.1-01 |
| Revocación del consentimiento | Fuera de alcance (HU-W1.2-01, D2) | — | Historia futura |
| Rutas en el API Gateway (AWS) y variables de ECS | No registradas | Solo se puede probar en local | `chore(infra)` |

## 9. Cambios para otras historias

Estas guías y archivos **no se editaron** en este cambio (decisión D10); sus dueños deben actualizarlos:

| Guía | Qué quedó desactualizado |
|---|---|
| `HU-W1.3-01-api.md` §5.3 y §8 | «**El endpoint para generar la nueva oferta no existe aún**» y «Generar nueva oferta — No existe»: ahora existe `POST /v1/offers/{offer_id}/renewal` (solo para ofertas personalizadas `off-…`; una `demo-expired-*` sigue sin renovación). «No hay endpoint para leer el detalle de la oferta»: ahora existe `GET /v1/offers/{offer_id}` (también para `demo-*`, vista reducida). «Ofertas simuladas»: las personalizadas están en PostgreSQL. Una oferta reemplazada responde `422 offer_expired` al aceptarla, y una oferta sin dueño `403` |
| `HU-W1.1-02-api.md` | «**No hay endpoint de detalle de oferta**» (§8, fila «Detalle de la oferta ajustada en E3»): `GET /v1/offers/adj-{acceptance_id}` existe, con la vista reducida (`kind: adjusted`, `terms`/`conditions`/`profile` en `null`); la pantalla E3 puede abrirla desde «Ver la nueva oferta» |
| `HA-D02-api.md` §4 | «Monto asegurado, plazo y coberturas del evento de activación son de demostración … hasta HU-W1.2-03»: **siguen siendo de demostración**. El cobro usa ya la prima real de la oferta (`AsOffer().Premium`), pero `demoCoverageTerms` (`cmd/origination/billing.go`) no lee `terms`: es una tarea de HA-D02. «Las aceptaciones y ofertas siguen en memoria»: las ofertas personalizadas ya no |
