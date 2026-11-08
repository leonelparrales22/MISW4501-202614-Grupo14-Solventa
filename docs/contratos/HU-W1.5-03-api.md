# Contrato de API — HU-W1.5-03 · Renovación asistida con recotización

> **Fuente para el front (Angular).** Describe lo que implementa el servicio `origination`
> (`internal/adapters/http/assisted_renewals.go`, `internal/renewal/assisted.go`, `internal/renewal/origin.go`,
> `internal/renewal/pricing.go`, `internal/domain/assisted_renewal.go`, migración `0014_assisted_renewal.sql`). Contrato formal:
> `services/origination/api/openapi.yaml` (0.15.0). Alimenta las pantallas **S7 «Renovación asistida»** y **S7b «Renovación
> formalizada»** del portal del asesor (`/originacion/renovaciones/:id`, con `:id` = `policy_id`), a las que se entra desde el listado de
> HU-W1.5-01 con «Renovar» o «Ver».

## 1. Flujo

```
Renovaciones (HU-W1.5-01) ──«Renovar»/«Ver»──▶ GET /v1/policies/{policy_id}/assisted-renewal
   │
   ├─ «Recotizar» ──POST …/assisted-renewal/requotes {consent_affirmed}──▶ consentimiento ──▶ fuentes (caché de respaldo) ──▶ motor v1
   │                                                                       (audit o casilla del asesor)
   ├─ medio «Código de confirmación» ──POST …/assisted-renewal/consent {requote_id}──▶ correo al titular (código de 6 dígitos)
   │
   └─ «Formalizar renovación» ──POST …/assisted-renewal/formalization──▶ renovación del periodo (HU-W1.5-02) + registro del asesor
                                                                          ──▶ cobro idempotente (HA-D02) ──▶ póliza nueva y certificado
                                                                              (HU-W1.3-03)
```

- **Qué pólizas.** Solo las que emite originación (línea `life`); las del núcleo de pólizas no tienen fila y responden `404
  policy_not_found` (decisión D-2 a).
- **Una renovación por póliza y periodo.** La del asesor y la del asegurado en línea (HU-W1.5-02) son la misma fila `renewal`
  (`UNIQUE (policy_id, period_end)`): si el asegurado renovó primero, el asesor recibe `409 renewal_closed`; si otro asesor formalizó
  primero, `409 already_formalized` (decisión D-3 a).
- **Consentimiento antes de consultar nada (decisiones D-8 b y D-13 c).** `consent_mode` dice cómo:
  - `audit`: la póliza nació en el canal embebido (o no se sabe): `audit` verifica que el cliente tenga consentimiento activo. Sin él,
    `422 consent_required`; si `audit` no responde, `503 consent_unverifiable`. En los dos casos no se consulta ninguna fuente ni la caché.
  - `advisor_affirmation`: la póliza nació en el canal asistido: la pantalla muestra la casilla «El cliente autorizó consultar sus datos
    financieros para esta renovación» y el cuerpo debe traer `consent_affirmed: true`. Sin ella, `422 validation_failed` con el campo
    `consent_affirmed` y nada consultado. La recotización guarda quién afirmó y cuándo (`consent_basis: advisor_affirmed`,
    `consent_affirmed_at`).
- **Recotizar consulta primero las fuentes (decisión D-5 b).** Aunque haya un perfil reciente en caché. Si las fuentes no responden, usa
  el último perfil en caché (30 días como máximo) y lo dice: `mode: provisional`, `source: cache`, `notice: last_profile_in_use` y
  `profile_date` (CA-5). Sin un perfil reciente, el actuarial base: `source: base_actuarial`, `notice: personalization_unavailable`
  (decisión D-7). La pantalla muestra «Recotizar» desactivado mientras espera.
- **La recotización se guarda (decisión D-4 a).** Es inmutable, vale 24 horas (`expires_at`) y solo se formaliza la última del periodo:
  una anterior responde `422 requote_superseded`; una vencida, `422 requote_expired`.
- **Aceptación del cliente (CA-2, decisión D-9 a).** Los tres medios de la compra (HU-W1.1-03): `recorded_call` y `remote_signature`
  con su referencia (obligatoria) y `confirmation_code`, con el código que el titular recibe por correo (la referencia es opcional).
- **Sin medio de pago registrado (decisión D-10 a).** Se puede recotizar, pero no pedir el código ni formalizar: `blocked_reason:
  payment_method_missing` y `409 payment_method_missing`. El asegurado puede renovar desde su portal.
