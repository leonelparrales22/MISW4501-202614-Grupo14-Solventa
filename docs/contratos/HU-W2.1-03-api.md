# Contrato de API — HU-W2.1-03 · Intervención del asesor en la aprobación de la radicación

Servicio: **siniestros** (`claims`). Consumidores: portal web del asesor (revisión de la radicación), portal y app del
asegurado (estado y línea de tiempo: HU-W2.1-02, HU-M2.4-01) y la notificación por cambio de etapa (HU-M3.3-01).

> Contrato formal: `services/claims/api/openapi.yaml` (0.10.0). Este documento resume lo que necesita cada consumidor.

---

## 1. Qué cambia en el ciclo del siniestro

Hasta el Sprint 2 un caso radicado entraba directo a evaluación (supuesto S5 de HU-W2.2-01). Desde esta historia el
**asesor revisa cada radicación** antes de que llegue a operaciones:

```
radicar (móvil o web) ──▶ active ──aprobar (asesor)──▶ in_evaluation ──▶ triage + cola de operaciones ──▶ decisión (HU-W2.2-03)
                            │  ▲
                            │  └── reenviar (asegurado, HU-W2.1-04)
                            └──devolver (asesor)──▶ returned
```

| Estado | En el portal | Quién actúa |
|---|---|---|
| `active` | Radicado · pendiente de revisión | Asesor |
| `returned` | Devuelto al cliente | Asegurado (corrige y reenvía, HU-W2.1-04) |
| `in_evaluation` | En evaluación | Operaciones |
| `approved`, `paid`, `rejected`, `payment_exception` | Igual que en HU-W2.2-03 | Operaciones |

**Efectos para el resto del equipo:**

- El **triage** (HU-W2.2-01) y la **cola de operaciones** (`GET /v1/claims/evaluations`, HU-W2.2-03/HU-W2.2-02) solo toman
  casos `in_evaluation`. Un caso recién radicado **no aparece en evaluación** hasta que el asesor lo aprueba.
- Operaciones solo decide casos `in_evaluation` (un `active` o `returned` responde `409 claim_not_decidable`).
- Para la demo: radicar → **aprobar como asesor** → evaluar como operaciones.

---

## 2. Rutas

Todas cuelgan de `/v1/claims/{proxy+}` (GET y POST), así que el API Gateway ya las publica con el autorizador de Cognito.
No hay cambios de Terraform.

| Método y ruta | Rol | Para qué |
|---|---|---|
| `GET /v1/claims/reviews` | `advisor` | Radicaciones por revisar (`active`), de la más antigua a la más nueva, paginadas como `/evaluations` |
| `GET /v1/claims/{claim_id}/review` | `advisor` | Pantalla S8: lo mismo que ve operaciones en `/evaluation` (caso, soportes, evidencia, póliza, cobertura, asegurado) + `checklist`, `prior_claims` y `reviews` |
| `POST /v1/claims/{claim_id}/review-approval` | `advisor` | Aprueba (todos los soportes adjuntos quedan válidos): `active` → `in_evaluation` |
| `POST /v1/claims/{claim_id}/review-return` | `advisor` | Devuelve con motivo, mensaje y veredicto por soporte: `active` → `returned` |
| `GET /v1/claims/{claim_id}/history` | `insured` (solo sus casos), `advisor`, `operations` | Cada cambio de estado del caso |

Otro rol recibe `403`. Un asegurado que pide el historial de un caso ajeno recibe `404` (no se revela que existe).

---

## 3. Revisar una radicación (pantalla S8)

### 3.1 Lo que lee el asesor

`GET /v1/claims/{claim_id}/review` trae, además de la lectura de la evaluación:

```json
{
  "checklist": [
    { "requirement_code": "vehicle_photos", "label": "Fotos del vehículo", "required": true, "files": 6 },
    { "requirement_code": "driver_document", "label": "Documento del conductor", "required": true, "files": 1 },
    { "requirement_code": "traffic_report", "label": "Informe de tránsito o croquis", "required": true, "files": 1 },
    { "requirement_code": "plate_photo", "label": "Fotografía de la placa", "required": false, "files": 0 }
  ],
  "prior_claims": [ { "claim_id": "…", "filing_number": "SIN-2026-01500", "incident_type": "glass", "status": "rejected", "…": "…" } ],
  "reviews": []
}
```

