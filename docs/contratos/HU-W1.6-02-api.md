# Contrato de API — HU-W1.6-02 · Cancelación de póliza por incumplimiento de pago

> **Fuente para el portal web (Angular) y el móvil.** Describe las cuotas mensuales de la prima, la cancelación automática por no pago y
> lo que leen el asegurado (A1, A2 y A2b) y el asesor (Renovaciones, S6). Contratos formales: `services/origination/api/openapi.yaml`
> (0.17.0) y `services/notifications/api/openapi.yaml` (0.5.0). Evento: `PolicyCancelledForNonPayment` en
> [`docs/events/README.md`](../events/README.md).

## 1. Reglas acordadas

La prima de una póliza de originación es **mensual**. El cobro de la emisión (HA-D02) paga el primer mes. Desde el segundo, cada mes es
una **cuota**:

| Regla | Valor | Variable |
|---|---|---|
| Vencimiento | El mismo día del mes en que empezó la vigencia (16/09 → 16/10, como A2), o el último día si el mes no lo tiene | — |
| Periodo de gracia | 30 días desde el vencimiento. La póliza **conserva la cobertura** y queda en «Pago pendiente» | `INSTALLMENT_GRACE_DAYS` |
| Cobros automáticos | Días 1, 8 y 15 de la gracia, a la tarjeta de la emisión, con una llave idempotente por intento (`cuota-<póliza>-<mes>-<intento>`) | `INSTALLMENT_ATTEMPT_DAYS` |
| Cancelación | El día 31, si la cuota sigue sin pagar: «Cancelada por no pago», sin cobertura y **sin reembolso** | — |
| Mora | No se cobra interés de mora (art. 1068 del C. Co.: la mora termina el contrato) | — |
| Aviso | Correo (origination) y *push* (notifications, desde el evento). **No** SMS: el Figma dice «Correo y SMS» y se corrige en pantalla | — |
| Demo | `INSTALLMENT_DAY_MS` acorta el día: con `60000`, un mes dura 30 minutos y la gracia otros 30 | `INSTALLMENT_DAY_MS` |
| Barrido | Cada hora | `INSTALLMENT_SWEEP_INTERVAL_MS` |

Además:

- **Solo pólizas de originación** (vida). Las del núcleo de pólizas no tienen prima en el sistema: no se cobran ni se cancelan aquí. Claims
  radica sobre las del núcleo, así que no puede recibir un siniestro de una póliza cancelada aquí.
- **Desde cuándo:** la migración `0017` guarda en `installment_billing` el momento en que empezó la facturación. Un mes que venció antes
  no se cobra ni cancela la póliza (pólizas emitidas antes de esta historia).
- **Un mes a la vez:** mientras una cuota está pendiente no se factura la siguiente; si se paga tarde, la siguiente se factura en el
  próximo barrido. La gracia siempre es de una sola cuota.
- **PSE:** una póliza pagada por PSE no tiene tarjeta que cobrar de nuevo. Cada intento queda rechazado con `no_payment_method` y la póliza
  se cancela al final de la gracia. Cobrar por PSE sin el asegurado queda fuera de esta historia.
- **Resultado desconocido:** un intento sin respuesta de la pasarela se reintenta con la **misma llave** una hora después
  (`INSTALLMENT_UNKNOWN_RETRY_MS`) y no se cuenta como rechazo.
- **Renovación:** una póliza cancelada no se renueva (`409 renewal_not_available`), no recibe alertas de vencimiento (HU-M3.2-01) ni el
  aviso de «no fue renovada» al terminar su vigencia.

## 2. Flujo

```
origination: barrido (internal/installments), cada INSTALLMENT_SWEEP_INTERVAL_MS
  1. factura los meses vencidos  ─────────────▶ premium_installment (pending)
  2. cobra los intentos del día  ─────────────▶ pasarela (cuota-…) ─▶ premium_installment_attempt (approved / declined)
  3. cancela al día 31 sin pago ─────────────▶ policy_cancellation + PolicyCancelledForNonPayment en el outbox (una transacción)
  4. correo al asegurado (idempotente por event_id; hasta 5 intentos)
notifications: ntf-cancelled-<policy_id> en el centro + push por dispositivo con permiso (grupo policy_cancelled)
Portal: GET /v1/policies (A1), GET /v1/policies/{id} (A2/A2b), GET /v1/policies/expiring (asesor)
```

Varias tareas de origination pueden barrer a la vez: el mes se factura una vez (`PRIMARY KEY (policy_id, seq)`), cada intento lo toma una
sola tarea (`claimed_until`, `FOR UPDATE SKIP LOCKED`) y la póliza se cancela una vez (`policy_cancellation`, `ON CONFLICT DO NOTHING`).

## 3. `GET /v1/policies` (A1) — campos nuevos

Cada póliza de originación trae `payment` y, si se canceló, `cancellation`. Las del núcleo traen ambos en `null`.

