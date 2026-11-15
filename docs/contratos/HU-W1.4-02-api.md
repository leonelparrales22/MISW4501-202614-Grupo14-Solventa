# Contrato de API — HU-W1.4-02 · Aplicación controlada del endoso

> **Fuente para el front (Angular).** Describe lo que implementa el servicio `origination`
> (`internal/adapters/http/endorsements.go`, `internal/endorsements/review.go`, `internal/adapters/postgres/endorsements.go`,
> `internal/domain/endorsement.go`, migración `0018_endorsement.sql`). Contrato formal: `services/origination/api/openapi.yaml`
> (0.18.0). Alimenta las pantallas del portal del analista de operaciones **O2 «Endosos»** (`/originacion/endosos`), **O3/O3b «Detalle
> del endoso»** (`/originacion/endosos/:id`) y **O4 «Historial de endosos»** (`/originacion/polizas/:id/endosos`). Las solicitudes las
> radica el asegurado con las rutas de HU-W1.4-01 ([`HU-W1.4-01-api.md`](HU-W1.4-01-api.md)), donde también ve el estado que deja el
> analista.

## 1. Flujo

```
O2 ──GET /v1/policies/endorsement-requests?change_type&status&filed_from&page&page_size──▶ página + indicadores
O2 «Revisar» ──POST …/endorsement-requests/{id}/review──▶ in_review ──▶ O3
O3 ──GET …/endorsement-requests/{id}──▶ cambio, póliza, validaciones (vista previa), lo decidido
O3 «Aplicar endoso» ──POST …/{id}/application──▶ applied (END-<año>-<número>) | not_applied (O3b) | 503 sin cambios
O3 «Rechazar» ──POST …/{id}/rejection {observations}──▶ rejected
O3b «Devolver al asegurado» ──POST …/{id}/return {observations}──▶ not_applied devuelta (el asegurado la ve rechazada)
O3 «Endosos previos» / «Ver historial» ──GET /v1/policies/{policy_id}/endorsements──▶ O4
```

- **Quién.** Solo el analista de operaciones (rol `operations`): sin token `401`; asegurado, asesor o socio `403`. Todas llevan
  `Cache-Control: no-store`. Publicadas por `/v1/policies/{proxy+}` del API Gateway (sin cambios de infraestructura de rutas, D-14 a).
- **Qué pólizas (D-2 a).** Las que emite originación (vida hipotecario).
- **Estados (D-8 a, D-9 a).** Los cinco de HU-W1.4-01. `returned` (en las respuestas) distingue una `not_applied` que el analista ya
  devolvió (tiene `decision_reason`):

| `status` | `returned` | Bandeja | Acción | Lo ve el asegurado |
|---|---|---|---|---|
| `filed` | `false` | Radicada | *Revisar* | Radicada |
| `in_review` | `false` | En revisión | *Continuar* | En revisión |
| `applied` | `false` | Aplicada | *Ver* | Aplicada |
| `not_applied` | `false` | No aplicado | *Continuar* (O3b: *Devolver al asegurado*) | En revisión |
| `not_applied` | `true` | No aplicado | *Ver* | Rechazada con las observaciones |
| `rejected` | `false` | Rechazada | *Ver* | Rechazada con las observaciones |

- **Estado contractual vigente (CA-3, D-3 a).** Al aplicar, las condiciones que resultan quedan como el último endoso de la póliza: la
  billetera (`GET /v1/policies`, web y móvil), las condiciones del asegurado (`…/current-terms`), las renovaciones y el listado del asesor
  las leen en el mismo `COMMIT`. Una suma asegurada nueva se tarifa con el motor `v1` (D-7 a); la diferencia de prima no se cobra.
- **Aviso por correo (D-12 a).** Aplicada, rechazada o devuelta: el asegurado recibe un correo con la decisión (las observaciones van
  en el de rechazo y en el de devolución). Una `not_applied` sin devolver no se avisa.

## 2. Validaciones de negocio (CA-2, D-4 a)

`validations` trae, en el orden de la pantalla, `{code, outcome, params}`; `outcome` es `passed` («Cumple»), `failed` («No cumple») o
`unverifiable` («No verificable»). Los textos los arma el front con `params`:

