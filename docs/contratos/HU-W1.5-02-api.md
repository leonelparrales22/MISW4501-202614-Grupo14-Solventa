# Contrato de API — HU-W1.5-02 · Renovación en línea por el asegurado

> **Fuente para el front (Angular).** Describe lo que implementa el servicio `origination`
> (`internal/adapters/http/policies.go`, `internal/renewal`, `internal/adapters/postgres/{policies,renewals}.go`, migración `0012`).
> Contrato formal: `services/origination/api/openapi.yaml` (0.12.0). Alimenta las pantallas **A1 «Mis pólizas»**
> (`/originacion/polizas`), **A4 «Renovar póliza»** y **A4b «Renovación confirmada»** (`/originacion/polizas/:id/renovar`).

## 1. Flujo

```
cobertura activada ──▶ póliza (proyección: cliente, vigencia, prima, coberturas)

Mis pólizas ──GET /v1/policies──────────────────────────────▶ pólizas del asegurado, ventana y can_renew
Renovar     ──GET /v1/policies/{id}/renewal─────────────────▶ condiciones vigentes vs. recotizadas (409 fuera de la ventana)
«Renovar y pagar» ──POST /v1/policies/{id}/renewal──────────▶ renovación + cobro (201; 200 si ya estaba aceptada)
                      └─ cobro con canal renewal, con el medio de pago de la póliza (flujo de emisión de HA-D02)
Confirmada  ──GET /v1/policies/{id}/renewal/status (polling)▶ accepted → renewed | not_renewed
Pago rechazado ──POST /v1/acceptances/{charge_id}/charge/payment-method▶ otro medio, hasta retry_until, misma solicitud
```

- **Ventana.** Abre `RENEWAL_WINDOW_DAYS` (30) días antes de que termine la cobertura y cierra cuando termina. Fuera de ella no hay
  renovación: `GET` y `POST` responden `409 renewal_not_available` y `can_renew` es `false` (criterio 3).
- **Recotización.** El motor vuelve a tarifar con la edad de hoy, el mismo valor asegurado y el plazo de la renovación
  (`RENEWAL_TERM_MONTHS`, 12), con el perfil actual del asegurado. Si no puede (no hay cliente registrado con esa cuenta, no hay perfil o falla
  la tarifa) la renovación **repite las condiciones vigentes** y `requoted` es `false`: nunca se bloquea ni se tarifa con datos inventados.
- **Descuento por buena experiencia.** `discount_rate = 0.08` cuando la póliza no tuvo siniestros en el último año. Origination no tiene
  datos de siniestros: hoy siempre aplica (regla simulada, `renewal.ClaimsHistory` es el punto donde enchufar el servicio `claims`).
- **Emisión sin interrupción.** Aceptar guarda la renovación y el cobro de la prima (canal `renewal`, id `ren-<póliza>-<aaaammdd>`) y
  reutiliza el flujo de HA-D02 y HU-W1.3-03: al confirmarse el cobro nace una póliza nueva (`renewal_of` = la anterior) cuya vigencia
  **empieza cuando termina la anterior**, y se emite su documento contractual (criterio 2). No pasa por oferta ni suscripción.
- **Pago fallido.** El cobro rechazado queda como excepción visible en el monitor de HU-W1.3-04 (`GET /v1/issuances?channel=renewal`), la
  solicitud no se pierde y el asegurado puede entregar **otro medio de pago** hasta `retry_until` (`RENEWAL_PAYMENT_RETRY_HOURS`, 72)
  sin crear otra solicitud (criterios 4 y 6). Pasado el plazo el medio se rechaza (`409 charge_not_payable`) y la renovación queda `not_renewed` con
  `reason: payment_failed`.
- **Sin respuesta.** Un barrido (`RENEWAL_POLL_INTERVAL_MS`) registra `not_renewed` con `reason: no_response` cuando la ventana cierra sin
  renovación y avisa por correo, una sola vez, que la cobertura finalizó e indica si aplica período de gracia (`RENEWAL_GRACE_DAYS`, 0) (criterio 5).

## 2. Endpoints

Todos exigen el ID token de Cognito con rol `insured` y solo ven y renuevan **sus** pólizas (`403` si es de otro, `401` sin token). Llevan
`Cache-Control: no-store`.

### `GET /v1/policies` → `200`

> **Desde HU-M1.2-01 (0.13.0)** esta lista la arma la billetera: agrega las pólizas del núcleo de pólizas y los campos `source`, `line`,
> `product`, `insured_object`, `status`, `holder` y `partial`. Lo de abajo sigue valiendo; ver [`HU-M1.2-01-api.md`](HU-M1.2-01-api.md).

```json
{ "items": [ {
  "policy_id": "pol-acc-…", "holder_name": "Carolina Pérez", "premium": 142300, "currency": "COP", "insured_amount": 68000000,
  "coverages": [ { "code": "death", "insured_amount": 68000000 } ],
  "coverage_start": "2025-10-02T23:59:00Z", "coverage_end": "2026-10-02T23:59:00Z", "renewal_of": null,
  "renewal_window": { "opens_at": "2026-09-02T23:59:00Z", "closes_at": "2026-10-02T23:59:00Z" },
  "can_renew": true, "renewal": null
} ] }
```

