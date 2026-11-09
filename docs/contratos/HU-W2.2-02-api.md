# Contrato de API — HU-W2.2-02 · Cola de trabajo ordenada por prioridad

Servicio: **siniestros** (`claims`). Consumidor: portal web del analista de operaciones (pantalla O5, `/siniestros/evaluacion`).

> Contrato formal: `services/claims/api/openapi.yaml` (0.11.0). Se apoya en HU-W2.1-03: la cola son los casos
> `in_evaluation`, que el asesor aprobó.

---

## 1. `GET /v1/claims/evaluations`

Solo el rol `operations`; los demás reciben `403`.

| Parámetro | Valores | Para qué |
|---|---|---|
| `page_size` | 1 a 50 (20 si no viene) | Casos por página |
| `page_token` | El `next_page_token` anterior | Página siguiente, en el mismo orden |
| `level` | `1`, `2`, `3`, `none` | Filtro de prioridad (`none`: sin nivel todavía) |
| `line` | `auto`, `home` | Filtro de producto, por el tipo de siniestro |
| `status` | `in_evaluation` | Filtro de estado. Hoy es el único estado de la cola; HU-W2.3-02 agregará «Perito informó» |

Un valor fuera de la lista responde `400 invalid_request`.

**Orden (CA-2):** Triage 1, luego 2, luego 3 y al final lo que el triage aún no calcula. En cada nivel va primero el caso
radicado hace más tiempo. El orden se calcula en cada lectura sobre el triage vigente (`claim_current_triage`), así que un
cambio de nivel mueve el caso en la siguiente consulta, sin reordenar a mano (CA-3). La paginación es por cursor
(nivel, fecha de radicación, id): no repite ni salta casos aunque entren casos nuevos.

### Respuesta `200`

```json
{
  "items": [
    {
      "claim_id": "1c9a3e57-2b4d-4f6a-8e0b-7d5c3a1f9e24",
      "filing_number": "SIN-2026-01690",
      "status": "in_evaluation",
      "channel": "web",
      "policy": { "policy_id": "AU-2025-118204", "line": "auto", "product": "Autos", "insured_object": "KJT-482" },
      "incident_type": "theft",
      "occurred_at": "2026-09-03T03:15:00Z",
      "filed_at": "2026-09-04T00:20:00Z",
      "estimated_amount_minor": 4800000000,
      "triage": { "state": "assessed", "level": 1, "severity": "high", "amount_band": "high" },
      "holder": { "full_name": "Andrea Rojas Mejía", "document_type": "CC", "document_id": "52998112" }
    }
  ],
  "next_page_token": null,
  "summary": {
    "total": 22,
    "matching": 2,
    "levels": [ { "level": 1, "claims": 3 }, { "level": 2, "claims": 7 }, { "level": 3, "claims": 12 } ],
    "unassessed": 0,
    "mean_resolution_days": 4.2,
    "decided_in_window": 18,
    "window_days": 30
  }
}
```

- `holder`: el asegurado según el núcleo de pólizas, sin su contacto. Se lee **una vez por asegurado de la página**
  (hasta 8 a la vez). Si el núcleo no responde, llega `null` y la cola se sirve igual.
- `summary.levels`, `unassessed` y `total` cuentan **toda la cola**, sin filtros: son los indicadores de O5. `matching` son
  los casos que cumplen los filtros («22 casos en cola»).
- `mean_resolution_days`: promedio de días entre la radicación y la decisión de los casos decididos en los últimos 30 días,
  con un decimal. Si nadie se decidió en la ventana llega `null`, nunca un valor inventado (CA-4).
- Con la cola vacía: `items: []`, conteos en 0 y `mean_resolution_days: null` (CA-4).
- El objetivo de atención de cada nivel («Objetivo: 5 días hábiles») sigue saliendo de `GET /v1/claims/triage-policy`.

## 2. Lo que no entra en la cola

- `active` (espera la revisión del asesor) y `returned` (espera al asegurado), aunque el mockup O5 muestre un «Devuelto al
  cliente»: el criterio 1 define la cola como lo pendiente de decisión del analista.
- Los casos decididos (`approved`, `paid`, `rejected`, `payment_exception`). Las excepciones de pago siguen en
  `GET /v1/claims/payment-exceptions`.