| `code` | Texto | Tipos | `params` |
|---|---|---|---|
| `shares_total_100` | Suma de participaciones igual a 100 % | beneficiarios | `shares` (`"60\|40"`), `total` (`"100"`) |
| `onerous_beneficiary_intact` | Beneficiario oneroso intacto | beneficiarios | `lender` (nombre o código), `basis` |
| `identity_documents_valid` | Documentos de identidad válidos (Registraduría) | beneficiarios | `checks` (documentos nuevos verificados), `not_found`, `document` (el primero que no está) |
| `no_open_claim` | Sin siniestro en curso que bloquee el cambio | beneficiarios, suma | `open_claims` |
| `requester_is_holder` | Solicitante = tomador autenticado | los tres | `requester` (nombre, si lo hay), `channel` |
| `insured_amount_within_limits` | Suma asegurada dentro de los límites del producto | suma | `requested`, `min`, `max` |
| `personal_data_valid` | Datos de contacto con formato válido | datos personales | `fields` (los inválidos, `"\|"`) |
| `policy_in_force` | Póliza vigente (guardia) | los tres | `coverage_end` |
| `previous_values_current` | Los valores anteriores siguen vigentes (guardia) | los tres | — |
| `no_renewal_in_progress` | Sin renovación aceptada pendiente (guardia) | suma | — |
| `premium_recalculated` | La prima nueva se pudo calcular (guardia) | suma | — |

Las guardias solo aparecen cuando no pasan. `validations_source`: `preview` (calculadas ahora, sin escribir nada, mientras la solicitud
se puede aplicar), `attempt` (las del último intento) o `none` (rechazada antes de que alguien intentara aplicarla).

## 3. Endpoints

### `GET /v1/policies/endorsement-requests` → `200`

Filtros opcionales: `change_type` (`beneficiaries`, `insured_amount`, `personal_data`), `status` (`pending` por defecto —radicadas, en
revisión y no aplicadas sin devolver—, `filed`, `in_review`, `applied`, `rejected`, `not_applied`, `all`), `filed_from` (día de Colombia
`AAAA-MM-DD`, incluido), `page` (≥ 1) y `page_size` (1–50, 20 por defecto). La más reciente primero. Un parámetro ilegible responde
`400 validation_failed` con todos los campos.

```json
{
  "items": [{
    "request_id": "SOL-MOD-2026-0042", "policy_id": "pol-acc-4f2a", "change_type": "beneficiaries", "channel": "web",
    "filed_at": "2026-09-16T20:10:00Z", "status": "filed", "status_changed_at": "2026-09-16T20:10:00Z", "returned": false,
    "decision_reason": null, "requester_name": "Carolina Pérez Ruiz",
    "holder": { "full_name": "Carolina Pérez Ruiz", "document_id": "1020345678" }
  }],
  "page": 1, "page_size": 20, "total_items": 6, "total_pages": 1,
  "indicators": {
    "pending": { "count": 5, "overdue": 2 },
    "applied_today": { "count": 8, "mean_time_to_apply_seconds": 12000 },
    "rejected_last_7_days": { "count": 1, "top_reason": "shares_total_100" }
  }
}
```

Los indicadores ignoran el filtro: `overdue` son las pendientes radicadas hace más de 24 h; `applied_today`, los endosos del día civil de
Colombia y el tiempo medio de radicación a aplicación (`null` sin ninguno); `rejected_last_7_days`, las rechazadas o no aplicadas en los
últimos 7 días y el código de la validación que más falló (o `analyst_rejection` si dominan los rechazos), `null` sin ninguna. El
documento del titular va sin su tipo (la póliza no lo guarda).

### `GET /v1/policies/endorsement-requests/{request_id}` → `200`

```json
{
  "request": { "request_id": "SOL-MOD-2026-0045", "change_type": "insured_amount", "status": "in_review", "returned": false, "…": "…" },
  "changes": [{ "field": "insured_amount", "previous": 250000000, "requested": 300000000 }],
  "premium": { "previous": 68500, "new": 82200, "currency": "COP", "rating_version": "v1" },
  "policy": {
    "policy_id": "pol-acc-4f2a", "line": "life", "holder": { "full_name": "Carolina Pérez Ruiz", "document_id": "1020345678" },
    "in_force": true, "coverage_start": "…", "coverage_end": "…", "insured_amount": 250000000, "premium": 68500, "currency": "COP",
    "onerous_beneficiary": { "partner_id": "aliado-banco-1", "name": "Banco Aliado", "basis": "outstanding_balance" },
    "endorsements": { "count": 1, "last": { "endorsement_id": "END-2026-0099", "change_type": "personal_data", "applied_at": "…" } }
  },
  "validations": [{ "code": "insured_amount_within_limits", "outcome": "passed",
                    "params": { "requested": "300000000", "min": "10000000", "max": "2000000000" } }],
  "validations_source": "preview",
  "decision": null
}
```