```json
{
  "policy_id": "VH-2026-004512",
  "status": "cancelled",
  "can_renew": false,
  "payment": { "status": "cancelled", "next_due_at": null, "pending": null },
  "cancellation": {
    "reason": "non_payment",
    "cancelled_at": "2026-09-12T05:00:00Z",
    "refund": "not_applicable",
    "month": 2,
    "due_at": "2026-08-12T05:00:00Z",
    "grace_ends_at": "2026-09-11T05:00:00Z",
    "amount": 142300,
    "currency": "COP",
    "attempts": 3,
    "email_sent_at": "2026-09-12T05:01:00Z"
  }
}
```

| `payment.status` | Cuándo | `status` de la póliza |
|---|---|---|
| `up_to_date` | Sin cuotas pendientes; `next_due_at` es el próximo vencimiento | `active` |
| `payment_pending` | Una cuota venció sin pago; `pending` trae el mes, el vencimiento, el fin de la gracia, la cancelación y los intentos | `active` (conserva la cobertura) |
| `cancelled` | Cancelada por no pago | `cancelled` |

## 4. `GET /v1/policies/{policy_id}` (A2 y A2b) — nueva

La póliza como la lista, más `payment_method` (sin el token) y `payments`: cada mes facturado, del más reciente al más antiguo. El mes 1
es la emisión (`kind: issuance`); los demás, cuotas con sus intentos.

```json
{
  "policy_id": "VH-2026-004512",
  "payment_method": { "method": "card", "brand": "visa", "last4": "0010", "installments": 1, "bank_code": null,
    "payer_type": null, "captured_at": "2026-07-02T15:00:00Z" },
  "payments": [
    { "month": 2, "kind": "installment", "status": "unpaid", "due_at": "2026-08-02T15:00:00Z", "amount": 142300,
      "currency": "COP", "paid_at": null,
      "attempts": [
        { "attempt": 1, "outcome": "declined", "decline_code": "51", "attempted_at": "2026-08-03T15:00:00Z" },
        { "attempt": 2, "outcome": "declined", "decline_code": "51", "attempted_at": "2026-08-10T15:00:00Z" },
        { "attempt": 3, "outcome": "declined", "decline_code": "51", "attempted_at": "2026-08-17T15:00:00Z" }
      ] },
    { "month": 1, "kind": "issuance", "status": "paid", "due_at": "2026-07-02T15:00:00Z", "amount": 142300,
      "currency": "COP", "paid_at": "2026-07-02T15:00:00Z", "attempts": [] }
  ]
}
```

- Solo el asegurado dueño de la póliza la lee; una póliza ajena responde `404 policy_not_found`, igual que una que no existe.
- Para una póliza del núcleo, `payment_method` es `null` y `payments` está vacío.
- Errores: `400 invalid_request` (id de más de 128 caracteres), `401`, `403` (otro rol), `404 policy_not_found`, `503 renewal_unavailable`.

## 5. `GET /v1/policies/expiring` (asesor) — cambios

- `status` admite `cancelled_non_payment`: las pólizas canceladas por no pago en los **últimos 30 días**, sin importar el rango de
  vencimiento. Sin `status`, aparecen después de las abiertas, la más reciente primero.
- Cada fila trae `cancelled_at` (`null` en las demás) y `can_renew: false` si está cancelada.
- La respuesta trae `cancelled_non_payment_last_30_days`, el contador de la tarjeta «Canceladas por no pago», sin importar el filtro.
- `due_within_window` ya no cuenta las canceladas.

## 6. Notificación (`notifications` 0.5.0)

Tipo `policy_cancelled`, id `ntf-cancelled-<policy_id>` (una por póliza), con `cancellation`:

```json
{ "policy_id": "VH-2026-004512", "reason": "non_payment", "line": "life", "installment_seq": 2,
  "due_at": "2026-08-12T05:00:00Z", "grace_ends_at": "2026-09-11T05:00:00Z", "cancelled_at": "2026-09-12T05:00:00Z",
  "amount": 142300, "currency": "COP", "refund": "not_applicable" }
```

El *push* va en el grupo `policy_cancelled` con `policy_id` en `data`. **Pendiente en el móvil:** la app ignora los tipos que no conoce,
así que muestra este aviso cuando aprenda `policy_cancelled` (tarjeta del centro y notificación del sistema que abre la póliza).

## 7. Demo de punta a punta

1. Pagar una cotización con la tarjeta **4000 0000 0000 0010** de la pasarela simulada: aprueba la emisión y rechaza las cuotas
   (`cuota-…`) por fondos insuficientes (código 51). La 4111 1111 1111 1111 paga todo.
2. Arrancar origination con `INSTALLMENT_DAY_MS=60000` y `INSTALLMENT_SWEEP_INTERVAL_MS=30000`: la cuota vence a los 30 minutos, se cobra
   a los minutos 31, 38 y 45 y la póliza se cancela al minuto 61.
3. Ver «Pago pendiente» en A1 durante la gracia, «No pago» y A2b después, y la fila con el contador en Renovaciones del asesor.
