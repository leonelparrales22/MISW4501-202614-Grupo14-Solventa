# Contrato de API — HU-W1.5-01 · Consulta de pólizas próximas a vencer

> **Fuente para el front (Angular).** Describe lo que implementa el servicio `origination`
> (`internal/adapters/http/expiring.go`, `internal/renewal/expiring.go`, `internal/adapters/postgres/expiring.go`,
> `internal/domain/expiring.go`). Contrato formal: `services/origination/api/openapi.yaml` (0.14.0). Alimenta la pantalla
> **S6 «Renovaciones»** del portal del asesor (`/originacion/renovaciones`), la entrada de la renovación asistida de HU-W1.5-03
> (`/originacion/renovaciones/:id`, con `:id` = `policy_id`).

## 1. Flujo

```
cobertura activada (HA-D02) ──▶ policy (proyección de HU-W1.5-02)
asegurado acepta la renovación en línea (HU-W1.5-02) ──▶ renewal del periodo: accepted
barrido de renovaciones (cada RENEWAL_POLL_INTERVAL_MS) ──▶ renewal: renewed | not_renewed

Renovaciones ──GET /v1/policies/expiring?expires_from&expires_to&status&page&page_size──▶ página + rango aplicado + indicador
   └─ fila con can_renew ──«Renovar»──▶ /originacion/renovaciones/<policy_id> (HU-W1.5-03)
   └─ fila renewal_in_progress ──«Ver»──▶ /originacion/renovaciones/<policy_id> (HU-W1.5-03)
```

- **Qué pólizas salen (decisiones D-1 a y D-3 a).** Las que emite originación (línea `life`) que siguen en vigor, cuyo vencimiento
  (`coverage_end`) cae en el rango y cuyo **periodo vigente está abierto**: sin renovación (`expiring`, «Por vencer») o con la renovación
  aceptada y en cobro (`renewal_in_progress`, «Renovación en curso»). Las renovadas, las no renovadas y las vencidas no salen. La póliza
  nueva de una renovación (`renewal_of`) vuelve a salir cuando su propio vencimiento entra en el rango. Las del núcleo de pólizas (Autos,
  Hogar, Viaje) no salen: originación no puede renovarlas.
- **Estado (decisión D-4 a).** Es la renovación del periodo tal como está guardada: una renovación cuyo cobro ya se confirmó se ve
  `renewal_in_progress` hasta que el barrido la cierra (un minuto como máximo con el valor por defecto). Listar no escribe nada.
- **Rango.** Días civiles de Colombia (UTC-5), los dos incluidos: `expires_from=2026-10-02` empieza a las 00:00 del 2 de octubre y
  `expires_to=2026-10-05` termina a las 23:59:59,999 del 5 de octubre. **Sin rango**, va de hoy a hoy + `RENEWAL_WINDOW_DAYS` (30 por
  defecto), la ventana de HU-W1.5-02; **solo con `expires_from`**, dura la ventana desde ese día; **solo con `expires_to`**, empieza hoy.
  La respuesta trae el rango aplicado (`expires_from`, `expires_to`) para pintarlo en los campos.
- **Renovar (decisión D-5 a).** `can_renew` es `true` solo **dentro de la ventana de renovación** y mientras el periodo no tenga renovación
  (la misma regla que el asegurado en HU-W1.5-02). Una póliza que vence más adelante sale como `expiring` con `can_renew: false`; la
  pantalla muestra «Renovable desde el {{renewal_window.opens_at}}».
- **Indicador.** `due_within_window` cuenta las pólizas con el periodo abierto que vencen dentro de la ventana desde ahora, **sin los
  filtros** («Vencen en {{window_days}} días»).
- **Cartera (P-1).** Todos los asesores ven todas las pólizas.
- **Ampliable (decisión D-6 a).** `status` puede ganar valores: HU-W1.6-02 agregará las canceladas por no pago. Un cliente muestra un
  valor desconocido con una insignia neutra.

## 2. Endpoint

### `GET /v1/policies/expiring` → `200`

ID token de Cognito con rol **`advisor`**: sin token `401`, con otro rol (`insured`, `operations`, `partner`) `403`. Lleva
`Cache-Control: no-store`. Publicado en AWS por el recurso `/v1/policies/{proxy+}` del API Gateway (sin cambios de infraestructura).

| Parámetro | Valor | Por defecto |
|---|---|---|
| `expires_from` | Primer día del rango, `AAAA-MM-DD` (Colombia) | hoy |
| `expires_to` | Último día del rango, `AAAA-MM-DD`; no antes de `expires_from` | `expires_from` + `RENEWAL_WINDOW_DAYS` |
| `status` | `expiring` o `renewal_in_progress` | los dos |
| `page` | 1 o más | 1 |
| `page_size` | 1 a 50 | 20 |

