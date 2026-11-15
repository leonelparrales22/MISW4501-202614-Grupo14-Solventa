# Contrato de API — HU-W1.4-01 · Solicitud de modificación por el asegurado (autogestión)

> **Fuente para el front (Angular).** Describe lo que implementa el servicio `origination`
> (`internal/adapters/http/change_requests.go`, `internal/endorsements/`, `internal/adapters/postgres/change_requests.go`,
> `internal/domain/change_request.go`, migración `0016_change_request.sql`). Contrato formal: `services/origination/api/openapi.yaml`
> (0.16.0). Alimenta las pantallas del portal del asegurado **A3 «Solicitar una modificación»** (`/originacion/polizas/:id/modificar`),
> **A3b «Solicitud radicada»** (`/originacion/solicitudes/:id`) y **A11 «Mis solicitudes»** (`/originacion/solicitudes`).
> Revisar y aplicar la solicitud es de HU-W1.4-02, que reutiliza este modelo.

## 1. Flujo

```
Mis pólizas (A1) ──«Solicitar modificación»──▶ A3
A3 ──GET /v1/policies/{policy_id}/current-terms──▶ condiciones vigentes, vigencia, abiertas por tipo, límites
A3 ──POST /v1/policies/{policy_id}/change-requests──▶ 201 SOL-MOD-<año>-<número> (radicada) ──▶ A3b
A11 ──GET /v1/policies/change-requests?status&policy_id&filed_from&page&page_size──▶ página + indicadores + pólizas
A3b / «Ver detalle» ──GET /v1/policies/change-requests/{request_id}──▶ la solicitud con su estado
HU-W1.4-02 (analista) ──▶ in_review ──▶ applied | rejected | not_applied (con o sin motivo)
```

- **Qué pólizas (D-2 a).** Solo las que emite originación (vida hipotecario) y están **vigentes** (`coverage_start ≤ ahora <
  coverage_end`). Una póliza del núcleo (Autos, Hogar, Viaje) responde `404 policy_not_found`.
- **Quién (D-11 a, CA-4).** Solo el asegurado dueño (`sub` del token = dueño de la póliza), con el rol `insured`. Otro rol responde `403`
  sin leer nada; otro asegurado, `403` sin radicar nada. Nadie radica en nombre del asegurado.
- **Tipos (CA-1).** `beneficiaries` (los beneficiarios del remanente, que **reemplazan** a los designados), `insured_amount` (valor
  asegurado) y `personal_data` (dirección, **correo de contacto de la póliza** —nunca el de inicio de sesión, D-5 a— y teléfono).
- **Validaciones (CA-3, D-3 a y D-4 a).** Todas las reglas de la captura se revisan a la vez en el servicio (las mismas que el
  formulario): ver §3. Si alguna falla, `422` con **todos** los campos y no se radica nada ni se consume número.
- **Una abierta por póliza y tipo (D-6 a, ajuste A-2).** Abierta = `filed`, `in_review` o `not_applied` **sin** `decision_reason`. Otra del
  mismo tipo responde `409 change_request_open` con el `request_id` de la abierta (para enlazarla), también si dos se radican a la vez.
  Tipos distintos sí pueden estar abiertos al mismo tiempo.
- **Identificador (D-7 a, CA-2).** `SOL-MOD-<año civil de Colombia>-<número de al menos cuatro dígitos>`, consecutivo por año y sin huecos.
- **Estados (D-8 a, ajuste A-1).** El API devuelve el estado guardado (cinco valores). El asegurado lo ve así:

| `status` guardado | `decision_reason` | Lo que ve el asegurado |
|---|---|---|
| `filed` | `null` | Radicada |
| `in_review` | `null` | En revisión |
| `applied` | `null` | Aplicada |
| `rejected` | el motivo | Rechazada, con el motivo |
| `not_applied` | `null` | En revisión (el analista sigue trabajándola) |
| `not_applied` | el motivo | Rechazada, con el motivo (devuelta al asegurado) |

## 2. Endpoints

