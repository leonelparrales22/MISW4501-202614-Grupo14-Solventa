# Contrato de API — HU-M3.3-01 · Notificación por cada cambio de etapa del siniestro

> **Fuente para el cliente móvil (Kotlin).** Describe lo que la app lee del servicio `notifications` para el aviso de cada cambio de
> etapa del siniestro. Contrato formal: `services/notifications/api/openapi.yaml` (0.3.0). No hay rutas nuevas: es un tipo nuevo de
> notificación en el mismo centro de HU-M3.1-01 (`GET /v1/notifications`, `GET /v1/notifications/{id}`, lectura y dispositivo).

## 1. Flujo

```
claims: el siniestro cambia de estado (asesor, operaciones o el pago)
   └─ claim_status_change ──(internal/stages, cada 2 s)──▶ ClaimStageChanged en el outbox ──▶ SNS claims ──▶ SQS notifications
notifications: notificación ntf-stage-<change_id> (siempre) + push por dispositivo con permiso (grupo claim_stages)
App: GET /v1/notifications ──▶ tarjeta del centro con la etapa, el radicado y la hora ──▶ al tocarla, la línea de tiempo (M10)
```

- **Etapas que avisan:** `in_evaluation` (criterio 1), `approved` con el monto (criterio 2) y `paid` con el monto y la fecha del desembolso
  (criterio 3). No avisan la radicación, la devolución, el rechazo, la novedad del pago ni los siniestros paramétricos (ya tienen el aviso
  de su pago, HU-M3.1-01).
- **Tiempo:** el evento sale de `claims` en unos 2 s y `notifications` lo registra al recibirlo, muy por debajo de los 60 s.

## 2. La notificación

```json
{
  "notification_id": "ntf-stage-1287",
  "kind": "claim_stage_changed",
  "occurred_at": "2026-09-20T14:40:05Z",
  "created_at": "2026-09-20T14:40:08Z",
  "read_at": null,
  "payout": null,
  "stage": {
    "change_id": 1287,
    "claim_id": "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90",
    "filing_number": "SIN-2026-01655",
    "policy_id": "HG-2025-880231",
    "stage": "paid",
    "changed_at": "2026-09-20T14:40:05Z",
    "amount": 4132500,
    "currency": "COP",
    "paid_at": "2026-09-20T14:40:00Z"
  }
}
```

- **Qué mostrar (criterio 5):** la etapa (`stage`), el radicado (`filing_number`) y la fecha y hora (`changed_at`). En `approved` y
  `paid`, el monto (`amount` + `currency`): lo que recibe el asegurado, ya sin el deducible. En `paid`, además, la fecha del desembolso
  (`paid_at`).
- **Al abrirla (criterio 5):** la línea de tiempo del siniestro `stage.claim_id` (pantalla M10, `GET /v1/claims/{claim_id}` y su
  historial). Ojo: en `parametric_payout` el campo `payout.claim_id` es el radicado; en `claim_stage_changed` el id va en `claim_id` y el
  radicado en `filing_number`.
- Un tipo que la app no conoce se ignora, como antes.

## 3. El *push* (criterio 4)

Va en su propio grupo (`claim_stages`), en el idioma del dispositivo, con `data` = `notification_id`, `kind` y `claim_id`. Ejemplos:

| Etapa | Título | Cuerpo |
|---|---|---|
| `in_evaluation` | Tu siniestro está en evaluación | SIN-2026-01655 pasó a evaluación el 05/09/2026 a las 10:02. Toca para ver la línea de tiempo. |
| `approved` | Tu siniestro fue aprobado: $ 4.132.500 COP | SIN-2026-01655 fue aprobado el …. Te avisaremos cuando se haga el pago. … |
| `paid` | Pago de tu siniestro realizado: $ 4.132.500 COP | El pago de SIN-2026-01655 se hizo el … a tu medio registrado. … |

En AWS el *push* sale por **Firebase Cloud Messaging** (proyecto `solventa-94126`): un mensaje de solo datos con prioridad alta que
despierta la app aunque esté cerrada o el teléfono en reposo. La app no lo muestra tal cual: en ese momento revisa el centro y muestra la
notificación del sistema con sus propios textos, en su idioma, en el canal «Avance de tus siniestros», respetando el permiso (criterio 4).
Llega en segundos. La revisión cada 20 s con la app abierta y la periódica cada 15 min quedan de respaldo.

En local y en las e2e el proveedor sigue siendo el simulador `stubs/push`, que no entrega al teléfono: ahí la app solo se entera por esas
revisiones.