Orden: vencimiento más próximo primero y, a igual vencimiento, por `policy_id`. Respuesta real (prueba de integración con PostgreSQL,
2026-11-08, sin rango: hoy → hoy + 30; una póliza por vencer y otra con la renovación en línea aceptada):

```json
{
  "items": [
    {
      "policy_id": "pol-acc-4f2a", "line": "life", "product": null,
      "holder": { "full_name": "Carolina Pérez", "document_type": null, "document_id": "1032456789" },
      "premium": 142300, "currency": "COP", "coverage_end": "2026-11-18T21:20:37.795463Z",
      "status": "expiring", "can_renew": true,
      "renewal_window": { "opens_at": "2026-10-19T21:20:37.795463Z", "closes_at": "2026-11-18T21:20:37.795463Z" },
      "renewal": null
    },
    {
      "policy_id": "pol-acc-7c1d", "line": "life", "product": null,
      "holder": { "full_name": "Carolina Pérez", "document_type": null, "document_id": "1032456789" },
      "premium": 142300, "currency": "COP", "coverage_end": "2026-11-28T21:20:37.806619Z",
      "status": "renewal_in_progress", "can_renew": false,
      "renewal_window": { "opens_at": "2026-10-29T21:20:37.806619Z", "closes_at": "2026-11-28T21:20:37.806619Z" },
      "renewal": {
        "renewal_id": "ren-pol-acc-7c1d-20261128", "status": "accepted", "reason": null, "new_policy_id": null,
        "charge_id": "ren-pol-acc-7c1d-20261128", "accepted_at": "2026-11-08T21:20:37.812308Z",
        "retry_until": "2026-11-11T21:20:37.812308Z", "closed_at": null
      }
    }
  ],
  "expires_from": "2026-11-08", "expires_to": "2026-12-08", "window_days": 30,
  "due_within_window": 2, "total": 2, "page": 1, "page_size": 20
}
```

- `holder` es el asegurado principal **sin correo** (el asesor no lo necesita en esta pantalla). `document_type` es `null`: la póliza no
  guarda el tipo de documento (igual que la billetera de HU-M1.2-01).
- `line` es siempre `life` y `product` `null` para las pólizas de originación; la columna «Producto» la traduce el cliente desde `line`
  (decisión D-8 b, sin filtro de producto).
- `renewal` es el mismo resumen de `GET /v1/policies` (HU-W1.5-02): `null` sin renovación del periodo.
- `total` cuenta todas las que cumplen el filtro; el paginador «Anterior / Siguiente» se muestra solo si `total > page_size`.

**Rango sin pólizas (CA-3)** → `200`, lista vacía, nunca un error (el indicador no cambia):

```json
{ "items": [], "expires_from": "2027-05-08", "expires_to": "2027-05-15", "window_days": 30,
  "due_within_window": 2, "total": 0, "page": 1, "page_size": 20 }
```

### Errores

| Código | `error` | Cuándo |
|---|---|---|
| `400` | `out_of_range` | `expires_to` antes de `expires_from`: `{"error":"out_of_range","message":"expires_to must not be before expires_from"}` |
| `400` | `invalid_request` | Fecha que no es `AAAA-MM-DD` (`{"error":"invalid_request","message":"expires_from must be a date (AAAA-MM-DD)"}`), `status` desconocido, `page` o `page_size` fuera de rango |
| `401` | `unauthorized` | Sin token o con token inválido |
| `403` | `forbidden` | El rol no es `advisor` |
| `503` | `renewal_unavailable` | La base de originación no respondió; reintentar |

## 3. Datos

- Solo lee las tablas `policy` y `renewal` de HU-W1.5-02 (migración `0012`): `policy LEFT JOIN renewal` por póliza y periodo
  (`period_end = coverage_end`). Sin migración: usa `policy_coverage_end_idx` y el índice único de `renewal`.
- La página y el total se leen en la misma transacción de solo lectura, así cuadran.

## 4. Lo que queda fuera

- La renovación asistida (recotizar, aceptación del cliente, formalizar, cobro y emisión) y la página `/originacion/renovaciones/:id`:
  **HU-W1.5-03**. Esta historia entrega el `policy_id` y `can_renew` para el enlace.
- La tarjeta «Canceladas por no pago», su estado y su filtro: **HU-W1.6-02** (originación no cancela pólizas todavía).
- Las pólizas del núcleo de pólizas (Autos, Hogar, Viaje) y la cartera por asesor.
- Las pólizas anteriores a HU-W1.5-02 no están en `policy` (no hay *backfill*) y no salen.
