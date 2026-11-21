# Contrato de API — HU-W1.6-01 · Cancelación de póliza con reembolso prorrateado

> **Fuente para el portal web (Angular).** Describe la cancelación de una póliza a solicitud del asegurado: el cálculo del reembolso antes
> de confirmar (A5), la confirmación y lo que el asegurado lee después en el detalle de la póliza (A5b) y en «Mis pólizas» (A1). Contrato
> formal: `services/origination/api/openapi.yaml` (0.20.0). Reutiliza el modelo de cancelación de HU-W1.6-02
> ([`HU-W1.6-02-api.md`](HU-W1.6-02-api.md)) con un motivo nuevo. Sin evento nuevo: `docs/events/README.md` no cambia.

## 1. Reglas acordadas

La prima de una póliza de originación es **mensual** (HU-W1.6-02 §1): el cobro de la emisión paga el mes 1 y cada mes siguiente es una
cuota. La ficha de la historia no fija el mecanismo del cálculo (INVEST *Negociable*); esta es la regla acordada:

| Regla | Valor |
|---|---|
| Qué pólizas | Solo las de originación (vida hipotecario). Las del núcleo de pólizas no tienen prima en el sistema: responden `404 policy_not_found`, igual que una ajena o una que no existe |
| Fecha efectiva | Hoy, **al final del día en hora de Colombia** (23:59:59). La póliza queda `cancelled` desde la confirmación y su cobertura termina en la fecha efectiva |
| Periodo pagado en curso | El mes `n` de la prima tal que su vencimiento ya llegó y el del mes `n+1` todavía no; termina en el vencimiento siguiente o en el fin de la vigencia si llega antes. Son las mismas fronteras de las cuotas |
| Base del reembolso | Lo **efectivamente pagado** por ese mes: el cobro de la emisión si es el mes 1; la cuota pagada si es un mes posterior. Nunca la prima vigente: un endoso que cambió la prima no cobra la diferencia del mes en curso, así que tampoco se devuelve sobre ella |
| Días | `D` = días calendario del periodo (del día de inicio al día anterior al siguiente vencimiento); `U` = días usados, del inicio al día de la cancelación **inclusive**; no utilizados `N = D − U` |
| Monto | `pagado × N / D`, **truncado al peso** (nunca se devuelve más de lo no usado; la diferencia es menor a un peso) |
| Porcentaje | `N / D × 100`, con un decimal |
| Gastos no reembolsables | Ninguno |
| Mes en curso sin pagar | Una cuota en su periodo de gracia, o vencida y aún sin facturar: no hay prima pagada sin usar → reembolso **0**, `refund: not_applicable`; la cancelación se permite y la cuota se cierra sin cobrarse |
| A dónde vuelve | Al medio de pago de la emisión (tarjeta: marca y últimos cuatro; PSE: banco), sin el token |
| Referencia | `REF-<año civil de Colombia>-<número de al menos cinco dígitos>`, consecutiva por año; solo si hay algo que reembolsar |
| Procesamiento | El reembolso queda registrado con la cancelación (`in_process`) y se envía a la pasarela en segundo plano, con la clave `reembolso-<póliza>` (nunca aleatoria) y reintentos con espera creciente dentro de la ventana del cobro (`RETRY_WINDOW_SECONDS`, 24 h). Termina `completed` o, si la pasarela lo rechaza o la ventana vence, `failed`; nunca queda en el limbo |
| Motivos | Lista fija: `property_sold` (Vendí el inmueble), `mortgage_paid` (Pagué el crédito hipotecario), `better_offer` (Encontré una mejor oferta), `no_longer_needed` (Ya no necesito el seguro), `financial_reasons` (Razones económicas), `other` (Otro motivo); comentarios opcionales de hasta 500 caracteres |
| Constancia y correo | No hay: ni constancia descargable ni correo de confirmación |

Ejemplo (prima 142.300 COP, vigencia desde el 18/11):

| Cancelación | `D` | `U` | `N` | Porcentaje | Reembolso |
|---|---|---|---|---|---|
| 18/11 (el día de la compra) | 30 | 1 | 29 | 96,7 % | **137.556 COP** |
| 03/12 | 30 | 16 | 14 | 46,7 % | **66.406 COP** |
| 20/12, con la cuota de diciembre en gracia | 31 | 3 | 28 | 90,3 % | **0** (no aplica) |

Además:

- **Una sola vez:** de varias confirmaciones simultáneas solo una cancela; las demás responden `409 policy_already_cancelled`. La
  cancelación por no pago y la del asegurado comparten la misma fila: la que llega primero gana.
- **Renovación en curso:** si el periodo actual tiene la renovación aceptada (cobro en curso) o ya renovada, la póliza no se cancela
  (`409 renewal_in_progress`).
- **Cuota en cobro:** mientras el barrido de las cuotas está cobrando un mes de la póliza, la confirmación responde
  `409 payment_in_progress` (dura lo que el *lease* del barrido; se reintenta en unos minutos).
- **Lo que deja de pasar:** una póliza cancelada no se renueva, no recibe alertas de vencimiento, no admite solicitudes de modificación
  ni endosos, y no se le cobran más cuotas. El asesor no la ve como «Cancelada por no pago» ni la cuenta en ese indicador, y el asegurado
  no recibe el correo de la cancelación por no pago.

## 2. Flujo

1. A2 → «Cancelar póliza» → A5 pide `GET /v1/policies/{policy_id}/cancellation/quote` y muestra el cálculo.
2. El asegurado elige el motivo, marca «Entiendo que la cancelación es definitiva» y confirma: `POST /v1/policies/{policy_id}/cancellation`
   con el monto que vio.