- **Estado del cobro (decisión 01·D-3 a+).** El `GET` trae el cobro de la renovación (de los dos canales) y `renewal.retry_until`: si el
  pago falló, la pantalla dice «El pago falló el {{charge.last_failure_at}}; el cliente puede reintentar con otro medio desde su portal
  hasta {{renewal.retry_until}}». El asesor no reintenta por el cliente.
- **Nombre del asesor.** `formalization.advisor_name` es el claim `name` del *token*; `null` si la cuenta no lo tiene (el pool permanente
  no lo tiene): entonces la pantalla muestra `advisor_email`.

## 2. Endpoints

ID token de Cognito con rol **`advisor`**: sin token `401`, con otro rol (`insured`, `operations`, `partner`) `403`. Todas llevan
`Cache-Control: no-store`. Publicadas en AWS por el recurso `/v1/policies/{proxy+}` del API Gateway (sin cambios de infraestructura). Se
montan con la renovación en línea (PostgreSQL y pasarela); sin `AUDIT_URL` la recotización de una póliza de origen embebido responde
`503 consent_unverifiable` y sin `EMAIL_URL` el código responde `503 code_delivery_unavailable`.

### `GET /v1/policies/{policy_id}/assisted-renewal` → `200`

Todo lo que pintan S7, S7b y «Ver»; la página funciona al recargarse. Ejemplo (ilustrativo) de una póliza recotizada en modo degradado:

```json
{
  "policy": {
    "policy_id": "pol-acc-7f2c", "line": "life",
    "holder": { "full_name": "Carolina Pérez Ruiz", "document_type": null, "document_id": "1020345678" },
    "premium": 142300, "currency": "COP", "insured_amount": 68000000,
    "coverages": [{ "code": "death", "insured_amount": 68000000 }],
    "coverage_start": "2025-10-02T23:59:00Z", "coverage_end": "2026-10-02T23:59:00Z", "renewal_of": null
  },
  "renewal_window": { "opens_at": "2026-09-02T23:59:00Z", "closes_at": "2026-10-02T23:59:00Z", "open": true },
  "current": { "premium": 142300, "currency": "COP", "insured_amount": 68000000, "coverages": [ … ],
               "coverage_start": "2025-10-02T23:59:00Z", "coverage_end": "2026-10-02T23:59:00Z" },
  "payment_method": { "method": "card", "brand": "visa", "last4": "1111", "installments": 1, "bank_code": null,
                      "payer_type": null, "captured_at": "2025-10-01T15:00:00Z" },
  "requote": { "requote_id": "rq-…", "status": "valid", "notice": "last_profile_in_use", "…": "ver POST …/requotes" },
  "renewal": null,
  "formalization": null,
  "charge": null,
  "consent_mode": "audit",
  "can_requote": true,
  "can_formalize": true,
  "blocked_reason": null
}
```

| Campo | Qué dice |
|---|---|
| `requote` | La última recotización del periodo, con `status` `valid` o `expired`; `null` si no hay |
| `renewal` | La renovación del periodo (`accepted` → `renewed` / `not_renewed`) con `channel` (`advisor` o `insured`), `retry_until` y `new_policy_id`; `null` si no hay |
| `formalization` | El registro del asesor: `advisor_id`, `advisor_email`, `advisor_name` (o `null`), `formalized_at`, `requote_id` y `acceptance{method, accepted_at, reference}`; `null` si renovó el asegurado o no hay renovación |
| `charge` | El cobro de la renovación: `status` (`pending`, `in_progress`, `confirmed`, `failed`), `failure_reason`, `decline_code`, `last_failure_at`, `attempts` y `policy_id` (la póliza nueva al confirmarse) |
| `blocked_reason` | `renewal_not_available` (fuera de ventana o no renovada), `renewal_closed` (el periodo ya tiene renovación), `payment_method_missing`, o `null` |
| `can_requote` | En ventana y sin renovación del periodo |
| `can_formalize` | Sin bloqueo y con una recotización vigente |

Para la emisión en curso se reutilizan, sin cambios, `GET /v1/policies/{new_policy_id}/issuance-status` y `contract-document`, que
admiten `advisor`.

### `POST /v1/policies/{policy_id}/assisted-renewal/requotes` → `201`

Cuerpo opcional: `{"consent_affirmed": true}` (solo cuenta para `consent_mode: advisor_affirmation`). Ejemplo (ilustrativo) con Open
Finance caído:

