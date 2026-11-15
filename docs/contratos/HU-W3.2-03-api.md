# Contrato de API — HU-W3.2-03 · Desempeño técnico de la API del socio

> **Fuente para el front (Angular).** Describe lo que implementa el servicio `partners`
> (`internal/adapters/http/performance.go`, `internal/dashboard/service.go`, `internal/adapters/cloudwatch/telemetry.go`,
> `internal/domain/performance.go`) y las alarmas del SLO del módulo `infra/terraform/modules/apigateway` (`slo.tf`). Contrato formal:
> `services/partners/api/openapi.yaml` (0.3.0). Alimenta la pantalla **B3 «Desempeño técnico de la API»** del portal del socio
> (`/socios/tablero/desempeno`).

## 1. De dónde salen los datos (CA-5)

El SLO de la integración del socio es el de la plataforma, como está en la wiki:

| Objetivo | Valor | Origen |
|---|---|---|
| Latencia | **p95 ≤ 400 ms** en el camino de la cotización | Escenario de calidad de HA-L01 (*Arquitectura final*) |
| Disponibilidad | **≥ 99,9 %** mensual | RC-02 (*Objetivos, Restricciones y Requisitos de Calidad*) |
| Fuente | **CloudWatch** | Decisión de observabilidad (*Recopilación de Decisiones de Arquitectura*) |

Las rutas de socio son las cinco que el socio llama con su API key:
- `POST /v1/risk-profiles/resolve`;
- `GET /v1/risk-profiles/{customer_id}`;
- `POST /v1/offers`;
- `GET /v1/audit/consent-terms/current`;
- `POST /v1/audit/consents`.

Cada una tiene **dos alarmas de CloudWatch** sobre sus métricas del API Gateway:

| Alarma | Condición | Estado que produce |
|---|---|---|
| `<nombre>-slo-latency-<ruta>` | `Latency` p95 > 400 ms en **dos periodos seguidos de 5 minutos** | **degradado**: un incidente que afecta el SLO |
| `<nombre>-slo-errors-<ruta>` | `5XXError` (proporción de respuestas 5XX) ≥ 5 % en **tres periodos seguidos de 1 minuto** | **no disponible**: un incidente que impide atender la API |

- Un periodo sin peticiones nunca dispara una alarma (`treat_missing_data = notBreaching`).
- `partners` lee las alarmas por su prefijo (`SLO_ALARM_PREFIX`). Las **dimensiones** de cada alarma le dicen qué ruta y qué métricas leer, así que el tablero mide con las mismas métricas y umbrales del SLO.
- El presupuesto de latencia que se muestra (`budget_ms`) es el umbral de las alarmas de latencia.

Se mide **toda la API de socios**, no cada socio: todos los socios comparten la API, y el SLO es de la API (decisión del equipo).

## 2. Endpoint

### `GET /v1/partner-performance?period=today|7d|30d` → `200`

ID token de Cognito con rol **`partner`**: sin token `401`; con otro rol (`insured`, `advisor`, `operations`), `403`. Lleva
`Cache-Control: no-store`. Publicado en AWS como ruta humana `partner-performance` del API Gateway (autorizador de Cognito); el ALB
la lleva a `partners`.

| Parámetro | Valor | Por defecto |
|---|---|---|
| `period` | `today` (desde las 00:00 de Colombia), `7d` (hoy y los 6 días anteriores) o `30d` | `7d` |

```json
{
  "period": "7d",
  "from": "2026-09-10T05:00:00Z",
  "to": "2026-09-17T03:30:00Z",
  "status": "degraded",
  "availability": { "percent": 99.96, "objective_percent": 99.9, "requests": 10000, "server_errors": 4 },
  "latency": { "p95_ms": 1150, "budget_ms": 400 },
  "incidents": [
    {
      "id": "solventa-stg-slo-latency-post-offers-20260916T212000Z",
      "kind": "degraded",
      "route": "POST /v1/offers",
      "started_at": "2026-09-16T21:20:00Z",
      "ended_at": null,
      "duration_seconds": 2520,
      "resolved": false
    },
    {
      "id": "solventa-stg-slo-errors-post-offers-20260908T081200Z",
      "kind": "unavailable",
      "route": "POST /v1/offers",
      "started_at": "2026-09-08T08:12:00Z",
      "ended_at": "2026-09-08T08:26:00Z",
      "duration_seconds": 840,
      "resolved": true
    }
  ]
}
```

| Campo | Regla |
|---|---|
| `status` (CA-1, CA-2) | `unavailable` si alguna alarma de errores está activa; si no, `degraded` si alguna de latencia lo está; si no, `operational` |
| `availability.percent` (CA-1) | Peticiones de las rutas de socio respondidas sin 5XX en el periodo, truncado a dos decimales (un error nunca se ve como 100 %). **Sin peticiones, 100.** Un 4XX es la petición del socio, no una falla de la API |
| `latency.p95_ms` (CA-1) | p95 de la ruta de socio más lenta con peticiones en el periodo, el mismo estadístico que vigila su alarma. `null` sin peticiones |
| `incidents` (CA-3, CA-4) | Cada vez que una alarma estuvo activa, si se cruza con el periodo; el más reciente primero. Uno abierto tiene `ended_at: null`, `resolved: false` y dura hasta ahora. **Lista vacía = la integración operó sin interrupciones** |

### Errores

| Estado | `error` | Cuándo |
|---|---|---|
| 400 | `invalid_request` | `period` no es `today`, `7d` ni `30d` |
| 401 | `unauthorized` | Sin token o con token inválido |
| 403 | `forbidden` | Rol distinto de `partner` |
| 503 | `telemetry_unavailable` | CloudWatch no respondió (8 s como máximo) o no hay alarmas del SLO; reintentar |

## 3. Despliegue

- `modules/apigateway`: variable `slo_alarms` (apagada por defecto, para no afectar a los experimentos) y salida `slo_alarm_prefix`.
- `modules/platform`:
  - la enciende;
  - pasa `SLO_ALARM_PREFIX` a `partners`;
  - le da permisos de solo lectura: `cloudwatch:DescribeAlarms` y `cloudwatch:GetMetricData` sobre `*` (no admiten recurso) y `cloudwatch:DescribeAlarmHistory` solo sobre las alarmas del SLO;
  - publica la ruta humana `partner-performance` y la regla `/v1/partner-performance*` del ALB hacia `partners`.
- `partners` monta la ruta con `SLO_ALARM_PREFIX` y `AUTH_*`, aunque no tenga `DATABASE_URL`. `SLO_AVAILABILITY_OBJECTIVE_PERCENT` (opcional, por defecto 99,9) es el objetivo que se muestra.
- CloudWatch guarda el historial de una alarma 30 días: alcanza para el periodo más largo.