- `checklist`: una fila por soporte del tipo de siniestro, en el orden del catálogo, con cuántos archivos tiene. Cada fila de
  "Soportes adjuntos" de S8 sale de aquí. Si `required` es verdadero y `files` es 0, el soporte **falta**.
- **Radicación del móvil:** su evidencia (4 a 6 fotos) cuenta como `vehicle_photos`, el único soporte que exige. El resto de
  los soportes del tipo aparecen con `required: false`, para que el asesor pueda pedirlos.
- **Fotografía de la placa** (`plate_photo`) es un soporte **opcional** nuevo de colisión, para que el asesor pueda pedirlo,
  como en S8. La radicación web no lo exige.
- `prior_claims`: los otros siniestros del mismo asegurado sobre la misma póliza, del más nuevo al más viejo (máx. 20). Es el
  "Siniestros previos" del panel de la póliza.

### 3.2 Aprobar

`POST /v1/claims/{claim_id}/review-approval`, sin cuerpo. Aprobar quiere decir que **todos los soportes adjuntos son
válidos**. Si falta un soporte requerido, responde `422 supports_incomplete`: hay que devolver la radicación.

### 3.3 Devolver

```http
POST /v1/claims/{claim_id}/review-return
{
  "reason_code": "illegible_or_incomplete_supports",
  "message": "El informe de tránsito no se puede leer y falta la foto de la placa. Adjúntalos y reenvía la radicación.",
  "supports": [
    { "requirement_code": "traffic_report", "verdict": "illegible", "note": "borroso, no se lee el número de croquis" },
    { "requirement_code": "plate_photo", "verdict": "missing" }
  ],
  "facts_to_correct": []
}
```

| Campo | Reglas |
|---|---|
| `reason_code` | Obligatorio: `illegible_or_incomplete_supports`, `inconsistent_facts`, `missing_information` u `other` |
| `message` | Obligatorio, 1 a 1000 caracteres: lo que lee el asegurado (A9) |
| `supports[]` | El veredicto sobre los soportes que **no sirven**. Un soporte adjunto puede ser `valid` o `illegible`, y uno sin adjuntar solo `missing` (así se pide uno opcional). `note` es opcional (máx. 300). Sin repetir el código, máx. 12 |
| `facts_to_correct` | `occurred_at`, `place`, `description`, `estimated_amount`; sin repetir |

- Un soporte adjunto que no se menciona queda `valid`. Uno **requerido** sin adjuntar queda `missing` aunque no se mencione.
- Debe haber al menos un soporte `illegible` o `missing`, o un dato a corregir.

Respuesta `200`. La aprobación responde igual, con `status: in_evaluation`, `outcome: approved`, `reason_code` y
`message` en `null`, y todos los soportes `valid`:

```json
{
  "claim_id": "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90",
  "filing_number": "SIN-2026-01877",
  "status": "returned",
  "review": {
    "seq": 1,
    "outcome": "returned",
    "reviewed_by": "juan.rios@solventa.example",
    "reviewer_name": "Juan Ríos",
    "reviewed_at": "2026-09-15T14:12:00Z",
    "reason_code": "illegible_or_incomplete_supports",
    "message": "El informe de tránsito no se puede leer y falta la foto de la placa. Adjúntalos y reenvía la radicación.",
    "supports": [
      { "requirement_code": "vehicle_photos", "label": "Fotos del vehículo", "verdict": "valid", "note": null },
      { "requirement_code": "driver_document", "label": "Documento del conductor", "verdict": "valid", "note": null },
      { "requirement_code": "traffic_report", "label": "Informe de tránsito o croquis", "verdict": "illegible",
        "note": "borroso, no se lee el número de croquis" },
      { "requirement_code": "plate_photo", "label": "Fotografía de la placa", "verdict": "missing", "note": null }
    ],
    "facts_to_correct": []
  }
}
```

