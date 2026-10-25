# Contrato de API — HU-W1.1-03 · Formalización de la compra por el asesor

> **Fuente única para el front (Angular).** Describe el código real del servicio `origination`
> (`internal/adapters/http/formalization.go`, `internal/formalization`, `internal/adapters/postgres/{quotes,formalizations}.go`,
> `internal/adapters/email/consent_notifier.go`, `cmd/origination/main.go`). Contrato formal: `services/origination/api/openapi.yaml`
> (versión 0.11.0). Migración `0011_purchase_formalization.sql`.

---

## 1. Resumen del flujo

```
Asesor (portal)                          origination                                         Cliente
───────────────                          ──────────────────────────────────────────────      ───────────────
GET /v1/assisted-quotes/{id} ───────────▶ cotización vigente (can_formalize=true)
(si acepta con código) POST …/{id}/consent ─▶ genera código de 6 dígitos, guarda solo su hash ──▶ correo con el código
                                          └─ responde sent_to (correo enmascarado) y expires_at
                (el cliente le dice el código al asesor)
POST …/{id}/formalization {acceptance} ─▶ valida vigencia, aceptación (y código si aplica) ─▶ registra la formalización (única por cotización)
                                          ├─ guarda la oferta de la cotización y asigna dueño si el cliente tiene cuenta
                                          └─ registra la aceptación con canal `advisor` ─▶ suscripción ─▶ cobro HA-D02 ─▶ emisión
GET /v1/issuances (HU-W1.3-04) ◀─────────  la emisión aparece con canal `advisor`
```

| Criterio | Cubierto | Cómo |
|---|---|---|
| CA-1 Confirmar datos y condiciones de una cotización vigente y formalizar | Sí | `GET /v1/assisted-quotes/{id}` (datos y condiciones) y `POST …/formalization` |
| CA-2 Vencida: no se formaliza y se ofrece una cotización nueva | Sí | `422 quote_expired` con `suggest_new_quote: true` (también al pedir el código) |
| CA-3 Se dispara la emisión sin pasos manuales | Sí | la formalización registra la aceptación con canal `advisor`; el procesador de aceptaciones y el cobro siguen solos |
| CA-4 Queda registrado asesor, fecha y cotización de origen | Sí | `formalization` (*append-only*): `advisor_id`, `formalized_at`, `quote_id`; `GET …/formalization` |
| CA-5 Consentimiento explícito del cliente | Sí | el asesor registra cómo y cuándo aceptó el cliente: **llamada grabada** o **firma remota** (con su referencia) o **código de confirmación** enviado por correo al cliente y digitado por el asesor (solo se guarda su *hash*) |
| CA-6 Dos asesores a la vez: solo una formalización | Sí | `formalization.quote_id` es único: uno completa (`201`), los demás reciben `409 already_formalized` |

---

## 2. Endpoints

Solo el rol **`advisor`**; otro rol recibe `403` y sin token `401`. Todas responden con `Cache-Control: no-store`.

### 2.1 `POST /v1/assisted-quotes/{quote_id}/consent` → `200`

Solo para el medio `confirmation_code`. Sin cuerpo. Envía al correo del cliente (el de la cotización) un código de **6 dígitos**.

```json
{ "quote_id": "5d0c…", "sent_to": "an***@example.com", "expires_at": "2026-10-25T15:10:00Z" }
```

- El código **nunca** vuelve al asesor: solo el correo enmascarado. Vale **10 minutos** (y nunca más allá de la vigencia de la cotización).
- Un código nuevo reemplaza al anterior; máximo **3 códigos** por cotización (`429 too_many_codes`).

### 2.2 `POST /v1/assisted-quotes/{quote_id}/formalization` → `201` | `200`

```json
{
  "acceptance": { "method": "recorded_call", "accepted_at": "2026-10-25T14:59:00Z", "reference": "GRAB-2026-88910" }
}
```

Con el medio `confirmation_code`, la `reference` es opcional y se agrega el código: `"confirmation_code": "482913"`.

| Campo | Regla |
|---|---|
| `acceptance.method` | `recorded_call` \| `remote_signature` \| `confirmation_code` |
| `acceptance.accepted_at` | RFC 3339; no futura (5 min de tolerancia) ni anterior al minuto en que se generó la cotización |
| `acceptance.reference` | 1–64 caracteres `A-Za-z0-9._:/#-`; obligatoria salvo con `confirmation_code` |
| `confirmation_code` | 6 dígitos; obligatorio solo con `confirmation_code` |

