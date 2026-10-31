# Contrato de API — HU-W2.2-03 · Evaluación, aprobación y pago del siniestro

Servicio: **siniestros** (`claims`). Consumidor: portal web del analista de operaciones (pantallas O6 y O6b).

> **Alcance de este documento.** La **lectura** de la evaluación (secciones 2 y 3) y la **decisión**: aprobar con pago
> automático, rechazar con aviso al asegurado y reintentar un pago fallido (secciones 4 a 7). Contrato formal:
> `services/claims/api/openapi.yaml` (0.8.0).

---

## 1. Resumen del flujo

```
Portal del analista (rol operations)                     claims                              policy-core
──────────────────────────────────────                   ─────────────────────────────        ────────────
GET /v1/claims/evaluations ───────────────────────────▶ casos en evaluación + prioridad
GET /v1/claims/{claim_id}/evaluation ─────────────────▶ caso, soportes, evidencia, triage ─▶ póliza y coberturas
                                                         y decisión                       ─▶ asegurado y cuenta
POST /v1/claims/{claim_id}/approval ──────────────────▶ valida contra la cobertura de hoy ─▶ (core)
                                                         worker ──▶ pasarela POST /payouts (llave indemnizacion-<id>)
POST /v1/claims/{claim_id}/rejection ─────────────────▶ rejected; worker ──▶ correo con el motivo
GET  /v1/claims/payment-exceptions ───────────────────▶ casos en payment_exception
POST /v1/claims/{claim_id}/payout-retry ──────────────▶ misma llave, destino corregido ──▶ worker
```

**Estados del caso**

```
active / in_evaluation ──aprobar──▶ approved ──pago confirmado──▶ paid
          │                            │  ▲
          │                            │  └──payout-retry──┐
          │                            └──rechazo de la pasarela o 3 intentos sin respuesta──▶ payment_exception
          └──rechazar──▶ rejected
```

- Las dos rutas cuelgan de `/v1/claims`, así que el API Gateway ya las enruta con el autorizador de Cognito
  (`/v1/claims/{proxy+}`, GET). **Solo el rol `operations`**: asegurado, asesor y socio reciben `403`.
- **Solo `operations`** aprueba, rechaza y reintenta (HA-S01, criterio 6).
- **"En evaluación"** son los estados que evalúa el triage (`active` e `in_evaluation`): mientras no exista la revisión del
  asesor (HU-W2.1-03), un caso radicado entra a evaluación tal como llega (supuesto S5 de HU-W2.2-01). Un caso decidido
  (pagado o rechazado, PR 2b) sale de la lista y el triage deja de recalcularlo.
- La **cola ordenada por prioridad** (O5) es HU-W2.2-02. Esta lista va del más antiguo al más nuevo y trae la prioridad de
  cada caso para que O5 la ordene o la filtre.

---

## 2. `GET /v1/claims/evaluations`

| Parámetro | Tipo | Reglas |
|---|---|---|
| `page_size` | entero | 1 a 50; 20 si no viene |
| `page_token` | string | El `next_page_token` de la página anterior; opaco |

### Respuesta `200`

```json
{
  "items": [
    {
      "claim_id": "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90",
      "filing_number": "SIN-2026-01655",
      "status": "active",
      "channel": "web",
      "policy": { "policy_id": "HG-2025-880231", "line": "home", "product": "Hogar", "insured_object": "Calle 1" },
      "incident_type": "water_damage",
      "occurred_at": "2026-09-02T22:15:00Z",
      "filed_at": "2026-09-03T19:20:00Z",
      "estimated_amount_minor": 480000000,
      "triage": {
        "claim_id": "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90",
        "state": "assessed",
        "level": 2,
        "severity": "medium",
        "amount_band": "low",
        "target": { "kind": "business_days", "business_days": 5, "resolution_business_days": 10 },
        "assessed_at": "2026-09-04T08:31:00Z"
      }
    }
  ],
  "next_page_token": null
}
```

- `estimated_amount_minor` en centavos de COP; `null` si el asegurado no lo declaró.
- `triage` tiene la misma forma que `GET /v1/claims/triage` ([HU-W2.2-01](HU-W2.2-01-api.md)). Un caso que el barrido aún
  no evaluó viene con `state: "pending"` y el resto en `null`.

### Errores

| Código | `error` | Condición |
|---|---|---|
| `400` | `invalid_request` | `page_size` fuera de 1–50 o `page_token` inválido |
| `401` / `403` | — | Sin sesión / rol distinto de `operations` |
| `503` | `claims_unavailable` | No se pudo leer. Reintentable |

---

## 3. `GET /v1/claims/{claim_id}/evaluation`

Todo lo que el analista necesita para decidir (pantalla O6).

### Respuesta `200`