ID token de Cognito con rol **`insured`** en los cuatro: sin token `401`; con otro rol (`advisor`, `operations`, `partner`) `403`. Llevan
`Cache-Control: no-store`. Publicados en AWS por los recursos `/v1/policies` y `/v1/policies/{proxy+}` del API Gateway (sin cambios de
infraestructura de rutas, D-15 a). El segmento fijo `change-requests` no tapa las rutas de una póliza.

### `GET /v1/policies/{policy_id}/current-terms` → `200`

```json
{
  "policy": {
    "policy_id": "pol-acc-4f2a", "line": "life", "holder_name": "Carolina Pérez",
    "coverage_start": "2026-07-16T12:00:00Z", "coverage_end": "2027-07-16T12:00:00Z", "in_force": true
  },
  "terms": {
    "insured_amount": 68000000, "currency": "COP",
    "beneficiaries": [],
    "onerous_beneficiary": { "partner_id": "aliado-banco-1", "name": "Banco Aliado", "basis": "outstanding_balance" },
    "personal_data": { "address": null, "email": "carolina@example.com", "phone": null }
  },
  "open_requests": [
    { "request_id": "SOL-MOD-2026-0041", "change_type": "insured_amount", "status": "in_review", "filed_at": "2026-09-16T12:00:00Z" }
  ],
  "limits": {
    "insured_amount_min": 10000000, "insured_amount_max": 2000000000, "max_beneficiaries": 10,
    "relationships": ["spouse_or_partner", "child", "parent", "sibling", "grandchild", "other"],
    "document_types": ["CC", "TI", "RC", "CE", "PA"]
  }
}
```

- `beneficiaries`: hoy siempre `[]` («sin beneficiarios designados» para el remanente) hasta que HU-W1.4-02 aplique un cambio.
- `onerous_beneficiary`: el aliado que otorgó el crédito (el de la oferta original), **no modificable**. `name` sale de
  `PARTNER_DISPLAY_NAMES` (D-14 a); `null` si el aliado no tiene nombre (la pantalla muestra el código). En una venta del asesor no hay
  aliado: `partner_id` y `name` son `null` y la pantalla muestra una etiqueta genérica («Entidad que otorgó el crédito»). No hay NIT.
- `personal_data`: `address` y `phone` son `null` mientras no estén registrados («No registrado»).
- `in_force: false` → la pantalla muestra la póliza como no vigente y no deja radicar.
- `open_requests`: la abierta de cada tipo que tenga una; ese tipo queda deshabilitado con su enlace.

### `POST /v1/policies/{policy_id}/change-requests` → `201`

Cuerpo: `change_type`, `channel` (`web` en el portal, `mobile` reservado) y **solo** el miembro de ese tipo. Lo que el contrato no nombra
(también el beneficiario oneroso) o un miembro de otro tipo responde `400 invalid_request`.

```json
{ "change_type": "beneficiaries", "channel": "web", "beneficiaries": [
  { "full_name": "Mateo Pérez Gómez", "relationship": "child", "document_type": "TI", "document_number": "1140223001", "share_percent": 60 },
  { "full_name": "Lucía Pérez Gómez", "relationship": "child", "document_type": "TI", "document_number": "1142998310", "share_percent": 40 }
] }
```

```json
{ "change_type": "insured_amount", "channel": "web", "insured_amount": 80000000 }
```

```json
{ "change_type": "personal_data", "channel": "web", "personal_data": { "address": "Calle 10 # 43-20, Medellín", "phone": "+57 300 123 4567" } }
```

Respuesta (`Location: /v1/policies/change-requests/SOL-MOD-2026-0042`):

