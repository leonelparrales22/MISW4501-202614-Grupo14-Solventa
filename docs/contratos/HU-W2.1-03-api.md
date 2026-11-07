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
| `GET /v1/claims/{claim_id}/review` | `advisor` | Lo mismo que ve operaciones en `/evaluation` (caso, soportes, evidencia, póliza, cobertura, asegurado) + `reviews` |
| `POST /v1/claims/{claim_id}/review-approval` | `advisor` | Aprueba: `active` → `in_evaluation` |
| `POST /v1/claims/{claim_id}/review-return` | `advisor` | Devuelve con motivo y lo que falta: `active` → `returned` |
| `GET /v1/claims/{claim_id}/history` | `insured` (solo sus casos), `advisor`, `operations` | Cada cambio de estado del caso |

Otro rol recibe `403`. Un asegurado que pide el historial de un caso ajeno recibe `404` (no se revela que existe).

---

## 3. Devolver una radicación

```http
POST /v1/claims/{claim_id}/review-return
{
  "reason": "La factura de la reparación no es legible.",
  "missing_supports": ["repair_quote"],
  "facts_to_correct": ["estimated_amount"]
}
```

| Campo | Reglas |
|---|---|
| `reason` | Obligatorio, 1 a 1000 caracteres; lo lee el asegurado |
| `missing_supports` | Códigos de soporte **del tipo de siniestro** (`GET /v1/claims/filing-options`), sin repetir, máx. 10 |
| `facts_to_correct` | `occurred_at`, `place`, `description`, `estimated_amount`; sin repetir |

Debe venir al menos uno de `missing_supports` o `facts_to_correct`.

Respuesta `200` (igual para la aprobación, con `status: in_evaluation`, `outcome: approved` y `reason: null`):

```json
{
  "claim_id": "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90",
  "filing_number": "SIN-2026-00007",
  "status": "returned",
  "review": {
    "seq": 1,
    "outcome": "returned",
    "reviewed_by": "asesor@solventa.example",
    "reviewed_at": "2026-11-09T20:00:00Z",
    "reason": "La factura de la reparación no es legible.",
    "missing_supports": ["repair_quote"],
    "facts_to_correct": ["estimated_amount"]
  }
}
```

| Código | Cuándo |
|---|---|
| `400 invalid_request` | El cuerpo no cumple el esquema (campo desconocido, código repetido, hecho no válido) |
| `404 claim_not_found` | No existe el caso |
| `409 claim_not_reviewable` | El caso no está `active` (ya se revisó, está devuelto o en otra etapa) |
| `422 reason_required` / `reason_too_long` | Motivo vacío o de más de 1000 caracteres |
| `422 nothing_requested` | No pide ningún soporte ni ningún dato |
| `422 support_not_applicable` | Un soporte que no es del tipo de siniestro |

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
  "last_return": {
    "seq": 1, "outcome": "returned", "reviewed_by": "asesor@solventa.example", "reviewed_at": "2026-11-09T20:00:00Z",
    "reason": "La factura de la reparación no es legible.", "missing_supports": ["repair_quote"],
    "facts_to_correct": ["estimated_amount"]
  }
}
```

- **Lo registra la base de datos**, no el código: un *trigger* sobre `claim` agrega una fila a `claim_status_change` cada
  vez que cambia `status`, la cambie quien la cambie (radicación, revisión, decisión, pago o lo que agreguen las historias
  siguientes, p. ej. "Perito informó" de HU-W2.3-02). Ninguna historia tiene que acordarse de escribir el historial.
- Las tablas `claim_review` y `claim_status_change` son de **solo inserción** (un `UPDATE`, `DELETE` o `TRUNCATE` falla).
- `id` crece con cada cambio de **todos** los casos: sirve como cursor si la notificación por cambio de etapa (HU-M3.3-01)
  necesita seguir los cambios. Si hace falta un *feed* (`GET /v1/claims/status-changes?after=<id>`), se agrega sobre la
  misma tabla sin migración.
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
