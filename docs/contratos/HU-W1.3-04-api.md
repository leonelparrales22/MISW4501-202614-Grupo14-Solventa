# HU-W1.3-04 — Contrato para el monitoreo de emisiones y excepciones

Contrato de las rutas de originación que usa la pantalla de operaciones **O1/O1b** (Monitoreo de emisiones, detalle de la
excepción). Es una vista del analista de operaciones; las rutas son humanas (JWT de Cognito) y **solo `operations`** puede
usarlas. La definición completa está en `services/origination/api/openapi.yaml`. Se apoya en el cobro de la prima de
[HA-D02](HA-D02-api.md) y en la emisión contractual de HU-W1.3-03: las rutas `GET /v1/issuance-exceptions` y
`POST /v1/acceptances/{id}/charge/resume` no cambian.

## 1. Qué es una emisión y cómo se deriva su estado

Una emisión es una aceptación en camino a una póliza activa y notificada. El estado **no se guarda**: se deriva de la aceptación,
de su cobro y de su emisión contractual.

| `status` | Origen | Significado |
|---|---|---|
| `accepted` | Aceptación `registered` o `issuance_pending` **sin** cobro | El cliente aceptó; la suscripción aún no creó el cobro |
| `charging` | Cobro `pending` o `in_progress` | Se está cobrando (incluye la espera del medio de pago de E4) |
| `active` | Cobro `confirmed` y emisión contractual en curso (o sin crear aún) | Cobertura activa; el documento o el aviso están en camino |
| `notified` | Cobro `confirmed` y emisión contractual `completed` | El asegurado tiene su documento y fue notificado: fin del recorrido |
| `exception` | Cobro `failed`, o emisión contractual `failed` tras todos sus intentos | Necesita a un analista |

Las aceptaciones en revisión, ajustadas o rechazadas no son emisiones.

`step` dice **dónde está** la emisión o, en una excepción, **dónde falló**:

| `step` | Cuándo |
|---|---|
| `underwriting` | `accepted` |
| `awaiting_payment_method` | `charging` sin medio de pago aún (E4) |
| `premium_charge` | `charging`, o excepción del cobro |
| `contract_document` | `active` sin documento todavía, o excepción del generador de documentos |
| `notification` | `active` con documento y aviso pendiente, o excepción del correo |
| `completed` | `notified` |

`reason` (solo en una excepción): `retry_window_expired` («dependencia externa caída»: la ventana de 24 h de HA-D02 terminó sin que la
pasarela respondiera), `payment_rejected`, `payment_method_missing`, `document_generation_failed` y `notification_failed`.

## 2. Endpoints

### `GET /v1/issuances?status=&channel=&from=&to=&day_from=&limit=` → `200`

El más reciente aceptado primero. Todos los filtros son opcionales y se combinan:

- `status`: `accepted` | `charging` | `active` | `notified` | `exception`.
- `channel`: `web` | `mobile` | `advisor` (dónde se registró la aceptación; `advisor` es la compra que un asesor formaliza en nombre del cliente, HU-W1.1-03; el aliado no se guarda en la aceptación).
- `from`, `to`: fecha de aceptación, **inclusivas**, en RFC 3339; `to` no puede ser anterior a `from`.
- `day_from`: inicio del día del analista, para los conteos «de hoy» (por defecto, medianoche UTC).
- `limit`: 1–200, por defecto 50.

```json
{
  "counts": { "accepted": 18, "charging": 6, "charging_with_retries": 2, "active_today": 42, "notified_today": 40,
              "exceptions": 3, "unattended": 1 },
  "items": [ {
    "acceptance_id": "779a6c96-8fe9-4d75-9c46-ef77a7292882", "offer_id": "off-4f2a9c", "customer_id": "4fb109ad-…",
    "customer_name": "Luis Fernando Castro", "channel": "web", "status": "exception", "step": "premium_charge",
    "accepted_at": "2026-10-17T02:25:03Z", "updated_at": "2026-10-17T02:25:13Z", "amount": 89500, "currency": "COP",
    "attempts": 6, "policy_id": null, "reason": "retry_window_expired", "failed_at": "2026-10-17T02:25:13Z",
    "notified_at": null, "unattended": true, "resume_count": 0, "last_resumed_at": null, "last_resumed_by": null
  } ]
}
```

- **`counts` cubre todas las emisiones, sin importar los filtros**: son las tarjetas de arriba de O1. `active_today` son las pólizas
  activadas desde `day_from` (aunque después falle su documento o su aviso); `notified_today`, las notificadas desde entonces;
  `charging_with_retries`, las que ya fallaron al menos un intento en la pasarela; `unattended`, las excepciones sin analista.