```json
{
  "request_id": "SOL-MOD-2026-0042", "policy_id": "pol-acc-4f2a", "change_type": "beneficiaries", "channel": "web",
  "status": "filed", "status_changed_at": "2026-09-16T12:00:00Z", "decision_reason": null, "filed_at": "2026-09-16T12:00:00Z",
  "requested_by": { "email": "carolina@example.com", "name": null },
  "previous": {
    "beneficiaries": [],
    "onerous_beneficiary": { "partner_id": "aliado-banco-1", "name": "Banco Aliado", "basis": "outstanding_balance" }
  },
  "requested": {
    "beneficiaries": [
      { "full_name": "Mateo Pérez Gómez", "relationship": "child", "document_type": "TI", "document_number": "1140223001", "share_percent": 60 },
      { "full_name": "Lucía Pérez Gómez", "relationship": "child", "document_type": "TI", "document_number": "1142998310", "share_percent": 40 }
    ]
  },
  "changes": [
    { "field": "beneficiaries", "previous": null, "requested": { "full_name": "Mateo Pérez Gómez", "relationship": "child", "document_type": "TI", "document_number": "1140223001", "share_percent": 60 } },
    { "field": "beneficiaries", "previous": null, "requested": { "full_name": "Lucía Pérez Gómez", "relationship": "child", "document_type": "TI", "document_number": "1142998310", "share_percent": 40 } }
  ]
}
```

- `previous` es la **foto del valor vigente al radicar** (con el oneroso en un cambio de beneficiarios); `requested`, lo pedido. En
  `personal_data`, `requested` trae `null` en lo que no cambia.
- `changes` es la diferencia por campo (la tabla «Campo / Valor anterior / Valor nuevo» del analista): `beneficiaries` empareja por tipo y
  número de documento (alta con `previous: null`, baja con `requested: null`, cambio con los dos); `insured_amount` con números;
  `personal_data.address`, `personal_data.email` y `personal_data.phone` con textos.
- `requested_by.name` es `null` cuando la cuenta no tiene nombre: la pantalla muestra el correo («Radicada por … · portal web»).
- El teléfono se guarda como sus 10 dígitos (sin `+57`, espacios ni guiones).

### `GET /v1/policies/change-requests` → `200`

| Parámetro | Valor | Por defecto |
|---|---|---|
| `status` | `filed`, `in_review`, `applied` o `rejected`, **como lo ve el asegurado**: `in_review` incluye `not_applied` sin motivo y `rejected` incluye `not_applied` con motivo | todos |
| `policy_id` | Una póliza del asegurado | todas |
| `filed_from` | Primer día de radicación, `AAAA-MM-DD` (Colombia), incluido | sin límite |
| `page` | 1 o más | 1 |
| `page_size` | 1 a 50 | 20 |

```json
{
  "items": [
    { "request_id": "SOL-MOD-2026-0042", "policy_id": "pol-acc-4f2a", "change_type": "beneficiaries", "channel": "web",
      "status": "filed", "status_changed_at": "2026-09-16T12:00:00Z", "decision_reason": null, "filed_at": "2026-09-16T12:00:00Z" }
  ],
  "page": 1, "page_size": 20, "total_items": 1, "total_pages": 1,
  "indicators": {
    "open": { "count": 1, "latest": { "request_id": "SOL-MOD-2026-0042", "change_type": "beneficiaries",
      "filed_at": "2026-09-16T12:00:00Z", "status_changed_at": "2026-09-16T12:00:00Z" } },
    "applied_this_year": { "count": 0, "latest": null },
    "rejected": { "count": 0, "latest": null }
  },
  "policies": ["pol-acc-4f2a"]
}
```

- Orden: la radicada más reciente primero. Solo las del asegurado que llama.
- `indicators` **no** depende de los filtros: `open` = abiertas (con la más reciente radicada), `applied_this_year` = aplicadas en el año
  civil (con la última aplicada), `rejected` = rechazadas más las no aplicadas devueltas con motivo (con la última).
- `policies`: las pólizas del asegurado que tienen solicitudes (opciones del filtro «Póliza»).
- La fila no trae los datos de los beneficiarios: el detalle sí.

### `GET /v1/policies/change-requests/{request_id}` → `200`

La misma forma de la respuesta del `POST`. Otro asegurado: `403`. Un id que no tiene la forma `SOL-MOD-<año>-<número>` o que no existe:
`404 change_request_not_found`.

### Errores

