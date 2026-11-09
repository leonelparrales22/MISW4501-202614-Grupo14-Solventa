# Contrato de API — HA-L02 · Consulta ágil del estado del siniestro

> **Fuente para el móvil (Kotlin) y para HU-M2.4-01 y HU-M2.4-02.** Describe lo que implementa el servicio `claims`
> (`internal/readcache`, `internal/adapters/{redis,http/freshness.go}`). Contrato formal: `services/claims/api/openapi.yaml`.
> Alimenta las pantallas **M10 «Siniestro · línea de tiempo»** (pie «Actualizado hoy a las 14:30») y **M9c «Mis siniestros ·
> servicio degradado»** (aviso «La información podría no estar actualizada» y «Caché 14:30» en cada tarjeta).

## 1. Flujo

```
GET /v1/claims[/{id}[/history]]
  └─ base de datos, con 900 ms (CLAIM_READ_BUDGET_MS)
       ├─ responde ─────────────────────────────▶ se guarda en Redis ─▶ 200, stale=false, as_of=ahora
       ├─ responde 404 / 403 / 400 ──────────────▶ sale tal cual: la base contestó, no hay nada que degradar
       └─ tarda más de 900 ms o falla ──▶ Redis
                                            ├─ hay una lectura guardada ─▶ 200, stale=true, as_of=hora de esa lectura
                                            └─ no hay ──────────────────▶ 503 claims_unavailable (como antes)
```

- **Mismo cuerpo de siempre más dos campos.** `stale` y `as_of` se agregan a la lista, al detalle y al historial. Nada de lo
  que ya existía cambia, así que el portal web no se entera.
- **La caché es un respaldo, no un atajo.** En operación normal cada consulta va a la base. La caché solo responde cuando la
  base no llega a tiempo.
- **Frescura ≤ 5 minutos.** Cada lectura guardada vence a los 5 minutos, así que una respuesta de la caché nunca tiene más de
  5 minutos y un cambio de estado no queda escondido más de ese tiempo (criterio 4).
- **Un asegurado no ve lo de otro.** La clave de la caché lleva el `sub` del token de quien lee; una lectura de la caché solo
  devuelve algo que esa misma persona ya leyó de la base (criterio 4).

## 2. Endpoints

Los tres exigen el ID token de Cognito y llevan `Cache-Control: no-store`. Lista y detalle son del rol `insured`; el historial
también lo leen `advisor` y `operations`.

| Ruta | Pantalla |
|---|---|
| `GET /v1/claims` | M9 / M9c: *Mis siniestros*, en curso y cerrados |
| `GET /v1/claims/{claim_id}` | M10: encabezado del caso (radicado, tipo, póliza, fecha, lugar) |
| `GET /v1/claims/{claim_id}/history` | M10: línea de tiempo (cada cambio de estado con su hora) |

### Campos nuevos

| Campo | Tipo | Qué es |
|---|---|---|
| `stale` | booleano | `true` si la base no respondió en 900 ms y esta es la última lectura guardada; los datos pueden estar desactualizados |
| `as_of` | fecha-hora (UTC) | Cuándo se leyeron los datos de la base: ahora, si `stale` es `false`; la hora de la lectura guardada, si es `true` |

### Cabeceras nuevas (para monitoreo)

| Cabecera | Valores |
|---|---|
| `X-Stale` | `true` / `false` |
| `X-Data-Source` | `database` / `cache` |

### Ejemplo: lista desde la caché

```json
{
  "items": [
    {
      "claim_id": "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90",
      "filing_number": "SIN-2026-01655",
      "status": "in_evaluation",
      "channel": "mobile",
      "policy": { "policy_id": "HG-2025-880231", "line": null, "product": null, "insured_object": null },
      "incident_type": "other",
      "occurred_at": "2026-09-02T03:15:00Z",
      "filed_at": "2026-09-04T00:20:00Z"
    }
  ],
  "next_page_token": null,
  "stale": true,
  "as_of": "2026-11-08T19:30:00Z"
}
```

`as_of` en hora de Colombia es 14:30: el móvil muestra «Caché 14:30» en cada tarjeta y el aviso con «(hoy 14:30)».

## 3. Cómo lo usa el móvil

- **`stale = false`:** pie «Actualizado hoy a las HH:mm» con `as_of` en hora local.
- **`stale = true`:** aviso ámbar de M9c, botón *Reintentar ahora* (repite la consulta) y «Caché HH:mm» en las tarjetas.
- **`503 claims_unavailable`:** no hay ni base ni lectura guardada; la app muestra el error con *Reintentar*.

## 4. Configuración

| Variable | Valor por defecto | Qué hace |
|---|---|---|
| `REDIS_ADDR` | — | Sin ella no hay caché y las consultas solo leen la base |
| `CLAIM_READ_BUDGET_MS` | `900` | Cuánto espera cada consulta a la base (50–5000) |
| `CLAIM_READ_CACHE_TTL_SECONDS` | `300` | Cuánto vive cada lectura guardada (1–300) |

## 5. Evidencia

- Pruebas unitarias: `services/claims/internal/readcache`, `internal/adapters/redis`, `internal/adapters/http/freshness_test.go`.
- Prueba de carga: [`tests/load-k6/ha-l02`](../../tests/load-k6/ha-l02/README.md).
- Prueba instrumentada del cliente: `MisSiniestrosPantallaTest` y `DetalleSiniestroPantallaTest` en el repositorio móvil.