- `customer_name` sale del titular del cobro o, mientras la aceptación no tiene cobro, del cliente registrado; es `null` si no se
  conoce. `amount` y `currency` son `null` en `accepted`; `reason` y `failed_at`, solo en una excepción; `attempts` son los del cobro
  y, con el cobro confirmado, los de la emisión contractual.
- **Sin resultados es `200` con `"items": []`**, nunca un error (CA-4).
- **`unattended`** (escalamiento del refinamiento): una excepción que lleva **30 minutos o más** sin que un analista la atienda.
  Solo marca: no envía alertas ni notificaciones. Una emisión sin excepción nunca lo está (CA-5).
- `400 invalid_request` (estado, canal, fechas o `limit` inválidos), `401`, `403` para `insured` y `advisor`, `503 charge_unavailable`.

### `POST /v1/issuances/{acceptance_id}/retry` → `202`

Reintenta **el paso que falló**, desde la aceptación registrada (CA-3). Cuerpo `{"note": "…"}` (1 a 500 caracteres; obligatoria).

- Si el **cobro** está en `failed`, es lo mismo que `POST /v1/acceptances/{id}/charge/resume` de HA-D02: vuelve a `pending` con la
  **misma** `idempotency_key` y una ventana nueva de 24 h. Responde `{"acceptance_id": "…", "action": "resume"}`.
- Si el cobro ya se confirmó y falló la **emisión contractual** (documento o aviso, tras todos sus intentos), la reabre: conserva el
  documento que ya tenga, reinicia los intentos y la deja vencida ya. Responde `{"acceptance_id": "…", "action": "retry"}`.

Errores: `400 invalid_request` (sin nota o cuerpo inválido), `404 issuance_not_found` (la aceptación no tiene cobro ni emisión
contractual), `409 issuance_not_retryable` (nada está fallido: otro analista o el proceso automático ya lo reintentó), `403` para otros
roles, `503 charge_unavailable`.

**Idempotente (CA-7):** el cobro conserva su clave y la reanudación es condicional a que esté `failed`; la emisión contractual se
reabre solo si está `failed`, y el aviso lleva su `Idempotency-Key`. Si dos analistas, o un analista y el proceso automático, coinciden,
solo uno gana y el otro recibe `409`. No hay doble cobro ni correo repetido.

### `GET /v1/issuances/{acceptance_id}/actions` → `200`

Lo que los analistas hicieron sobre la emisión, la más reciente primero. Sin acciones es `{"items": []}`.

```json
{ "items": [ { "id": 7, "action": "retry", "actor": "4fb109ad-…", "note": "El generador volvió", "created_at": "2026-10-17T03:10:00Z" } ] }
```

`action` es `resume` (se reintentó el cobro) o `retry` (la emisión contractual); `actor` es el `sub` del token del analista. Cada
acción que prospera deja su fila **en la misma sentencia SQL** que cambia el cobro o la emisión: nunca hay una acción de algo que no
ocurrió ni un cambio sin su acción. Un reintento rechazado (`409`, `404`) no deja nada. `403` para otros roles; `503`.

## 3. Datos

- `issuance_action` (migración `0009`): `id`, `acceptance_id`, `action`, `actor`, `note`, `created_at`. **Solo agrega:** triggers de
  PostgreSQL rechazan `UPDATE`, `DELETE` y `TRUNCATE` (`restrict_violation`), como las tablas de auditoría. Las reanudaciones
  anteriores a la migración solo se conocen por `resume_count`/`last_resumed_*` del cobro.
- `contract_issuance` (migración `0010`): la emisión contractual de HU-W1.3-03, que vivía en memoria. Sin ella no se podía distinguir
  una póliza activa de una notificada ni ver que el generador o el correo fallaron, y un reinicio las perdía. Con `DATABASE_URL`
  origination la usa; sin base de datos sigue en memoria. Varias tareas comparten la tabla: el trabajador **reclama** las filas
  vencidas con un arrendamiento de 2 minutos (`FOR UPDATE SKIP LOCKED`), así que dos tareas nunca avanzan la misma emisión. El registro de
  auditoría de la emisión contractual sigue en memoria (pendiente del servicio `audit`).

## 4. Lo que queda fuera

- **«En tiempo real»:** no hay empuje; el cliente consulta cada ~30 s y con un botón «Actualizar» (el refinamiento recomienda sondeo
  para no pasar de 5 SP).
- **Alerta real del escalamiento** (evento, correo o notificación a un responsable): hoy solo la marca `unattended`.
- **Gestión manual** distinta de reintentar, y excepciones de otros pasos que aún no existen (póliza, firma).
- **Aliado y nombre del asesor** (el mockup O1 muestra «Banco Aliado», «Asesor · L. Gómez»): la aceptación guarda `web`, `mobile` o
  `advisor` (HU-W1.1-03), pero no el aliado ni quién es el asesor; ese dato está en la formalización.
- **Código del banco** («código 51») y detalle del error de la pasarela: no se exponen.
