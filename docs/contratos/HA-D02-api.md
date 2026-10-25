# Contrato de API — HA-D02 · Cobro de la prima sin pérdida ante caída de la pasarela

> **Fuente para el front (Angular) y para los dueños de HU-W1.3-02 y HU-W1.3-04.** Describe lo que implementa el servicio
> `origination` (`internal/billing`, `internal/adapters/{http/charge.go,postgres,payments}`, `pkg/idempotency`,
> `pkg/adapters/payments`). Contrato formal: `services/origination/api/openapi.yaml` **v0.7.0**. Alimenta la pantalla **E4b
> «Tu pago está pendiente»** (`/embebido/pago`) y el monitoreo de emisiones de operaciones (O1/O1b, HU-W1.3-04).

## 1. Flujo

```
aceptación (HU-W1.3-01) ──▶ suscripción aprueba ──▶ cobro pendiente (clave cobro-<acceptance_id>) ──▶ issuance_pending
                                                     │ en segundo plano, sin intervención manual
                                                     ├─ pasarela responde 2xx ─▶ confirmed ─▶ cobertura activa (policy_id)
                                                     │                                      └▶ documento contractual (HU-W1.3-03)
                                                     ├─ pasarela no responde ─▶ pending: reintento con la misma clave,
                                                     │                          espera 500 ms ×2 hasta 60 s, ventana de 24 h
                                                     │     └─ ventana vencida ─▶ failed (retry_window_expired) ─▶ excepción
                                                     └─ pasarela rechaza ────▶ failed (payment_rejected) ─▶ excepción
operaciones ──POST …/charge/resume (nota)──▶ failed → pending, misma clave, ventana nueva
```

- **Sin pérdida:** la aceptación queda registrada antes de cualquier cobro, y el cobro se persiste en PostgreSQL antes de llamar a la
  pasarela; un reinicio del servicio no lo pierde.
- **Sin duplicados:** la clave `cobro-<acceptance_id>` viaja en cada intento (cabecera `Idempotency-Key`) y un reclamo atómico en la
  base de datos deja que solo una tarea llame a la pasarela a la vez. La pasarela no cobra dos veces la misma clave (requisito de
  cualquier proveedor real).
- **Cobertura solo con el pago confirmado:** `policy_id` y `coverage_activated_at` son `null` hasta `confirmed`; hasta entonces
  `GET /v1/policies/pol-<acceptance_id>/issuance-status` responde `404 policy_not_found`. Con el pago confirmado, la emisión
  contractual y el correo de HU-W1.3-03 empiezan sin intervención.
- La clave `Idempotency-Key` **no** la manda el cliente: es interna entre originación y la pasarela.

## 2. Endpoints

Todas las respuestas llevan `Cache-Control: no-store` y exigen el ID token de Cognito (`401` sin token válido). Errores con
`{"error","message"}`; la pantalla decide el texto por `error`, nunca por `message`.

### `GET /v1/acceptances/{acceptance_id}/charge` → `200`

Para el asegurado dueño de la aceptación, `advisor` u `operations` (`403` para otro asegurado). Respuestas capturadas en la corrida local de
`tests/resilience/ha-d02` (2026-10-16):

Pago pendiente (la pasarela no responde):

```json
{
  "acceptance_id": "33d67a50-e029-4d64-a3a4-169860105f6f", "offer_id": "demo-active-r1792203776-down-0",
  "customer_id": "4fb109ad-9adf-49ab-b7af-0ee1461ea3bb", "accepted_at": "2026-10-17T02:24:19.899669Z",
  "status": "pending", "idempotency_key": "cobro-33d67a50-e029-4d64-a3a4-169860105f6f",
  "amount": 89500, "currency": "COP", "attempts": 2,
  "last_attempt_at": "2026-10-17T02:24:20.553451Z", "last_failure_at": "2026-10-17T02:24:20.55523Z",
  "next_attempt_at": "2026-10-17T02:24:21.55523Z",
  "retry_window": { "started_at": "2026-10-17T02:24:19.89983Z", "ends_at": "2026-10-18T02:24:19.89983Z" },
  "failure_reason": null, "confirmed_at": null, "policy_id": null, "coverage_activated_at": null,
  "activation_published_at": null, "resume_count": 0, "updated_at": "2026-10-17T02:24:20.55523Z"
}
```

Pago confirmado:

```json
{
  "acceptance_id": "c991b511-c743-47b0-88a1-dcda574c34cf", "status": "confirmed",
  "idempotency_key": "cobro-c991b511-c743-47b0-88a1-dcda574c34cf", "amount": 89500, "currency": "COP", "attempts": 0,
  "next_attempt_at": null, "confirmed_at": "2026-10-17T02:24:15.38623Z",
  "policy_id": "pol-c991b511-c743-47b0-88a1-dcda574c34cf", "coverage_activated_at": "2026-10-17T02:24:15.38623Z",
  "activation_published_at": "2026-10-17T02:24:15.388591Z", "…": "resto de campos como arriba"
}
```

| `status` | Qué pinta E4b |
|---|---|
| `pending` con `attempts: 0` / `in_progress` | «Estamos procesando tu pago» (primer intento en curso) |
| `pending` con `attempts > 0` | «Tu pago está pendiente»: aceptación guardada, clave de operación, intentos sin respuesta, próximo reintento (`next_attempt_at`) y la ventana (`retry_window.ends_at`) |
| `confirmed` | «Pago confirmado»; con `policy_id`, cobertura activa y la póliza (E5 de HU-W1.3-03 con `/v1/policies/{policy_id}/…`). Si `policy_id` aún no llega, seguir consultando |
| `failed` | Excepción: `retry_window_expired` (la pasarela no respondió en 24 h), `payment_rejected` (la pasarela rechazó; la pantalla de rechazo E4c es de HU-W1.3-02) o `payment_method_missing` (la ventana venció sin que el cliente entregara un medio de pago, HU-W1.3-02). Lo gestiona operaciones sin perder la aceptación |

