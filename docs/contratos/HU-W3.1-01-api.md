# Contrato de API — HU-W3.1-01 · Generación de credenciales para socio nuevo

Servicio: **socios** (`partners`). Consumidor: portal web de operaciones (*Socios comerciales* O10 y *Detalle del socio y
credenciales* O11). Evento hacia **auditoría** (`audit`).

El analista de operaciones registra un socio comercial (queda *pendiente de aprobación*) y lo aprueba. Al aprobarlo, `partners` crea en
Amazon API Gateway una **API key** y un **usage plan** propios del socio (cuota diaria y límite de ráfaga): credenciales únicas, con su
cuota y aisladas de las de los demás socios (CA-1). Cuando el socio excede su cuota, el gateway le responde `429` con el mensaje estándar
sin afectar a los demás (CA-2). Cada generación, rotación y revocación queda en el registro de auditoría de `audit` con la fecha y el
analista o proceso que la emitió (CA-3). El secreto se muestra **una sola vez**, en la respuesta de la aprobación o de la rotación; no
existe ninguna ruta para volver a leerlo, solo para rotarlo (CA-4). La rotación emite un par nuevo y deja el anterior vigente durante un
periodo de transición configurable antes de revocarlo (CA-5).

---

## 1. Cómo funciona

```
1. listado ─────────GET  /v1/partners──────────────────────────▶ socios + cuota y consumo de hoy + indicadores
2. registrar ───────POST /v1/partners──────────────────────────▶ socio en pending_approval (sin credenciales)
3. aprobar ─────────POST /v1/partners/{id}/approval────────────▶ API key + usage plan en API Gateway
                                                                 + credencial, historial y evento en una transacción
   ◀── 201 { partner, secret }   ← el secreto viaja solo aquí (CA-4)
4. detalle ─────────GET  /v1/partners/{id}─────────────────────▶ credenciales (sin secreto), cuota, consumo e historial
5. rotar ───────────POST /v1/partners/{id}/credential-rotations▶ API key nueva en el mismo usage plan; la anterior sigue
                                                                 vigente hasta expires_at (periodo de transición)
   ◀── 201 { partner, secret }
6. trabajador de partners: al vencer la transición, retira la key anterior del usage plan, la elimina y registra la revocación

partners ──outbox──▶ SNS «partners» ──▶ SQS audit ──▶ partner_credential_record (append-only, CA-3)
socio ──x-api-key──▶ API Gateway (usage plan del socio) ──▶ 429 Limit Exceeded / Too Many Requests solo a ese socio (CA-2)
```

- **Credencial = API key de API Gateway.** El socio envía el secreto en la cabecera `x-api-key`. `client_id` es el identificador
  público de la credencial (el nombre de la key en el gateway); `client_secret` es el valor de la key. `partners` genera el valor, se lo
  entrega al gateway y **no lo guarda**: solo conserva los últimos 4 caracteres (`secret_hint`).
- **Cuota diaria (decisión del equipo):** los usage plans de API Gateway solo admiten cuota por día, semana o mes, y su consumo es diario
  (en UTC). La cuota es por día y el consumo «de hoy» se lee del gateway (`GetUsage`). Si el gateway no responde, `usage` es `null` y la
  pantalla muestra «Sin dato»; el resto de la respuesta llega igual.
- **Durante la transición** las dos keys pertenecen al mismo usage plan; API Gateway lleva la cuota por key, así que en esas horas cada
  key tiene su propia cuota diaria (riesgo aceptado: dura como máximo el periodo de transición).
- **Quién:** `actor` = `{type: "operations", id: <sub del token>, label: <correo o usuario del token>}` o, para la revocación al vencer la
  transición, `{type: "system", id: "partners", label: "Sistema"}`.

Todas las rutas son humanas: `Authorization: Bearer <ID token de Cognito>`, rol **`operations`** y `Cache-Control: no-store`. Los roles
`advisor`, `insured` y `partner` reciben `403` y no se ejecuta nada; sin token, `401`.

---

## 2. Modelos

### `PartnerSummary` (cada fila del listado)

```json
{
  "partner_id": "ptn-5f0c2a9e6d7b4c1a8e3f9b2d4c6a8e10",
  "code": "ten_bancoaliado",
  "name": "Banco Aliado",
  "type": "bank",
  "status": "active",
  "credentials_status": "active",
  "usage_status": "normal",
  "registered_at": "2026-03-03T15:20:00Z",
  "approved_at": "2026-03-03T15:30:00Z",
  "quota": { "period": "day", "daily_limit": 5000, "rate_limit": 100, "burst_limit": 200 },
  "usage": { "date": "2026-11-01", "used": 3120, "remaining": 1880, "percent": 62.4 }
}
```