- `changes` tiene la forma de HU-W1.4-01 (`field`, `previous`, `requested`; un beneficiario es un objeto con nombre, parentesco,
  documento y participación). `premium` solo existe en una suma asegurada (`new` es `null` si el motor no pudo tarifar).
- `decision` (`null` mientras no hay decisión): `{outcome: applied | not_applied | rejected | returned, by: {name, role}, at,
  observations, endorsement_id, failed}`. `by.name` es el nombre del analista (o su correo si no tiene nombre).
- Nunca trae el `sub` ni el correo del asegurado (salvo en las filas de un cambio de datos personales).

### `POST …/endorsement-requests/{request_id}/review` → `200 {request}`

Toma una radicada (`filed` → `in_review`); tomarla otra vez no cambia nada. Otro estado: `409 request_already_decided`.

### `POST …/endorsement-requests/{request_id}/application` → `200`

```json
{ "outcome": "applied", "request": { "status": "applied", "…": "…" },
  "endorsement": { "endorsement_id": "END-2026-0102", "version": 2, "applied_at": "…", "by": { "name": "Andrés Ruiz", "role": "operations" } },
  "attempt": null, "validations": ["…"] }
```

```json
{ "outcome": "not_applied", "request": { "status": "not_applied", "returned": false, "…": "…" }, "endorsement": null,
  "attempt": { "at": "…", "by": { "name": "Andrés Ruiz", "role": "operations" }, "failed": ["identity_documents_valid"] },
  "validations": ["…"] }
```

- Aplicar una ya aplicada devuelve su endoso, y una no aplicada sin devolver, su intento (un doble clic no crea dos).
- `503 validation_unverifiable` con `unverifiable` (los códigos) cuando KYC, `claims` o la oferta de origen no responden: **no cambia
  nada** y la solicitud sigue como estaba (pantalla «No se pudieron verificar las validaciones»).
- `409 policy_changed`: otro endoso de la misma póliza se aplicó mientras este se validaba; nada cambió y al reintentar decide contra las
  condiciones nuevas.
- `409 request_already_decided` con `status`, `returned` y `decision_reason` actuales para una rechazada o devuelta.

### `POST …/{request_id}/rejection` y `POST …/{request_id}/return` → `200 {request}`

Cuerpo `{"observations": "…"}` (10–500 caracteres sin espacios al borde; las lee el asegurado). Sin observaciones válidas `422
observations_required`. *Rechazar* solo una radicada o en revisión (`409 request_already_decided`); *Devolver* solo una `not_applied`
sin devolver (`409 not_returnable`).

### `GET /v1/policies/{policy_id}/endorsements` → `200`

```json
{
  "policy_id": "pol-acc-4f2a", "holder": { "full_name": "Carolina Pérez Ruiz", "document_id": "1020345678" },
  "entries": [
    { "outcome": "not_applied", "at": "…", "endorsement_id": null, "request_id": "SOL-MOD-2026-0043", "change_type": "beneficiaries",
      "changes": ["…"], "premium": null, "failed": ["identity_documents_valid"], "by": { "name": "Andrés Ruiz", "role": "operations" } },
    { "outcome": "applied", "at": "…", "endorsement_id": "END-2026-0102", "request_id": "SOL-MOD-2026-0045", "change_type": "insured_amount",
      "changes": ["…"], "premium": { "previous": 68500, "new": 82200, "currency": "COP" }, "failed": [], "by": { "…": "…" } }
  ]
}
```

El más reciente primero; solo lectura (los registros no se editan ni se borran). Una póliza sin endosos responde `entries: []`; una que no
existe, `404 policy_not_found`.

## 4. Errores

`{"error": "<código>", "message": "…"}`: `401 unauthorized`, `403 forbidden`, `400 invalid_request` (id con caracteres de control o
cuerpo mal formado), `400 validation_failed` / `out_of_range` (consulta de la bandeja), `404 change_request_not_found` /
`policy_not_found`, `409 request_already_decided` / `not_returnable` / `policy_changed`, `422 observations_required`,
`503 validation_unverifiable` (con `unverifiable`) y `503 endorsement_unavailable` (reintentar).