`renewal` es el resumen de la renovación del periodo actual (ver abajo) cuando ya hay una. `can_renew` solo es `true` dentro de la
ventana y mientras el periodo no tenga renovación.

### `GET /v1/policies/{policy_id}/renewal` → `200` (pantalla A4)

```json
{
  "policy": { "policy_id": "pol-acc-…", "…": "…" },
  "current": { "premium": 142300, "currency": "COP", "insured_amount": 68000000, "coverages": [ … ], "coverage_start": "…", "coverage_end": "2026-10-02T23:59:00Z" },
  "renewal": { "premium": 149800, "currency": "COP", "insured_amount": 68000000, "coverages": [ … ], "coverage_start": "2026-10-02T23:59:00Z", "coverage_end": "2027-10-02T23:59:00Z" },
  "renewal_window": { "opens_at": "…", "closes_at": "…" },
  "requoted": true, "discount_rate": 0.08,
  "payment_method": { "method": "card", "brand": "visa", "last4": "1111", "installments": 1, "bank_code": null, "captured_at": "…" }
}
```

`payment_method` es el medio con el que se pagó la póliza (sin token) o `null`; «cambiar» lo reemplaza con la ruta de pago de abajo.
`409 renewal_not_available` fuera de la ventana o si la ventana cerró sin renovación; `409 renewal_closed` si el periodo ya se aceptó o se renovó.

### `POST /v1/policies/{policy_id}/renewal` → `201` / `200` (botón «Renovar y pagar»)

Sin cuerpo. Aceptar es el consentimiento a las condiciones mostradas. Idempotente por póliza y periodo: repetirlo devuelve la misma
renovación y el mismo cobro con `200`.

```json
{
  "renewal": { "renewal_id": "ren-pol-acc-…-20261002", "status": "accepted", "reason": null, "new_policy_id": null,
               "charge_id": "ren-pol-acc-…-20261002", "accepted_at": "…", "retry_until": "…", "closed_at": null },
  "terms": { "premium": 149800, "…": "…" }, "discount_rate": 0.08,
  "charge": { "acceptance_id": "ren-pol-acc-…-20261002", "status": "pending", "…": "como GET /v1/acceptances/{id}/charge" }
}
```

### `GET /v1/policies/{policy_id}/renewal/status` → `200` (pantalla A4b, por *polling* cada 2 s)

Mismo cuerpo. `renewal.status`:

| `status` | Qué pinta la pantalla |
|---|---|
| `accepted` | «Estamos procesando tu pago». Si `charge.status` es `failed` con `failure_reason: payment_rejected`: pago rechazado, ofrecer otro medio hasta `retry_until` |
| `renewed` | Renovación confirmada: `new_policy_id`, vigencia nueva y certificado renovado (`GET /v1/policies/{new_policy_id}/contract-document`) |
| `not_renewed` | La póliza no se renovó: `reason` es `no_response` o `payment_failed` |

`404 renewal_not_found` si el periodo no tiene renovación.

### Otro medio de pago: `POST /v1/acceptances/{charge_id}/charge/payment-method`

Es la ruta de HU-W1.3-02 (pantalla E4/E4c) con el `charge_id` de la renovación; no hay ruta nueva. Tras un rechazo carga el medio nuevo con
llave de idempotencia nueva; para una renovación, pasado `retry_until` responde `409 charge_not_payable`.

## 3. Datos

- `policy` (proyección de `CoverageActivated`, solo se agrega) y `renewal` (una fila por póliza y periodo, `UNIQUE (policy_id, period_end)`):
  migración `0012_policy_renewal.sql`. `charge_attempt` gana `renewal_of` y `coverage_not_before`.
- La renovación guarda las condiciones aceptadas (prima, valor asegurado, coberturas, descuento, si se recotizó), cuándo se aceptó y el plazo.

## 4. Lo que queda fuera

- **Siniestros reales**: el descuento es una regla simulada (no se consulta el servicio `claims`).
- **Sin suscripción**: la renovación no pasa por la revisión de un asesor (HU-W1.1-02); el motor la recotiza y el asegurado la acepta.
- El aviso de «no renovada» sale por correo con `EMAIL_URL`; sin proveedor se registra el lapso pero no se avisa. No hay *push* (HU-M3.2-02).
- Las pólizas anteriores a esta historia no se proyectan (no hay *backfill*) y un lapso solo se avisa si la póliza terminó en los últimos 30 días.
- La renovación **asistida** del asesor (HU-W1.5-03) y el listado de pólizas por vencer del asesor (HU-W1.5-01) son otras historias.
- El criterio 2 cita el motor de emisión (cobro HA-D02 y documento HU-W1.3-03); HU-W1.3-04 solo monitorea la excepción del criterio 4.
