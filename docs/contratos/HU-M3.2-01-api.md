# Contrato de API — HU-M3.2-01 · Alertas 30, 15 y 5 días antes del vencimiento de la póliza

> **Fuente para el cliente móvil (Kotlin).** Describe lo que la app lee del servicio `notifications` para el aviso de vencimiento de
> una póliza. Contrato formal: `services/notifications/api/openapi.yaml` (0.4.0). No hay rutas nuevas: es un tipo nuevo de notificación
> en el mismo centro de HU-M3.1-01 (`GET /v1/notifications`, `GET /v1/notifications/{id}`, lectura y dispositivo).

## 1. Flujo

```
origination: barrido cada hora (EXPIRY_ALERT_INTERVAL_MS) sobre las pólizas activas
   ├─ las suyas (vida), sin renovación del periodo ni sucesora
   └─ las del núcleo de pólizas, titular por titular (clientes con cuenta)
   └─ hito cruzado y no avisado ──▶ policy_expiry_notice + PolicyExpiryApproaching en el outbox ──▶ SNS origination ──▶ SQS notifications
notifications: notificación ntf-expiry-<policy_id>-<AAAAMMDD>-<hito> (siempre) + push por dispositivo con permiso (grupo policy_expiry)
App: GET /v1/notifications ──▶ tarjeta del centro con la póliza y la fecha exacta de vencimiento ──▶ al tocarla, la póliza (M5)
```

- **Hitos (criterio 1):** 30, 15 y 5 días antes del vencimiento. Los días son de calendario en Colombia (America/Bogota): el día en que
  vence la póliza faltan 0.
- **Último hito cruzado:** se avisa solo el menor de {30, 15, 5} que no sea menor que los días que faltan. Una póliza que aparece a 14 días
  recibe solo el aviso de 15; el de 30 ya no llega. Nunca llegan dos avisos de la misma póliza a la vez.
- **Sin repetir (criterio 2):** una notificación por póliza, periodo e hito; el `notification_id` es determinista.
- **Ya renovada (criterio 3):** si el asegurado ya renovó (o el periodo ya tiene renovación en originación), no hay más avisos de ese
  periodo. Una renovación en el núcleo mueve la fecha de fin y el periodo nuevo vuelve a empezar sus hitos cuando se acerque.
- **Tiempo:** el barrido corre cada hora, así que el aviso llega dentro de la hora en que se cruza el hito.

## 2. La notificación

```json
{
  "notification_id": "ntf-expiry-AU-2025-118204-20261130-30",
  "kind": "policy_expiry",
  "occurred_at": "2026-11-14T15:00:00Z",
  "created_at": "2026-11-14T15:00:02Z",
  "read_at": null,
  "payout": null,
  "stage": null,
  "expiry": {
    "policy_id": "AU-2025-118204",
    "line": "auto",
    "product": "Autos",
    "insured_object": "KJT-482",
    "coverage_end": "2026-12-01T04:59:59Z",
    "milestone_days": 30,
    "days_left": 16
  }
}
```

- **`expiry`** es obligatorio y `null` en los otros tipos, igual que `payout` y `stage`.
- **Qué mostrar (criterio 1):** la póliza (`product` o, si es `null`, el nombre de la `line`; `insured_object` cuando viene) y la **fecha
  exacta de vencimiento**: `coverage_end` mostrado en la zona de Colombia (en el ejemplo, 30/11/2026). `days_left` son los días que
  faltaban al detectarlo; para el texto de la tarjeta conviene recalcularlos con la fecha de hoy, porque la notificación se puede leer días
  después.
- **`product` e `insured_object`** son `null` en las pólizas de vida que emitió originación (`line` = `life`).
- **Al abrirla:** el detalle de la póliza `expiry.policy_id` en la billetera (`GET /v1/policies` de originación).
- Un tipo que la app no conoce se ignora, como antes.

## 3. El *push* (criterio 4)

Va en su propio grupo (`policy_expiry`), con `data` = `notification_id`, `kind` (`policy_expiry`) y `policy_id`. En AWS sale por
**Firebase Cloud Messaging** como mensaje de solo datos con prioridad alta, igual que en HU-M3.3-01: despierta la app aunque esté cerrada,
y la app revisa el centro y muestra la notificación con sus propios textos y en su idioma. Los textos del simulador (`stubs/push`, en
local y en las e2e) son:

| Idioma | Título | Cuerpo |
|---|---|---|
| es | Tu póliza Autos vence el 30/11/2026 | AU-2025-118204 vence en 16 días. Renuévala para no perder tu cobertura. Toca para ver la póliza. |
| en | Your Autos policy expires on 11/30/2026 | AU-2025-118204 expires in 16 days. Renew it to keep your coverage. Tap to see the policy. |

Con 1 y 0 días el cuerpo dice «vence mañana» / «vence hoy» («expires tomorrow» / «expires today»). Sin producto se usa la línea (Vida,
Autos, Hogar, Viaje).

## 4. Ensayo local

[`tests/e2e/hu-m1.2`](../../tests/e2e/hu-m1.2/README.md) acepta `--dias-para-vencer N` para mover el vencimiento de AU-2025-118204, pero
ese entorno no produce el aviso de punta a punta (sin base de datos ni cliente vinculado ni `notifications`); ver su README.
