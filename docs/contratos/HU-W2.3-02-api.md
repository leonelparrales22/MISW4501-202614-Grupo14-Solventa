# Contrato de API — HU-W2.3-02 · Seguimiento y cierre del caso

Servicio: **siniestros** (`claims`). Consumidor: portal web del analista de operaciones, pantalla O8 «Intervención del
prestador · SIN-…».

> **Fuente única para el front.** El contrato formal está en `services/claims/api/openapi.yaml` (versión 0.19.0). Los ejemplos
> salen del caso del mockup: Servicios Hogar Norte, asignado el 06/09/2026, plomería y humedades. Las horas van en RFC 3339 y en
> UTC, y pueden traer fracciones de segundo.

---

## 1. Resumen del flujo

```
Portal del analista (rol operations)                       claims
─────────────────────────────────────────                  ─────────────────────────────────────────────────────────
GET  /v1/claims/{claim_id}/intervention ─────────────────▶ prestador asignado, pasos en orden, informe de la visita,
                                                             siguiente paso, si se puede cerrar y qué falta    [CA-2]
POST /v1/claims/{claim_id}/intervention/steps ───────────▶ registra el avance que reportó el prestador          [CA-1]
POST /v1/claims/{claim_id}/closure ──────────────────────▶ cierra con resultado obligatorio, guarda al analista
                                                             y libera al prestador                          [CA-3…CA-5]
```

La línea de tiempo de O8 tiene 6 filas:

1. *Asignado*, que sale de `assignment`.
2. Los 5 pasos de esta historia, en este orden:

| `step` | Fila de O8 | Notas |
|---|---|---|
| `accepted` | Aceptado por el prestador | La observación suele llevar la visita programada |
| `en_route` | En desplazamiento | |
| `visit_done` | Visita realizada e informe enviado | **Debe traer `report`** |
| `repair_authorized` | Reparación autorizada | Aprobación del analista |
| `repair_executed` | Reparación ejecutada y cierre | El prestador informa la intervención finalizada; desde aquí se puede cerrar |

| Criterio | Cómo |
|---|---|
| CA-1 Registrar cada avance con estado, fecha y hora y observación, y que el caso lo refleje de inmediato | `POST …/intervention/steps` devuelve la intervención ya actualizada |
| CA-2 Historial cronológico con soportes y comentarios | `steps[]` en orden, cada uno con `note`, `recorded_by` y `recorded_at`; `report` en la visita |
| CA-3 Cerrar con resultado obligatorio y fecha y hora | `POST …/closure` con `result` y `closed_at` |
| CA-4 No cerrar sin intervención finalizada ni sin resultado, y decir qué falta | `409 intervention_not_finished` con `missing[]`; sin `result`, `422` en `result`. El `GET` ya trae `closable` y `missing` |
| CA-5 El cierre no aprueba, rechaza ni paga, y registra al analista | `claim_status` no cambia; `closure.closed_by` |

---

## 2. Endpoints

### Cabeceras y reglas comunes

- `Authorization: Bearer <ID token de Cognito>`. Sin token responde `401`. **Solo el rol `operations`** puede usar estas
  rutas; el asegurado, el asesor y el socio reciben `403`.
- Todas las respuestas llevan `Cache-Control: no-store`.
- Los errores tienen la forma `{"error": "<código estable>", "message": "<texto en inglés, no mostrar>"}`.
- Si un campo no existe en el contrato o el cuerpo no es un objeto, la respuesta es `400 invalid_request`. Si los valores son
  inválidos, es `422 validation_failed` con **todos** los campos a la vez en `fields[]`.
- **El estado del siniestro no cambia.** El caso sigue en `in_evaluation` (o donde estuviera) durante la intervención y
  después del cierre.

### 2.1 Estados de la intervención

| `state` | Qué significa | Qué hace el front |
|---|---|---|
| `not_assigned` | El caso no tiene prestador | Aviso con enlace a «Asignar prestador» |
| `in_progress` | El prestador no ha informado la intervención finalizada | Insignia «En curso»; «Registrar avance» en el paso `next_step` |
| `ready_to_close` | Reparación ejecutada y caso abierto | Insignia «En curso»; «Cerrar caso» habilitado |
| `closed` | Caso cerrado | Insignia «Cerrado» y datos del cierre; sin acciones |

### 2.2 `GET /v1/claims/{claim_id}/intervention`

Así se ve O8 a mitad de la intervención, con la visita ya reportada y la autorización como siguiente paso:

```json
{
  "claim_id": "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90",
  "filing_number": "SIN-2026-01655",
  "claim_status": "in_evaluation",
  "incident_type": "water_damage",
  "policy_id": "HG-2025-880231",
  "place": "Carrera 15 # 93-40",
  "state": "in_progress",
  "assignment": {
    "provider": {
      "id": "prv-servicios-hogar-norte",
      "name": "Servicios Hogar Norte",
      "contact_email": "despachos@hogarnorte.example.com",
      "contact_phone": "+576015550102",
      "distance_km": 2.3
    },
    "assigned_by": {"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
    "assigned_at": "2026-09-06T15:15:02Z"
  },
  "steps": [
    {
      "step": "accepted",
      "occurred_at": "2026-09-06T15:40:00Z",
      "note": "visita programada 15/09/2026 8:00 a 12:00",
      "report": null,
      "recorded_by": {"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
      "recorded_at": "2026-09-06T15:41:10Z"
    },
    {
      "step": "en_route",
      "occurred_at": "2026-09-15T12:50:00Z",
      "note": "reportado por el prestador",
      "report": null,
      "recorded_by": {"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
      "recorded_at": "2026-09-15T12:52:31Z"
    },
    {
      "step": "visit_done",
      "occurred_at": "2026-09-15T14:30:00Z",
      "note": null,
      "report": {
        "cause": "Filtración por tubería del piso superior",
        "damage": "Techo y muro de cocina · 6 m²",
        "repair_cost_minor": 435000000,
        "repair_days": 2,
        "photos": 8
      },
      "recorded_by": {"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
      "recorded_at": "2026-09-15T14:35:02Z"
    }
  ],
  "next_step": "repair_authorized",
  "report": {
    "cause": "Filtración por tubería del piso superior",
    "damage": "Techo y muro de cocina · 6 m²",
    "repair_cost_minor": 435000000,
    "repair_days": 2,
    "photos": 8
  },
  "closure": null,
  "closable": false,
  "missing": ["repair_authorized", "repair_executed"]
}
```

