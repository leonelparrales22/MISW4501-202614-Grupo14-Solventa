# Contrato de API — HU-M2.4-01 · Consulta y seguimiento del siniestro (móvil)

> **Fuente para el cliente móvil (Kotlin).** Describe lo que la app lee del servicio `claims` para la línea de tiempo y el detalle del
> siniestro. Contrato formal: `services/claims/api/openapi.yaml` (0.14.0). Las rutas ya existían (HU-W2.1-01, HU-W2.1-03, HU-W2.2-01 y
> HA-L02). Esta historia agrega la **resolución automática** del siniestro paramétrico (`automatic_resolution`, criterio 3) y el motor
> paramétrico mínimo de `claims` que la produce. Todas las rutas son humanas (`Authorization: Bearer <ID token>`, rol `insured`); el
> titular es el `sub` del token.

## 1. Rutas por criterio

| Criterio | Ruta | Qué leer |
|---|---|---|
| CA-1 · "Mis siniestros" y línea de tiempo | `GET /v1/claims` y `GET /v1/claims/{claim_id}/history` | La lista (el más reciente primero) y el historial: `changes[]` trae cada cambio de estado, del más antiguo al más reciente, con `status`, `previous_status` y `changed_at`. La etapa actual es `status` (y `last_changed_at`) |
| CA-2 · En evaluación | `GET /v1/claims/{claim_id}/history` y `GET /v1/claims/{claim_id}/triage` | El inicio de la evaluación es el `changed_at` del cambio a `in_evaluation`. El tiempo estimado es `estimated_resolution_business_days` del triage, en días hábiles. Mientras el triage no se ha calculado responde sin él: la app no muestra estimado |
| CA-3 · Resolución automática | Las tres lecturas: lista, detalle e historial | `automatic_resolution` no nulo y `channel: "parametric"` (sección 3) |
| CA-4 · Información desactualizada | Las tres lecturas | `stale: true` y `as_of` (sección 4) |
| CA-5 · Sin conexión | Ninguna | Es solo de la app: muestra la última versión guardada con su hora de sincronización, distinta del aviso de CA-4 |

Un siniestro de otro asegurado responde `404 claim_not_found`, igual que uno que no existe. `503 claims_unavailable` significa reintentar.

## 2. Etapas (`status`)

| `status` | Etapa en la app |
|---|---|
| `active` | Radicado, en revisión del asesor |
| `returned` | Devuelto para completar (HU-W2.1-04) |
| `in_evaluation` | En evaluación |
| `approved` | Aprobado, pago en curso |
| `paid` | Pagado |
| `rejected` | Rechazado |
| `payment_exception` | Aprobado, con el pago en revisión |

Las etapas cumplidas son las de `changes[]`; la actual es la última. El historial de un siniestro paramétrico es
`in_evaluation → approved → paid`, normalmente en segundos.

## 3. Siniestro resuelto automáticamente (criterio 3)

```json
{
  "claim_id": "6f1c2a7e-3b4d-4e5f-8a9b-0c1d2e3f4a5b",
  "filing_number": "SIN-2026-01702",
  "status": "paid",
  "channel": "parametric",
  "incident_type": "flight_delay",
  "policy": { "policy_id": "VJ-2026-000931", "line": "travel", "product": "Viaje", "insured_object": "Viajes nacionales" },
  "automatic_resolution": {
    "resolved_at": "2026-09-11T21:39:30Z",
    "rule_version": "parametric-rules-v1",
    "trigger": {
      "parameter": "flight_delay",
      "event_reference": "AV-8421",
      "threshold": 180,
      "observed": 215,
      "unit": "minutes",
      "observed_at": "2026-09-11T21:39:00Z"
    }
  }
}
```

- **Cómo distinguirlo:** `automatic_resolution != null`, o `channel == "parametric"`. En un siniestro evaluado por un analista,
  `automatic_resolution` llega en `null`; el campo siempre está presente.
- **Qué mostrar:** el rótulo o ícono de "Resolución automática", el parámetro objetivo (`trigger.parameter` + `event_reference`, por
  ejemplo "Retraso del vuelo AV-8421"), el umbral (`threshold` + `unit`) y el valor que generó el pago (`observed` + `unit`).
- **El monto pagado** llega en la notificación de HU-M3.1-01 (`amount_minor`, `currency`). El `claim_id` de esa notificación es el de
  `GET /v1/claims/{claim_id}`, así que tocarla puede abrir este detalle.
- Un siniestro paramétrico no tiene soportes ni evidencia: `requirements` llega vacío.

## 4. Información potencialmente desactualizada (criterio 4)

Cada lectura espera 900 ms a la base. Si no responde, devuelve la última lectura guardada en la caché (HA-L02) con:

- `stale: true` y `as_of`: la hora en que se leyó de la base. La app lo muestra visible ("Información de las 14:30, puede estar
  desactualizada").
- Las cabeceras `X-Stale: true` y `X-Data-Source: cache`.

Con `stale: false`, `as_of` es la hora de la lectura. Este caso tiene conectividad y es distinto del CA-5, donde la app no llega al backend.

## 5. Probarlo

- **Local, con el emulador:** [`tests/e2e/hu-m2.4/app-local.sh`](../../tests/e2e/hu-m2.4/README.md). Siembra un siniestro radicado y
  uno en evaluación; `--payout` dispara la resolución automática y `--slow-db` / `--fast-db` sirven para el CA-4.
- **En AWS:** `tests/e2e/hu-m2.4/nube.sh` publica el retraso del vuelo en el tópico `iot`. El motor radica, resuelve y paga el siniestro,
  y `notifications` envía el *push*.
