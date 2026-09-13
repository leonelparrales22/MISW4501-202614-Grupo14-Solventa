# EC-D01 — Circuit breaker y caché de perfil de riesgo

POC mínimo para medir el umbral del circuit breaker y el TTL de Redis frente a una caída o latencia mayor de 700 ms del proveedor simulado.

## Componentes

- `originacion`: endpoint `POST /offers`; aplica timeout, circuit breaker y fallback.
- `redis`: conserva perfiles durante el TTL configurado.
- `openfinance`: stub del proveedor externo.
- `toxiproxy`: inyecta falla o latencia entre originación y el stub.
- `k6`: carga opcional.

La respuesta incluye `provisional`, `source` (`open_finance`, `cache` o `actuarial_base`) y el estado del circuit breaker.

## Variables

| Variable | Inicial | Propósito |
|---|---:|---|
| `CB_FAILURE_THRESHOLD` | `3` | Fallos consecutivos antes de abrir el circuito. |
| `CB_RECOVERY_WINDOW` | `5s` | Espera antes de una petición half-open. |
| `CB_HALF_OPEN_SUCCESS_THRESHOLD` | `3` | Éxitos secuenciales requeridos antes de cerrar desde half-open. |
| `RISK_PROFILE_CACHE_TTL` | `24h` | Vigencia del perfil en Redis. |
| `EXTERNAL_TIMEOUT` | `700ms` | Límite duro hacia Open Finance. |

## Ejecución

```bash
docker compose up --build -d
curl -X POST http://localhost:8080/offers -H 'Content-Type: application/json' -d '{"customer_id":"demo"}'
```

La primera solicitud llena Redis. Para simular una caída:

```bash
curl -X POST http://localhost:8474/proxies/openfinance -d '{"enabled":false}'
```

La respuesta debe mantener HTTP 200, marcar `provisional: true` y usar `cache` (o `actuarial_base` sin perfil válido). Reactiva el proxy con `enabled:true`. Ejecuta carga normal con `docker compose --profile load run --rm k6`; durante la caída usa `EXPECT_PROVISIONAL=true`.

## AWS mínimo

Ejecuta originación, Redis, Toxiproxy y el stub en una tarea ECS Fargate. Redis y Toxiproxy son contenedores auxiliares; originación expone 8080. Ejecuta k6 localmente o como una tarea Fargate efímera. El POC no requiere API Gateway, ALB ni ElastiCache.