| Campo | Valores |
|---|---|
| `type` | `bank`, `airline`, `ecommerce`, `fintech`, `cooperative`, `retail`, `other` |
| `status` | `pending_approval` (registrado, sin credenciales), `active` (aprobado). `offboarded` queda reservado para la baja (HU-W3.1-02) |
| `credentials_status` | `none` (pendiente), `active`, `rotation_in_progress` (hay una key anterior en transición), `disabled` (reservado, baja) |
| `usage_status` | `normal`, `near_limit` (≥ 80 % de la cuota diaria), `exceeded` (≥ 100 %), `unknown` (sin dato del gateway), `none` (sin credenciales) |
| `usage` | `null` si el socio no tiene credenciales o el gateway no respondió. `percent` con un decimal |

### `PartnerDetail` (detalle, aprobación y rotación)

`PartnerSummary` más:

```json
{
  "products": ["vida hipotecario", "hogar"],
  "technical_contact_email": "api@bancoaliado.co",
  "environment": "staging",
  "registered_by": { "type": "operations", "id": "7c9e…", "label": "andres.ruiz@solventa.co" },
  "credentials": {
    "authentication": "api_key",
    "header": "x-api-key",
    "default_grace_period_hours": 24,
    "active": {
      "credential_id": "crd-0b6f1c…",
      "client_id": "sol_7f3a91c2e8d4b06f5a1c91e2",
      "secret_hint": "a1c9",
      "issued_at": "2026-07-01T14:00:00Z",
      "issued_by": { "type": "operations", "id": "7c9e…", "label": "andres.ruiz@solventa.co" }
    },
    "previous": {
      "credential_id": "crd-91aa…",
      "client_id": "sol_…",
      "secret_hint": "f00d",
      "issued_at": "2026-03-03T15:30:00Z",
      "expires_at": "2026-07-02T14:00:00Z"
    },
    "first_issued_at": "2026-03-03T15:30:00Z",
    "first_issued_by": { "type": "operations", "id": "7c9e…", "label": "andres.ruiz@solventa.co" },
    "last_rotated_at": "2026-07-01T14:00:00Z",
    "last_rotated_by": { "type": "operations", "id": "7c9e…", "label": "andres.ruiz@solventa.co" }
  },
  "history": [
    {
      "event_id": "pev-…",
      "type": "credentials_rotated",
      "occurred_at": "2026-07-01T14:00:00Z",
      "actor": { "type": "operations", "id": "7c9e…", "label": "andres.ruiz@solventa.co" },
      "status": "in_progress",
      "details": { "client_id": "sol_…", "previous_client_id": "sol_…", "grace_period_hours": 24, "previous_expires_at": "2026-07-02T14:00:00Z" }
    }
  ]
}
```

- `credentials` es `null` mientras el socio está en `pending_approval`. `previous` es `null` fuera de una transición; `last_rotated_*`
  son `null` si nunca se rotó.
- `history`: del más reciente al más antiguo. `type`: `registered`, `credentials_issued` (aprobación), `credentials_rotated`,
  `credentials_revoked` (fin de la transición). `status`: `applied`, o `in_progress` para una rotación cuya transición no ha terminado.
  `details` según el tipo: `credentials_issued` → `client_id`, `daily_limit`, `rate_limit`, `burst_limit`; `credentials_rotated` →
  `client_id`, `previous_client_id`, `grace_period_hours`, `previous_expires_at`; `credentials_revoked` → `client_id`, `reason`
  (`grace_period_ended`); `registered` → `{}`.
- `environment` es el `ENVIRONMENT` del servicio (`local`, `staging`, `production-demo`).

### `IssuedSecret` (solo en `201` de aprobación y rotación)

```json
{ "client_id": "sol_7f3a91c2e8d4b06f5a1c91e2", "client_secret": "Zp3…43 caracteres…", "header": "x-api-key" }
```

### Errores

`{"error": "<código>", "message": "<texto>"}`; la validación agrega `"fields": [{"field": "<campo>", "code": "required|invalid_format|out_of_range"}]`
con **todos** los campos inválidos a la vez (`400 validation_failed`).