`review.supports` es lo que pinta A9 (HU-W2.1-04): `illegible` es **Reemplazar**, `missing` es **Falta** y `valid` es
**Adjunto**. `reviewer_name` sale del atributo `name` de Cognito; si el usuario no tiene nombre, se usa su correo.

| Código | Cuándo |
|---|---|
| `400 invalid_request` | El cuerpo no cumple el esquema: campo desconocido, motivo fuera de la lista, veredicto desconocido, código o dato repetido |
| `404 claim_not_found` | No existe el caso |
| `409 claim_not_reviewable` | El caso no está `active`: ya se revisó, está devuelto o está en otra etapa |
| `422 supports_incomplete` | (aprobar) Falta un soporte requerido |
| `422 message_required` / `message_too_long` / `note_too_long` | Mensaje vacío o largo, nota larga |
| `422 nothing_requested` | Ningún soporte `illegible` ni `missing` y ningún dato a corregir |
| `422 support_not_applicable` | Un soporte que no es del tipo de siniestro |
| `422 verdict_not_applicable` | Un soporte adjunto marcado `missing`, o uno sin adjuntar marcado `valid` o `illegible` |

Dos asesores revisando a la vez: gana uno y el otro recibe `409`.

---

## 4. Historial de estados (para HU-W2.1-02, HU-M2.4-01, HU-M3.3-01 y HA-L02)

```http
GET /v1/claims/{claim_id}/history
```

```json
{
  "claim_id": "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90",
  "filing_number": "SIN-2026-00007",
  "status": "returned",
  "last_changed_at": "2026-11-09T20:00:00Z",
  "changes": [
    { "id": 41, "status": "active", "previous_status": null, "changed_at": "2026-11-09T14:00:00Z" },
    { "id": 57, "status": "returned", "previous_status": "active", "changed_at": "2026-11-09T20:00:00Z" }
  ],
  "reviews": [ { "seq": 1, "outcome": "returned", "reviewer_name": "Juan Ríos", "…": "igual que en la sección 3.3" } ],
  "last_return": { "seq": 1, "outcome": "returned", "…": "la misma revisión, mientras el caso está devuelto" }
}
```

- **Lo registra la base de datos**, no el código: un *trigger* sobre `claim` agrega una fila a `claim_status_change` cada
  vez que cambia `status`, la cambie quien la cambie (radicación, revisión, decisión, pago o lo que agreguen las historias
  siguientes, p. ej. "Perito informó" de HU-W2.3-02). Ninguna historia tiene que acordarse de escribir el historial.
- Las tablas `claim_review` y `claim_status_change` son de **solo inserción** (un `UPDATE`, `DELETE` o `TRUNCATE` falla).
- `id` crece con cada cambio de **todos** los casos: sirve como cursor si la notificación por cambio de etapa (HU-M3.3-01)
  necesita seguir los cambios. Si hace falta un *feed* (`GET /v1/claims/status-changes?after=<id>`), se agrega sobre la
  misma tabla sin migración.
- `reviews` son todas las revisiones del asesor: de ahí salen "Revisión del asesor: aprobada · Juan Ríos" (A8) y
  "Devuelto por Juan Ríos" (A9).
- `last_return` solo viene mientras el caso está `returned`: es lo que el asegurado tiene que corregir (HU-W2.1-04).
- Los casos radicados antes de esta historia reciben un historial reconstruido: la radicación y, si ya estaban decididos,
  su estado actual a la hora de la decisión.

---

## 5. Lo que no hace esta historia

- No avisa al asegurado por correo o push de la devolución: eso es la notificación por cambio de etapa (HU-M3.3-01), que
  puede leer el historial.
- No permite corregir ni reenviar: eso es HU-W2.1-04 (el reenvío deja el caso otra vez `active` con el mismo radicado y
  una nueva revisión con `seq` 2).
- El asesor no lee el triage (`GET /v1/claims/{id}/triage` sigue siendo de operaciones y del asegurado): un caso por revisar
  todavía no tiene prioridad, y `/reviews` y `/review` la traen como `not_applicable`.