```json
{
  "requote_id": "rq-3f9a0c…", "created_at": "2026-09-16T21:02:00Z", "expires_at": "2026-09-17T21:02:00Z", "status": "valid",
  "current": { "premium": 142300, "currency": "COP", "insured_amount": 68000000, "coverages": [ … ],
               "coverage_start": "2025-10-02T23:59:00Z", "coverage_end": "2026-10-02T23:59:00Z" },
  "renewal": { "premium": 149800, "currency": "COP", "insured_amount": 68000000, "coverages": [ … ],
               "coverage_start": "2026-10-02T23:59:00Z", "coverage_end": "2027-10-02T23:59:00Z" },
  "premium_change_rate": 0.0527, "discount_rate": 0.08, "requoted": true, "rating_version": "v1",
  "applied_rules": [{ "code": "base_monthly_rate", "value": 0.00045 }, { "code": "age_factor", "value": 1.32 }, … ],
  "mode": "provisional", "source": "cache", "profile_date": "2026-08-27T21:02:00Z", "unavailable_source": "open_finance",
  "notice": "last_profile_in_use",
  "sources": [{ "source": "open_finance", "status": "timeout", "latency_ms": 120 },
              { "source": "open_data", "status": "ok", "latency_ms": 88 }, { "source": "kyc", "status": "ok", "latency_ms": 64 }],
  "consent_basis": "audit_verified", "consent_affirmed_at": null
}
```

Nunca viajan el puntaje, la categoría del perfil ni el cliente del aliado. `applied_rules` son los factores del motor (`rating.Explain`).
`requoted: false` repite las condiciones vigentes (la póliza no tiene cliente o fecha de nacimiento) y entonces `mode`, `source`,
`rating_version` y `notice` son `null`.

### `POST /v1/policies/{policy_id}/assisted-renewal/consent` → `200`

Cuerpo `{"requote_id": "rq-…"}`. Respuesta `{"requote_id", "sent_to": "ca***@example.com", "expires_at"}`. El código vale 10 minutos (y
nunca más que la recotización); hasta 3 envíos por recotización; cada envío reemplaza al anterior. El código nunca vuelve al asesor.

### `POST /v1/policies/{policy_id}/assisted-renewal/formalization` → `201` / `200`

```json
{
  "requote_id": "rq-3f9a0c…",
  "acceptance": { "method": "recorded_call", "accepted_at": "2026-09-16T21:05:00Z", "reference": "GRAB-2026-88931" },
  "confirmation_code": "482913"
}
```

`confirmation_code` solo con `method: confirmation_code`. `accepted_at` no puede estar más de 5 minutos en el futuro ni antes del minuto
de la recotización. Responde el cuerpo del `GET` (`201` y `Location`); el mismo asesor que repite recibe `200` con la misma renovación.

## 3. Errores

| HTTP | `error` | Cuándo |
|---|---|---|
| 400 | `invalid_request` | `policy_id` de más de 128 caracteres o cuerpo que no es JSON |
| 401 / 403 | `unauthorized` / `forbidden` | Sin *token* / rol distinto de `advisor` |
| 404 | `policy_not_found` | La póliza no es de originación |
| 404 | `requote_not_found` | La recotización no existe o es de otra póliza o periodo |
| 409 | `renewal_not_available` | Fuera de ventana, o el periodo terminó como no renovado |
| 409 | `renewal_closed` | El asegurado ya renovó el periodo |
| 409 | `already_formalized` | Otro asesor formalizó el periodo (o, al pedir el código, ya está formalizado) |
| 409 | `payment_method_missing` | La póliza no tiene medio de pago registrado |
| 422 | `validation_failed` | Campos inválidos, todos a la vez: `requote_id`, `acceptance.method`, `acceptance.accepted_at`, `acceptance.reference`, `confirmation_code`, `consent_affirmed` |
| 422 | `consent_required` | Origen embebido sin consentimiento activo en `audit`; nada consultado |
| 422 | `requote_expired` / `requote_superseded` | La recotización pasó de 24 horas / no es la última del periodo |
| 422 | `consent_code_required` / `invalid_confirmation_code` / `confirmation_code_expired` | No se pidió código / no es el enviado / venció |
| 429 | `too_many_codes` / `too_many_attempts` | 4.º envío / 6.º intento de la misma recotización |
| 503 | `consent_unverifiable` | `audit` no respondió (o no está configurado) |
| 503 | `code_delivery_unavailable` | El correo no está configurado o no respondió |
| 503 | `renewal_unavailable` | Falla de la base o del servicio; reintentar |