- `repair_cost_minor` está en **centavos de COP**, igual que el resto de montos de claims. Por ejemplo, `435000000` son
  $ 4.350.000.
- `report` repite el informe de la visita para la tarjeta «Informe del prestador». Vale `null` mientras no haya visita.
- Las fotos se quedan con el prestador: el servicio solo guarda cuántas son.
- Si el caso no tiene prestador, la respuesta es `200` con `state: not_assigned`, `assignment: null`, `steps: []` y
  `next_step: null`.
- La respuesta es `404 claim_not_found` si el caso no existe.

### 2.3 `POST /v1/claims/{claim_id}/intervention/steps`

```json
{"step": "accepted", "occurred_at": "2026-09-06T15:40:00Z", "note": "visita programada 15/09/2026 8:00 a 12:00"}
```

Para la visita:

```json
{
  "step": "visit_done",
  "occurred_at": "2026-09-15T14:30:00Z",
  "note": "informe PDF firmado",
  "report": {"cause": "Filtración por tubería del piso superior", "damage": "Techo y muro de cocina · 6 m²",
             "repair_cost_minor": 435000000, "repair_days": 2, "photos": 8}
}
```

**Reglas de los campos:**
- `step` tiene que ser justo `next_step`.
- `occurred_at` es cuándo dice el prestador que pasó. No puede ser anterior al paso previo (o a la asignación) ni estar en el
  futuro; hay 5 minutos de tolerancia para el reloj del navegador. La comparación se hace al minuto, porque el formulario no
  pide segundos.
- `note` es opcional y admite hasta 1000 caracteres.
- `report` es obligatorio en `visit_done` y se rechaza en cualquier otro paso:
  - `cause` y `damage`: entre 3 y 300 caracteres;
  - `repair_cost_minor`: ≥ 0;
  - `repair_days`: entre 0 y 365;
  - `photos`: opcional, entre 0 y 50.

**Respuestas:**

| Código | `error` | Cuándo |
|---|---|---|
| `201` | — | Paso registrado. El cuerpo es la intervención actualizada (igual al §2.2) |
| `409` | `step_out_of_order` | El paso ya estaba registrado o falta el anterior, por ejemplo porque otro analista lo registró primero. Hay que releer |
| `409` | `not_assigned` | El caso no tiene prestador |
| `409` | `case_closed` | El caso ya está cerrado |
| `422` | `validation_failed` | Los campos van en `fields[]`: `step`, `occurred_at`, `note`, `report`, `report.cause`, `report.damage`, `report.repair_cost_minor`, `report.repair_days`, `report.photos` |

### 2.4 `POST /v1/claims/{claim_id}/closure`

```json
{"result": "repaired", "closed_at": "2026-09-18T22:00:00Z", "notes": "Asegurada conforme con la reparación"}
```

| `result` | Texto sugerido |
|---|---|
| `repaired` | Reparación completada |
| `partially_repaired` | Reparación parcial |
| `no_repair_needed` | No requirió reparación |
| `not_repairable` | No reparable |

**Reglas de los campos:**
- `result` es obligatorio.
- `closed_at` es obligatorio. No puede ser anterior a `repair_executed` ni estar en el futuro.
- `notes` es opcional y admite hasta 1000 caracteres.

**Qué hace el cierre:**
- Guarda al analista responsable en `closure.closed_by`.
- En la misma transacción **libera al prestador**: pasa a `available` en el registro de prestadores y queda libre para otro caso.

**Respuestas:**

| Código | `error` | Cuándo |
|---|---|---|
| `201` | — | Caso cerrado. El cuerpo es la intervención con `state: closed` y `closure` |
| `409` | `intervention_not_finished` | El prestador no ha informado la intervención finalizada. El cuerpo trae `missing` con los pasos que faltan, por ejemplo `{"error": "intervention_not_finished", "message": "…", "missing": ["repair_authorized", "repair_executed"]}` |
| `409` | `not_assigned` | El caso no tiene prestador |
| `409` | `case_closed` | El caso ya estaba cerrado, por ejemplo porque otro analista lo cerró primero |
| `422` | `validation_failed` | Los campos van en `fields[]`: `result` (`required` o `invalid_format`), `closed_at` y `notes` |

---

## 3. Fuera de alcance

- Reasignar o cancelar la asignación.
- La calificación del prestador y su número de casos, que tampoco estaban en el contrato de HU-W2.3-01.
- Subir o ver las fotos del prestador: solo se registra cuántas son.
- Los datos de contacto de la asegurada: claims no los guarda. La tarjeta de O8 muestra la póliza y el lugar del incidente.
- Recalcular la prioridad con la causa `provider_report_received` (HU-W2.2-01) y avisar a la asegurada en cada etapa
  (HU-M3.3-01): como `claim.status` no cambia, no se dispara ningún evento.