- `attempts` cuenta los intentos fallidos de la ventana actual (incluidos los que no se enviaron porque el circuit breaker aislaba la
  pasarela).
- `activation_published_at` en `null` con `status: confirmed` significa que la publicación a la emisión contractual se está reintentando.
- `404 charge_not_found`: la aceptación aún no tiene cobro (suscripción en curso, en revisión o desconocida). `503 charge_unavailable`:
  reintentar.
- **Polling recomendado:** primera consulta a 1 s, luego cada 2 s; si `next_attempt_at` está a más de 2 s, consultar 1 s después de
  esa hora (tope de 61 s). Detenerse con `confirmed` y `policy_id`, con `failed` o ante 400, 401, 403 y 404.

### `GET /v1/issuance-exceptions?limit=` → `200` (solo `operations`)

Los cobros en excepción, el último en fallar primero (`limit` 1–200, por defecto 50), con `reason` `retry_window_expired`,
`payment_rejected` o `payment_method_missing`. Para la pantalla O1/O1b (HU-W1.3-04):

```json
{ "items": [ {
  "acceptance_id": "779a6c96-8fe9-4d75-9c46-ef77a7292882", "offer_id": "demo-active-r1792203776-expired-4",
  "customer_id": "4fb109ad-9adf-49ab-b7af-0ee1461ea3bb", "step": "premium_charge", "reason": "retry_window_expired",
  "attempts": 6, "idempotency_key": "cobro-779a6c96-8fe9-4d75-9c46-ef77a7292882", "amount": 89500, "currency": "COP",
  "accepted_at": "2026-10-17T02:25:03.022044Z", "failed_at": "2026-10-17T02:25:13.195176Z",
  "resume_count": 0, "last_resumed_at": null, "last_resumed_by": null
} ] }
```

`403` para `insured` y `advisor`; `400 invalid_request` con un `limit` inválido.

### `POST /v1/acceptances/{acceptance_id}/charge/resume` → `202` (solo `operations`)

Cuerpo `{"note": "…"}` (1 a 500 caracteres; obligatoria). Devuelve el cobro (`Charge`) otra vez en `pending`, con la **misma**
`idempotency_key`, `attempts: 0`, una ventana nueva de 24 h y `resume_count` + 1; el autor es el `sub` del token. Errores: `400
invalid_request` (sin nota o cuerpo inválido), `404 charge_not_found`, `409 charge_not_resumable` (el cobro no está en `failed`; de dos
reanudaciones a la vez gana una), `403` para otros roles.

**Un cobro que falló por `payment_method_missing` vuelve esperando el medio de pago, no vencido:** `pending` con
`next_attempt_at: null`, que es el estado con el que nace un cobro en HU-W1.3-02. Nunca tuvo tarjeta, así que dejarlo vencido
haría que el *worker* llamara a la pasarela sin token. El cliente paga desde E4 dentro de la ventana nueva. Los demás motivos
se reanudan como siempre, vencidos de inmediato.

## 3. Para quien tome HU-W1.3-02 (medio de pago, póliza) y HU-W1.3-04 (monitoreo)

- HU-W1.3-02 puede cambiar cuándo vence el primer intento (por ejemplo, tras capturar el medio de pago en E4) sin tocar el mecanismo; el
  `policy_id` hoy es `pol-<acceptance_id>` (fijado por HU-W1.3-03). Los eventos `ChargeCompleted` / `PolicyIssued` en SNS con *outbox*
  siguen pendientes de esa historia.
- HU-W1.3-04 construye O1/O1b sobre las dos rutas de operaciones; el escalamiento a los 30 min, los filtros y otros tipos de excepción
  son suyos. Ya están en [`HU-W1.3-04-api.md`](HU-W1.3-04-api.md): `GET /v1/issuances` (con filtros, conteos y la marca `unattended`),
  `POST /v1/issuances/{id}/retry` y `GET /v1/issuances/{id}/actions` (historial de acciones).

## 4. Lo que todavía es provisional

- Monto asegurado, plazo y coberturas del evento de activación: los de la oferta personalizada (HU-W1.2-03) cuando la oferta lo es, y
  los de la oferta que ajustó cuando es una ajustada (`adj-…`, D-5 a del fix C5); las de demostración (`demo-…`) y sus ajustadas activan
  los de demostración (250 000 000 COP, 120 meses, muerte e incapacidad total y permanente). Una personalizada sin términos completos
  no se cobra (fase AWS, D-16).
- Desde el fix C5 las aceptaciones están en PostgreSQL con `DATABASE_URL` (HU-W1.3-01): una aceptación aún sin cobro ya no se pierde
  con un reinicio. El cobro y las aceptaciones comparten el pool único de originación. Originación sigue con una sola tarea en AWS:
  pasar a varias tareas lo decide Nicolás aparte (las escrituras de la aceptación entre tareas son el bloqueo conocido, D-3 c).
- En AWS (fase AWS de HA-D02, `infra/terraform/modules/platform`): originación recibe su `DATABASE_URL` de Secrets Manager, los stubs
  `payment-gateway` y `email` corren detrás de Cloud Map, y `GET /v1/issuance-exceptions` y `POST …/charge/resume` pasan por el API
  Gateway con el authorizer de Cognito.