---

## 3. `GET /v1/partners`

`200`:

```json
{
  "items": [ PartnerSummary, … ],
  "indicators": {
    "active_partners": 5,
    "average_quota_used_percent": 42.0,
    "near_limit_partners": 1,
    "near_limit_names": ["Aerolínea Aliada"]
  }
}
```

- Orden: activos primero, luego pendientes; dentro de cada grupo por nombre.
- `average_quota_used_percent`: promedio de `usage.percent` de los socios activos con dato (un decimal); `null` si ninguno tiene dato.
- `near_limit_*`: socios activos con `usage_status` `near_limit` o `exceeded`.

## 4. `POST /v1/partners`

```json
{
  "name": "Banco Aliado",
  "code": "ten_bancoaliado",
  "type": "bank",
  "products": ["vida hipotecario", "hogar"],
  "technical_contact_email": "api@bancoaliado.co",
  "quota": { "daily_limit": 5000, "rate_limit": 100, "burst_limit": 200 }
}
```

| Campo | Regla |
|---|---|
| `name` | 3–80 caracteres |
| `code` | `^[a-z][a-z0-9_]{2,39}$`, único (identificador del *tenant*) |
| `type` | uno de los de §2 |
| `products` | 1–10 elementos de 2–60 caracteres |
| `technical_contact_email` | correo válido, hasta 254 caracteres |
| `quota.daily_limit` | 1–10 000 000 |
| `quota.rate_limit` | 1–10 000 (solicitudes por segundo) |
| `quota.burst_limit` | `rate_limit`–20 000 |

`201` → `PartnerDetail` (`status: pending_approval`, `credentials: null`). `409 partner_code_taken` si el `code` ya existe.

## 5. `GET /v1/partners/{partner_id}`

`200` → `PartnerDetail`. `404 partner_not_found` (también para un id con formato imposible).

## 6. `POST /v1/partners/{partner_id}/approval`

Sin cuerpo. Aprueba al socio y genera sus credenciales (CA-1). En una sola operación: crea el usage plan (cuota diaria + ráfaga) y la API
key en el gateway, la asocia, y en una transacción deja el socio `active`, la credencial, la entrada `credentials_issued` del historial y
el evento `PartnerCredentialsIssued` en el *outbox*. Si algo falla, el gateway se deja como estaba (se borran la key y el plan creados) y
no se registra nada.

`201`:

```json
{ "partner": PartnerDetail, "secret": IssuedSecret }
```

Errores: `404 partner_not_found`; `409 partner_not_pending` (ya aprobado: el secreto no se vuelve a mostrar); `503 gateway_unavailable`
(el gateway no respondió o no está configurado; nada cambia).

## 7. `POST /v1/partners/{partner_id}/credential-rotations`

```json
{ "grace_period_hours": 24 }
```

`grace_period_hours` es opcional: 1–168; si falta, `default_grace_period_hours` (variable `CREDENTIAL_GRACE_PERIOD_HOURS`, 24 por
defecto). Crea una API key nueva en el **mismo** usage plan; la anterior sigue funcionando hasta `previous.expires_at` (CA-5). Al vencer,
el trabajador de `partners` la quita del plan, la elimina, registra `credentials_revoked` (actor `system`) y publica
`PartnerCredentialsRevoked`.

`201` → `{ "partner": PartnerDetail, "secret": IssuedSecret }`.

Errores: `400 validation_failed`; `404 partner_not_found`; `409 partner_not_active`; `409 rotation_in_progress` (ya hay una key en
transición: espera a que venza); `503 gateway_unavailable`.

---

## 8. Límite por socio (CA-2)

Lo aplica API Gateway por usage plan, nunca el servicio: cuando un socio agota su cuota diaria (`QUOTA_EXCEEDED`) o su ráfaga
(`THROTTLED`), el gateway responde `429` solo a sus peticiones, con el cuerpo estándar de Solventa:

```json
{ "error": "rate_limited", "message": "The request quota of this credential was exceeded; try again later." }
```

Los demás socios tienen su propio usage plan y no se ven afectados.

## 9. Eventos hacia `audit` (CA-3)

Tópico SNS `partners` (uno por dominio publicador), suscrito por la cola de `audit`. Ver el
[catálogo de eventos](../events/README.md#partnercredentialsissued-partnercredentialsrotated-y-partnercredentialsrevoked-hu-w31-01).
Nunca llevan el secreto ni su pista.