```json
{
  "formalization_id": "frm-5d0c…", "quote_id": "5d0c…", "offer_id": "off-q-5d0c…",
  "acceptance_id": "e2b1…", "customer_id": "9a7e…", "advisor_id": "6f1c…", "advisor_email": "laura@example.com",
  "consent_method": "recorded_call", "accepted_at": "2026-10-25T14:59:00Z", "consent_reference": "GRAB-2026-88910",
  "formalized_at": "2026-10-25T15:03:11Z", "status": "registered"
}
```

- `201` la primera vez (con `Location`); `200` si el mismo asesor repite la llamada: devuelve el mismo registro, **sin** otra aceptación.
- `status` es el de la aceptación (`registered`, `issuance_pending`, `in_review`); el seguimiento sigue en `GET /v1/acceptances/{id}` y en el monitor de emisiones.
- Con `confirmation_code`, un código se puede intentar **5 veces** por cotización, acierte o no.

| Estado | `error` | Cuándo |
|---|---|---|
| `400` | `invalid_request` | cuerpo que no es JSON |
| `404` | `quote_not_found` | la cotización no existe |
| `409` | `already_formalized` | otro asesor formalizó primero esta cotización (CA-6) |
| `422` | `validation_failed` | `fields: [{ "field": "acceptance.reference", "code": "required" }, …]` — **todos** los campos con error a la vez (`required`, `invalid_format`, `out_of_range`) |
| `422` | `quote_expired` | la cotización venció; trae `expires_at` y `suggest_new_quote: true` (CA-2) |
| `422` | `consent_code_required` | con `confirmation_code`, no se pidió ningún código |
| `422` | `invalid_confirmation_code` | el código no es el enviado |
| `422` | `confirmation_code_expired` | pasaron más de 10 minutos desde el envío |
| `429` | `too_many_attempts` / `too_many_codes` | se agotaron los intentos o los envíos |
| `503` | `code_delivery_unavailable` | (solo al pedir el código) no hay proveedor de correo (`EMAIL_URL`) o está caído |
| `503` | `formalization_unavailable` | falla interna; reintentar |

### 2.3 `GET /v1/assisted-quotes/{quote_id}/formalization` → `200`

Misma forma que el `POST`. `404 formalization_not_found` si la cotización no se formalizó.

---

## 3. Decisiones y límites

- **Consentimiento (CA-5):** el asesor registra el medio, la fecha y hora y la referencia (el mockup S3 «Aceptación del cliente»). El
  servicio **no** valida que la grabación o la firma existan: guarda su referencia para la auditoría. Solo el medio `confirmation_code`
  se verifica: el código viaja por el correo de la cotización (el mismo proveedor de HU-W1.3-03, `EMAIL_URL`); en local, el stub de correo.
  Sin proveedor de correo siguen disponibles la llamada grabada y la firma remota.
- **Identidad del asesor:** el token de Cognito no trae nombre; el registro guarda `advisor_id` (el `sub`) y `advisor_email`.
- **Dueño de la oferta:** si el cliente ya tiene cuenta (registro HU-M1.1-01), su `sub` queda como dueño de la oferta y de la aceptación:
  él puede dar el medio de pago y ver su póliza. Si no la tiene, la oferta queda sin dueño hasta que se registre; el cobro espera el
  medio de pago (`awaiting_payment_method`) y el monitor de HU-W1.3-04 lo muestra.
- **Cotizaciones en PostgreSQL:** con `DATABASE_URL`, las cotizaciones (HU-W1.1-01) pasan a `assisted_quote` (inmutable) y sobreviven a
  un reinicio; sin ella, en memoria como antes.
- **Idempotencia y recuperación:** la oferta tiene el id `off-q-{quote_id}`; la formalización, `frm-{quote_id}`. Repetir la llamada completa
  lo que haya quedado a medias (oferta, dueño, aceptación) sin duplicar nada.
- **Canal `advisor`:** nuevo valor del canal de la aceptación; aparece en `GET /v1/issuances` (filtro `channel=advisor`) y en la
  aceptación. Un cliente no puede elegirlo: `POST /v1/offers/{id}/acceptance` sigue aceptando solo `web` y `mobile`.
- **Fuera de alcance:** alerta real si el correo falla, reenvío por SMS, validar la grabación o la firma contra un sistema externo, y que el cliente vea la formalización en su propia app.
