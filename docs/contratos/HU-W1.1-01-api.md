# Contrato de API — HU-W1.1-01 · Cotización asistida por asesor

> **Fuente para el front (Angular).** Describe lo que implementa el servicio `origination`
> (`internal/adapters/http/assisted_quotes.go`, `internal/orchestrator`, `internal/rating`, `internal/domain/quote.go`). Contrato formal:
> `services/origination/api/openapi.yaml`. Las rutas son **humanas**: exigen el ID token de Cognito y el rol `advisor`
> (`401` sin token válido, `403` con otro rol). Todas devuelven `Cache-Control: no-store`.

## 1. Flujo

```
Pantalla S1 (datos del cliente)                         origination
───────────────────────────────                         ───────────────────────────────────────────
documento ─ «Buscar» ──GET /v1/customers?document_id=──▶ 200 cliente (se precargan los campos) · 404 no existe
«Cotizar» ──POST /v1/assisted-quotes──────────────────▶ 201 cotización (vigente 24 h)
                                                         ├─ campo faltante/ inválido ─▶ 422 validation_failed + fields[] (no se cotiza)
                                                         ├─ sin consentimiento ───────▶ 422 consent_required
                                                         └─ fuentes caídas ───────────▶ 201 con mode=provisional + notice (S2b)
Pantalla S2 (resultado) ◀──GET /v1/assisted-quotes/{quote_id}──
                                                         └─ pasadas 24 h: status=expired, can_formalize=false,
                                                            blocked_reason=quote_expired (la UI ofrece una cotización nueva)
```

## 2. Endpoints

### `POST /v1/assisted-quotes` → `201`

```json
{
  "document_id": "1032456789", "full_name": "Ana María Pérez", "email": "ana@example.com",
  "birth_date": "1990-05-20", "insured_amount": 250000000, "term_months": 180, "consent_active": true
}
```

| Campo | Regla | Códigos de error |
|---|---|---|
| `document_id` | 5–20 letras o dígitos | `required`, `invalid_format` |
| `full_name` | 3–120 caracteres, con letras | `required`, `invalid_format` |
| `email` | dirección simple con dominio | `required`, `invalid_format` |
| `birth_date` | `YYYY-MM-DD`; edad entre 18 y 70 años | `required`, `invalid_format`, `out_of_range` |
| `insured_amount` | 10 000 000 – 2 000 000 000 (COP) | `required`, `out_of_range` |
| `term_months` | entero 12 – 360 | `required`, `out_of_range` |
| `consent_active` | `true` (el asesor afirma el consentimiento del cliente) | `required` |

Respuesta (`Location: /v1/assisted-quotes/{quote_id}`); es la misma que devuelve el `GET`:

```json
{
  "quote_id": "3f1c…", "customer_id": "9a7e…",
  "customer": { "document_id": "1032456789", "full_name": "Ana María Pérez", "email": "ana@example.com" },
  "insured_amount": 250000000, "term_months": 180, "premium": 132800, "currency": "COP",
  "coverages": [ { "code": "death", "insured_amount": 250000000 }, { "code": "total_permanent_disability", "insured_amount": 250000000 } ],
  "rating_version": "v1",
  "mode": "normal", "source": "external_sources", "profile": { "score": 0.44, "category": "standard" },
  "profile_date": "2026-10-16T14:55:00Z", "unavailable_source": null, "notice": null,
  "created_at": "2026-10-16T15:00:00Z", "expires_at": "2026-10-17T15:00:00Z",
  "status": "valid", "can_formalize": true, "blocked_reason": null
}
```

- `premium` es **mensual**, redondeada a 100 pesos; `rating_version` dice con qué tabla se calculó.
- **Modo degradado (S2b):** `mode: "provisional"`, `unavailable_source: "open_finance"` y `notice` =
  `last_profile_in_use` (se usó el último perfil en caché, `profile_date` indica de cuándo) o `personalization_unavailable` (cliente sin
  perfil previo: perfil actuarial base). Los textos los traduce el front por el código.
- `source`: `external_sources`, `open_finance`, `cache` (perfil completo reutilizado o de respaldo) o `base_actuarial`.

Errores:

| Estado | `error` | Cuándo |
|---|---|---|
| `400` | `invalid_request` | cuerpo que no es JSON válido |
| `422` | `validation_failed` | `fields: [{ "field": "email", "code": "invalid_format" }, …]` — **todos** los campos con error a la vez |
| `422` | `consent_required` | `consent_active: false` |
| `503` | `quote_unavailable` | falla interna; reintentar |

### `GET /v1/assisted-quotes/{quote_id}` → `200`

Misma forma que el `POST`. Una cotización vencida sigue siendo legible para explicar el motivo. `404 quote_not_found` si no existe.

### `GET /v1/customers?document_id=…` → `200`

```json
{ "customer_id": "9a7e…", "document_id": "1032456789", "full_name": "Ana María Pérez", "email": "ana@example.com", "birth_date": "1990-05-20" }
```

`404 customer_not_found` si el documento no está registrado; `422 validation_failed` si el formato no es válido. Al cotizar con un
documento ya registrado se **actualizan** los datos de contacto y se conserva el `customer_id` (la caché de perfil depende de él).

## 3. Lo que todavía es provisional

- Clientes y cotizaciones están **en memoria** (se pierden si el servicio se reinicia) hasta contar con PostgreSQL.
- El motor de rating `v1` es una tabla actuarial mínima detrás del puerto `Rater`; HU-W1.2-03 puede ampliarla sin cambiar este contrato.
- `consent_active` lo afirma el asesor mientras `audit` no exponga la consulta de consentimientos (HU-W1.2-01).
- La formalización de la compra es HU-W1.1-03: aquí solo se expone `can_formalize`.
