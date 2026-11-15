# Contrato de API — HU-W3.2-01 · Tablero de integración del socio (resumen)

> **Fuente para el front (Angular).** Describe lo que implementa el servicio `origination`
> (`internal/adapters/http/partner_metrics.go`, `internal/partnermetrics/service.go`,
> `internal/adapters/postgres/partner_metrics.go`, `internal/domain/partner_metrics.go`). Contrato formal:
> `services/origination/api/openapi.yaml` (0.16.0). Alimenta la pantalla **B1 «Tablero de integración»** del portal del socio
> (`/socios/tablero`).

## 1. Qué cuenta

```
socio ──POST /v1/offers (API key)──▶ offer (canal embedded, partner_id del socio)      ← cotización solicitada
asegurado acepta ──▶ acceptance ──▶ cobro confirmado (HA-D02) ──▶ policy (coverage_start) ← póliza emitida

Tablero ──GET /v1/partner-metrics/summary?from&to──▶ cotizaciones, pólizas y conversión del socio del token
```

- **Decisión del equipo:** el resumen lo calcula `origination`, dueño de las ofertas y las pólizas, en lugar de copiarlas a `partners`
  con eventos. El módulo `dashboard` de `partners` sigue reservado para lo que no esté en originación.
- **Cotizaciones solicitadas.** Ofertas del canal embebido del socio creadas en el rango. No cuentan la oferta que ajusta el asesor
  (`supersedes`) ni la renovación de una oferta (`renews`), porque no son pedidos del socio.
- **Pólizas emitidas.** Pólizas nuevas (no renovaciones) de ofertas del socio cuya cobertura empezó en el rango, que es cuando se confirmó
  la prima. Una cotización de un día anterior que se emite dentro del rango cuenta como póliza del rango. Sin el cobro de la prima
  (`PAYMENT_GATEWAY_URL`) no se emite ninguna póliza.
- **Tasa de conversión (CA-1).** `policies_issued / quotes_requested × 100` con un decimal. **Sin cotizaciones es `0`**, nunca un error.
- **Rango (CA-2).** Días civiles de Colombia (UTC-5), los dos incluidos: `from=2026-09-10&to=2026-09-16` va de las 00:00 del 10 a las
  23:59:59,999 del 16. Máximo 366 días. El front calcula «Hoy», «Últimos 7 días» (hoy y los 6 anteriores) y «Últimos 30 días» como
  rangos y los manda igual que el personalizado.
- **Sin datos (CA-3).** Un rango sin actividad responde `0`, `0` y `0`, con `200`.
- **Aislamiento (CA-4).** El socio es **siempre** el claim `custom:partner_id` del token verificado (HA-S01). La petición no puede nombrar
  otro socio: un `partner_id` en la query se ignora. Un usuario del portal del socio sin ese claim recibe `403`. El filtro por socio se
  aplica en SQL, así que las filas de otro socio no salen de la base.

## 2. Endpoint

### `GET /v1/partner-metrics/summary` → `200`

ID token de Cognito con rol **`partner`**: sin token `401`; con otro rol (`insured`, `advisor`, `operations`) o sin
`custom:partner_id`, `403`. Lleva `Cache-Control: no-store`. Publicado en AWS como ruta humana `partner-metrics` del API Gateway
(autorizador de Cognito); el ALB la lleva a `origination`.

| Parámetro | Valor |
|---|---|
| `from` | Primer día del rango, `AAAA-MM-DD` (Colombia). Obligatorio |
| `to` | Último día del rango, `AAAA-MM-DD`; no antes de `from`, máximo 366 días. Obligatorio |

```json
{
  "partner_id": "socio-a",
  "from": "2026-08-18",
  "to": "2026-09-16",
  "quotes_requested": 12480,
  "policies_issued": 1872,
  "conversion_rate": 15
}
```

### Errores

| Estado | `error` | Cuándo |
|---|---|---|
| 400 | `invalid_request` | Falta `from` o `to`, o no son `AAAA-MM-DD` |
| 400 | `out_of_range` | `to` es anterior a `from` |
| 400 | `range_too_long` | El rango pasa de 366 días |
| 401 | `unauthorized` | Sin token o con token inválido |
| 403 | `forbidden` | Rol distinto de `partner`, o usuario sin `custom:partner_id` |
| 503 | `metrics_unavailable` | No se pudo leer la base; reintentar |

## 3. Datos y despliegue

- Migración `0016_partner_metrics.sql`: dos índices parciales, `offer (partner_id, created_at)` de las ofertas embebidas originales y
  `policy (coverage_start)` de las pólizas nuevas. No hay tablas ni eventos nuevos.
- Se monta con `DATABASE_URL` y `AUTH_ISSUER`. No hay variables nuevas.
- Infraestructura: ruta humana `partner-metrics` (solo `GET`, la raíz sin método) en `modules/platform` y el prefijo `/v1/partner-metrics*`
  en la regla del ALB de `origination`.
- Para ver datos, el socio del usuario del portal (`custom:partner_id`, p. ej. `socio-a` en el pool permanente) debe ser el mismo
  `partner_id` con el que se generan las ofertas del canal embebido (`aliadoId` del contexto de lanzamiento).