3. Con `201`, el portal vuelve al detalle (`GET /v1/policies/{policy_id}`), que muestra A5b con el reembolso «En proceso»; al recargar,
   cuando la pasarela lo confirma, «Reembolsado».
4. Con `409 refund_changed` (cambió el día o entró un pago), el portal muestra el monto nuevo de `quote` y pide confirmar otra vez.

## 3. `GET /v1/policies/{policy_id}/cancellation/quote` (A5) — nueva

```json
{
  "policy_id": "VH-2026-004512",
  "effective_at": "2026-11-19T04:59:59Z",
  "paid_period": { "month": 1, "kind": "issuance", "start": "2026-11-18T15:00:00Z", "end": "2026-12-18T15:00:00Z",
    "paid_amount": 142300, "currency": "COP" },
  "unused_days": 29,
  "period_days": 30,
  "prorate_percent": 96.7,
  "refund_amount": 137556,
  "currency": "COP",
  "refund_method": { "method": "card", "brand": "visa", "last4": "1111", "bank_code": null },
  "payment_status": "up_to_date"
}
```

- `payment_status: payment_pending` cuando hay una cuota en gracia (el reembolso es 0).
- Errores: `400 invalid_request` (id de más de 128 caracteres), `401`, `403` (otro rol), `404 policy_not_found`,
  `409 policy_not_in_force` (vencida o aún no iniciada), `409 policy_already_cancelled`, `409 renewal_in_progress`,
  `503 cancellation_unavailable`. Sin caché (`Cache-Control: no-store`).

## 4. `POST /v1/policies/{policy_id}/cancellation` (A5) — nueva

```json
{ "reason": "property_sold", "comments": "Vendí el apartamento", "refund_amount": 137556, "accept_terms": true }
```

`201` con la póliza cancelada (cabecera `Location: /v1/policies/{policy_id}`):

```json
{
  "policy_id": "VH-2026-004512",
  "status": "cancelled",
  "cancellation": {
    "reason": "insured_request",
    "cancelled_at": "2026-11-18T20:00:00Z",
    "effective_at": "2026-11-19T04:59:59Z",
    "refund": "in_process",
    "month": null, "due_at": null, "grace_ends_at": null, "amount": null, "attempts": null, "email_sent_at": null,
    "currency": "COP",
    "insured_reason": { "code": "property_sold", "comments": "Vendí el apartamento" },
    "refund_detail": {
      "reference": "REF-2026-00001",
      "amount": 137556,
      "currency": "COP",
      "method": { "method": "card", "brand": "visa", "last4": "1111", "bank_code": null },
      "registered_at": "2026-11-18T20:00:00Z",
      "completed_at": null,
      "paid_period": { "month": 1, "kind": "issuance", "start": "2026-11-18T15:00:00Z", "end": "2026-12-18T15:00:00Z",
        "paid_amount": 142300, "currency": "COP" },
      "unused_days": 29,
      "period_days": 30,
      "prorate_percent": 96.7
    }
  }
}
```

- `400 invalid_request`: el cuerpo no es JSON o trae un campo que el contrato no nombra.
- `422 invalid_request` con `fields` (`reason`, `comments`, `refund_amount`, `accept_terms`; códigos `required`, `invalid`, `too_long`):
  motivo fuera de la lista, comentarios de más de 500 caracteres, monto negativo o `accept_terms` distinto de `true`.
- `409 refund_changed` con `quote` (la cotización nueva): **el monto que confirma el asegurado es el que se registra**; si no coincide no se
  escribe nada.
- `409 payment_in_progress`, `409 policy_already_cancelled`, `409 policy_not_in_force`, `409 renewal_in_progress`, `404`, `503` como en §3.

## 5. `GET /v1/policies` y `GET /v1/policies/{policy_id}` (A1, A2, A5b) — cambios

`cancellation` ya existía (HU-W1.6-02) y ahora cubre los dos motivos:

| Campo | `non_payment` | `insured_request` |
|---|---|---|
| `reason` | `non_payment` | `insured_request` |
| `effective_at` (nuevo) | igual a `cancelled_at` | fin del día de la cancelación |
| `refund` | `not_applicable` | `not_applicable` (nada pagado), `in_process`, `completed` o `failed` |
| `month`, `due_at`, `grace_ends_at`, `amount`, `attempts` | la cuota impaga | `null` |
| `email_sent_at` | el correo al asegurado | `null` |
| `refund_detail` (nuevo) | `null` | el reembolso, como en §4 |
| `insured_reason` (nuevo) | `null` | `{code, comments}` |

La póliza queda `status: cancelled` y `can_renew: false` con cualquiera de los dos motivos.

| `refund` | A5b muestra |
|---|---|
| `in_process` | «En proceso» |
| `completed` | «Reembolsado» (`refund_detail.completed_at`) |
| `failed` | «No se pudo procesar» |
| `not_applicable` | Sin reembolso (el mes en curso no estaba pagado) |

## 6. Demo local

`tests/e2e/hu-w1.6/cancellation.sh` levanta el ensayo de `tests/e2e/hu-w1.4` (originación con su PostgreSQL, la pasarela simulada y Moto
como Cognito), compra una póliza por el recorrido real, cotiza, confirma y espera el reembolso «completed»; también prueba la pasarela
caída y de vuelta (un solo reembolso real en `GET /debug/charges`).