```json
{
  "claim": {
    "claim_id": "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90",
    "filing_number": "SIN-2026-01655",
    "status": "active",
    "channel": "web",
    "policy": { "policy_id": "HG-2025-880231", "line": "home", "product": "Hogar", "insured_object": "Calle 1" },
    "incident_type": "water_damage",
    "occurred_at": "2026-09-02T22:15:00Z",
    "filed_at": "2026-09-03T19:20:00Z",
    "place": "Carrera 15 # 93-40, apto 402, Bogotá",
    "description": "Filtración desde el apartamento superior que afectó techo y muro de la cocina.",
    "supports": { "required_total": 2, "required_attached": 2, "files": 3, "items": [ "…" ] },
    "evidence": [
      {
        "kind": "photo", "content_type": "image/jpeg", "captured_at": "2026-09-02T22:20:00Z",
        "latitude": 4.6782, "longitude": -74.0482, "accuracy_m": 12, "location_trusted": true,
        "byte_size": 245000, "duration_s": 0,
        "download_url": "https://…firmada…", "download_expires_at": "2026-09-16T16:50:00Z"
      }
    ],
    "estimated_amount_minor": 480000000
  },
  "support_files": [
    {
      "support_id": "sup-1", "requirement_code": "water_photos", "file_name": "techo.jpg",
      "content_type": "image/jpeg", "byte_size": 2048, "sha256": "aa…",
      "download_url": "https://…firmada…", "download_expires_at": "2026-09-16T16:50:00Z"
    }
  ],
  "triage": { "view": "operations", "claim_id": "…", "state": "assessed", "current": { "…": "…" }, "history": [ "…" ] },
  "policy": {
    "policy_id": "HG-2025-880231", "line": "home", "product": "Hogar", "insured_object": "Calle 1",
    "status": "active", "valid_from": "2025-11-01T05:00:00Z", "valid_to": "2026-11-01T05:00:00Z",
    "coverages": [
      { "incident": "water_damage", "insured_value_minor": 32000000000, "currency": "COP", "deductible_basis_points": 500 }
    ]
  },
  "coverage": { "incident": "water_damage", "insured_value_minor": 32000000000, "currency": "COP", "deductible_basis_points": 500 },
  "holder": {
    "full_name": "Carolina Pérez Ruiz", "document_type": "CC", "document_id": "1020345678",
    "email": "carolina.perez@correo.example", "phone": "+57 301 234 5678",
    "payout_account": { "bank_code": "1007", "bank_name": "Banco Aliado", "account_type": "savings", "masked_number": "•••• 4512" }
  },
  "policy_unavailable": false
}
```

| Campo | Descripción |
|---|---|
| `claim` | El mismo detalle que ve el asegurado (`GET /v1/claims/{claim_id}`). `estimated_amount_minor` no viene si el asegurado no lo declaró |
| `claim.evidence` | Fotos y video del móvil (HU-M2.1-01) con ubicación, precisión, si es confiable y lectura firmada por 10 min. **Cierra el criterio 6 de HU-M2.1-01** |
| `support_files` | Los archivos de la radicación web, cada uno con su lectura firmada por 10 min |
| `triage` | La vista de operaciones de [HU-W2.2-01](HU-W2.2-01-api.md): nivel actual e historial («Triage N asignado» del historial de O6) |
| `policy` | La póliza **como está hoy** en el núcleo, con el valor asegurado y el deducible de cada cobertura. `null` si el núcleo ya no la conoce o no respondió |
| `coverage` | La cobertura del incidente del caso. `null` si la póliza no lo cubre o no se conocen sus términos |
| `holder` | El asegurado según el núcleo. **La cuenta viaja enmascarada**: el número completo hace falta para pagar, no para evaluar, y nunca sale del servicio en una lectura |
| `policy_unavailable` | `true` si el núcleo de pólizas no respondió. El resto del caso se muestra igual, pero **no se puede decidir sobre él** (O6 debe deshabilitar *Aprobar y pagar*) |

**Deducible (criterio 2).** `deductible_basis_points` es el porcentaje en puntos básicos (500 = 5 %). El monto que se paga es
el **aprobado bruto** menos ese porcentaje: con $ 4.350.000 aprobados y 5 %, se pagan $ 4.132.500. El PR 2b lo calcula y lo
valida en el servicio; el portal solo lo muestra.

### Errores

| Código | `error` | Condición |
|---|---|---|
| `401` / `403` | — | Sin sesión / rol distinto de `operations` |
| `404` | `claim_not_found` | No hay caso con ese id |
| `503` | `claims_unavailable` | No se pudo leer el caso o su prioridad. Reintentable |

Una caída del núcleo de pólizas **no** es un `503`: responde `200` con `policy_unavailable: true`.

`decision` es `null` mientras el caso está en evaluación; después trae el mismo cuerpo de la sección 4.

---

## 4. `POST /v1/claims/{claim_id}/approval`

```json
{ "approved_amount_minor": 435000000 }
```

El monto **bruto** aprobado, en centavos. El servicio lee del núcleo la cobertura del incidente **en ese momento** y valida:

| Regla | Si no se cumple |
|---|---|
| `approved_amount_minor` entero ≥ 1 | `400 invalid_request` |
| No supera `insured_value_minor` de la cobertura | `422 amount_exceeds_insured_value` |
| Después del deducible queda algo por pagar | `422 nothing_to_pay` |
| La póliza cubre el incidente y el núcleo conoce al asegurado | `422 not_covered` |
| El asegurado tiene cuenta de pago | `422 no_payout_account` |
| El caso sigue en evaluación (nadie lo decidió antes) | `409 claim_not_decidable` |
| El núcleo respondió | `503 policy_unavailable` (reintentable) |

### Respuesta `202`

```json
{
  "claim_id": "8f14e45f-…", "filing_number": "SIN-2026-01655",
  "status": "approved", "outcome": "approved",
  "decided_by": "analista@solventa.example", "decided_at": "2026-09-16T16:40:00Z",
  "approval": {
    "approved_amount_minor": 435000000, "insured_value_minor": 32000000000, "deductible_basis_points": 500,
    "deductible_minor": 21750000, "payout_amount_minor": 413250000, "currency": "COP"
  },
  "payout": {
    "payment_reference": "indemnizacion-8f14e45f-…", "status": "pending",
    "destination": { "bank_code": "1007", "bank_name": "Banco Aliado", "account_type": "savings", "masked_number": "•••• 4512" },
    "automatic_attempts": 0, "max_automatic_attempts": 3,
    "failure": null, "gateway_code": null, "last_failure_at": null,
    "gateway_reference": null, "paid_at": null,
    "manual_retries": 0, "last_retried_by": null, "last_retried_at": null
  },
  "rejection": null
}
```

- `202` porque el pago va aparte: el *worker* lo envía en cuanto se registra la aprobación (normalmente en menos de un
  segundo). El portal vuelve a leer el detalle (sección 3) para ver `paid` o `payment_exception`.
- **Deducible:** `deductible_minor = round(approved × basis_points / 10 000)` y `payout_amount_minor = approved − deductible`.
  El ejemplo de O6: $ 4.350.000 − 5 % = **$ 4.132.500**.
- **Un desembolso por aprobación (criterio 4, HA-D02):** el pago viaja con `Idempotency-Key: indemnizacion-<claim_id>`, la
  misma en todos los intentos y reintentos, y la pasarela paga una llave una sola vez.

## 5. `POST /v1/claims/{claim_id}/rejection`

```json
{ "reason": "La póliza excluye los daños por falta de mantenimiento." }
```

| Regla | Si no se cumple |
|---|---|
| `reason` presente | `400 invalid_request` |
| No es solo espacios | `422 reason_required` |
| Hasta 1000 caracteres | `422 reason_too_long` |
| El núcleo conoce al asegurado y tiene correo | `422 not_covered` / `422 no_contact` |
| El caso sigue en evaluación | `409 claim_not_decidable` |

Responde `200` con el caso en `rejected`, `approval` y `payout` en `null` y
`"rejection": { "reason": "…", "notified_at": null }`. El *worker* envía el correo al asegurado con el número de radicado y el
motivo (llave `rechazo-<claim_id>`); `notified_at` se llena cuando el proveedor lo acepta, y un envío fallido se reintenta.

## 6. Pago fallido: `payment_exception`

El caso pasa a `payment_exception` cuando:

| `payout.failure` | Qué pasó | `payout.gateway_code` |
|---|---|---|
| `rejected` | La pasarela respondió y rechazó el pago (no se reintenta solo) | El motivo, p. ej. `invalid_destination_account` |
| `retries_exhausted` | 3 intentos sin respuesta clara (2 s y 4 s entre ellos), siempre con la misma llave | `null` |

La aprobación **se conserva** con su `payment_reference` (criterio 4). `GET /v1/claims/payment-exceptions` los lista con la
misma forma y paginación que la sección 2.

**En la demo:** el stub de la pasarela rechaza toda cuenta terminada en `89`, así que un asegurado con esa cuenta lleva a O6b.

## 7. `POST /v1/claims/{claim_id}/payout-retry`

```json
{ "destination": { "bank_code": "1007", "bank_name": "Banco Aliado", "account_type": "savings", "account_number": "0123-4567-12" } }
```

- `destination` es opcional: sin él se reintenta a la misma cuenta (útil cuando la pasarela no respondía).
- `bank_code` 3–6 dígitos, `account_type` `savings` o `checking`, `account_number` de 6 a 24 dígitos y guiones; si no,
  `400 invalid_request`.
- Responde `202` con el caso de nuevo en `approved`, `automatic_attempts: 0`, `manual_retries` + 1 y quién reintentó. La llave
  **no cambia**: un pago rechazado no movió dinero, y uno sin respuesta se resuelve con la misma llave sin pagar dos veces.
- `409 payout_not_retryable` si el caso no está en `payment_exception`.

## 8. Lo que ve el asegurado

`GET /v1/claims` y `GET /v1/claims/{claim_id}` devuelven el estado nuevo (`approved`, `paid`, `rejected`,
`payment_exception`), y el listado acepta `?status=` con cualquiera de ellos (criterio 5).