| Código | `error` | Cuándo |
|---|---|---|
| `400` | `invalid_request` | Cuerpo que no es JSON, un miembro que el contrato no nombra (también `onerous_beneficiary`) o de otro tipo; `policy_id` o `request_id` de más de 128 caracteres |
| `400` | `validation_failed` | Lista: cada parámetro ilegible a la vez, en `fields` (`status`, `policy_id`, `filed_from` con `invalid_format`; `page`, `page_size` con `invalid_format` u `out_of_range`) |
| `401` | `unauthorized` | Sin token o con token inválido |
| `403` | `forbidden` | El rol no es `insured`, o la póliza o la solicitud son de otro asegurado |
| `404` | `policy_not_found` | La póliza no existe en originación (o es del núcleo de pólizas) |
| `404` | `change_request_not_found` | La solicitud no existe |
| `409` | `policy_not_in_force` | La póliza no está vigente |
| `409` | `change_request_open` | Ya hay una abierta del mismo tipo: `{"error":"change_request_open","message":"…","request_id":"SOL-MOD-2026-0041"}` |
| `422` | `validation_failed` | Todos los campos inválidos a la vez (§3) |
| `503` | `change_request_unavailable` | La base de originación no respondió; reintentar |

## 3. Reglas de la captura (`422 validation_failed`, `fields[].field` y `fields[].code`)

| Tipo | Campo | Regla | Códigos |
|---|---|---|---|
| todos | `change_type`, `channel` | Uno de los valores del contrato | `required`, `invalid_format` |
| `beneficiaries` | `beneficiaries` | De 1 a 10; participaciones que suman **exactamente 100**; distinto de los vigentes | `required`, `out_of_range`, `share_total_not_100`, `no_change` |
| `beneficiaries` | `beneficiaries[i].full_name` | 3 a 120 caracteres con al menos una letra (espacios colapsados) | `required`, `invalid_format` |
| `beneficiaries` | `beneficiaries[i].relationship` | `spouse_or_partner`, `child`, `parent`, `sibling`, `grandchild`, `other` | `required`, `invalid_format` |
| `beneficiaries` | `beneficiaries[i].document_type` | `CC`, `TI`, `RC`, `CE`, `PA` | `required`, `invalid_format` |
| `beneficiaries` | `beneficiaries[i].document_number` | 5 a 20 letras o dígitos; solo dígitos en `CC`, `TI` y `RC`; sin repetir tipo y número | `required`, `invalid_format`, `duplicate` |
| `beneficiaries` | `beneficiaries[i].share_percent` | Mayor que 0 y hasta 100, con hasta dos decimales | `required`, `invalid_format`, `out_of_range` |
| `insured_amount` | `insured_amount` | Entero en COP entre 10 000 000 y 2 000 000 000, distinto del actual | `required`, `invalid_format`, `out_of_range`, `no_change` |
| `personal_data` | `personal_data` | Al menos uno de `address`, `email`, `phone` | `required` |
| `personal_data` | `personal_data.address` | 5 a 160 caracteres (espacios colapsados), distinta de la actual | `required`, `invalid_format`, `no_change` |
| `personal_data` | `personal_data.email` | Correo válido de hasta 254 caracteres, distinto del actual (sin distinguir mayúsculas) | `required`, `invalid_format`, `no_change` |
| `personal_data` | `personal_data.phone` | 10 dígitos de Colombia, `+57` opcional; se ignoran espacios y guiones | `required`, `invalid_format`, `no_change` |

Mientras una participación sea inválida no se reporta además `share_total_not_100`. Cada campo aparece una sola vez.

## 4. Datos

- Tablas `change_request_counter` (consecutivo por año) y `change_request` (migración `0016`). El número y la solicitud se escriben en
  **una** transacción: si choca con una abierta, se revierte también el número.
- El índice único parcial `change_request_one_open` (póliza, tipo) decide entre dos radicaciones simultáneas, sin bloqueos.
- El contenido (todo salvo `status`, `status_changed_at` y `decision_reason`) no cambia; sin borrado (disparador). `decision_reason` es
  `NULL` o un texto no vacío.
- No se publica ningún evento: HU-W1.4-02 lee la misma base de originación.

## 5. Lo que queda fuera

- Revisar, validar contra fuentes externas, aplicar o rechazar, y avisar el resultado por correo: HU-W1.4-02.
- Adjuntar soportes, cambiar nombre o documento del titular o el correo de inicio de sesión, retirar una solicitud radicada.
- El canal móvil (D-10 a): el API ya acepta `channel: mobile`.
