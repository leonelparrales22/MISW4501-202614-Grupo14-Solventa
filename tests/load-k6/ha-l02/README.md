# HA-L02 — Consulta ágil del estado del siniestro (escenario local)

Comprueba, con el servicio `claims` real, que la consulta del asegurado (lista, detalle y línea de tiempo, el recorrido móvil
de EC-L02) responde en p95 ≤ 2 s. También comprueba qué pasa cuando la base se pone lenta o se cae: cada lectura deja de
esperar a los 900 ms y responde la última versión guardada en la caché de lectura, marcada `stale: true` y con la hora en que
se leyó. Contrato: [`docs/contratos/HA-L02-api.md`](../../../docs/contratos/HA-L02-api.md).

> **Local = humo.** Las latencias de un portátil con Docker son indicativas. Lo que sí se comprueba aquí es el mecanismo:
> el corte a los 900 ms, la respuesta desde la caché, el aislamiento entre asegurados y la vuelta a la base.

## Qué levanta

`docker-compose.yml` (proyecto `ha-l02`):

- `claims` con su PostgreSQL detrás de Toxiproxy;
- el Redis de la caché de lectura;
- Moto como Cognito.

`claims` queda publicado en `127.0.0.1:8302` y la API de Toxiproxy en `127.0.0.1:8502`. `k6` arranca bajo demanda (perfil
`tools`).

## Cómo correrlo

```bash
cd tests/load-k6/ha-l02
./run.sh
```

Requisitos: Docker con `docker compose`, `curl`, `jq` y la AWS CLI. La AWS CLI se usa solo para sembrar el Moto local, igual
que `tests/security/ha-s01/local`. Las contraseñas (PostgreSQL y usuarios de Moto) se generan al azar en cada corrida y no se
imprimen. El script siembra un siniestro del asegurado `en evaluación`, corre las fases y destruye el ambiente al terminar,
aunque falle.

| Fase | Falla inyectada (Toxiproxy) | Qué se espera |
|---|---|---|
| 1. `normal` | ninguna | 100 % HTTP 200, `stale=false`, `X-Data-Source: database`; p95 ≤ 2 s [CA-1] |
| 2. `degradado` | 1500 ms de latencia hacia la base | 100 % HTTP 200, `stale=true`, `as_of` de la última lectura; cada lectura corta a los 900 ms [CA-2, CA-3] |
| 3. `caida` | la base desconectada | igual que la anterior: la caché sigue respondiendo [CA-2] |
| 4. `aislamiento` | la base desconectada | otro asegurado nunca recibe el siniestro, ni su historial, ni lo ve en su lista [CA-4] |
| 5. `recuperada` | ninguna | todo vuelve a `stale=false` |

Cada fase de k6 es un recorrido por segundo a tasa constante: la lista, el detalle y el historial, igual que la app al abrir un
siniestro. Por defecto son 20 recorridos por segundo durante 30 s, y se cambian con `RATE` y `DURATION`. Umbrales:
`http_req_failed == 0`, `checks == 1` y p95 de cada lectura < 2 s. Si una respuesta no cumple, el script termina con error.

La frescura de 5 minutos (CA-4) no se mide aquí, porque esperar 5 minutos no aporta nada. La cubren las pruebas de
`services/claims/internal/adapters/redis`, que adelantan el reloj de la caché.

## Resultado de referencia (humo local, 2026-11-08)

Configuración: 20 recorridos/s × 30 s por fase (≈ 1 800 lecturas por fase), con `CLAIM_READ_BUDGET_MS=900` y
`CLAIM_READ_CACHE_TTL_SECONDS=300`.

| Fase | Lecturas | Fallas | Checks | p95 lista / detalle / historial | Origen |
|---|---|---|---|---|---|
| normal | 1 803 | 0 % | 100 % | 3,6 / 4,8 / 6,3 ms | base |
| degradado (1500 ms) | 1 758 | 0 % | 100 % | 902,7 / 903,2 / 903,2 ms | caché, `stale=true` |
| caída de la base | 1 803 | 0 % | 100 % | 3,9 / 3,4 / 3,4 ms | caché, `stale=true` |
| aislamiento | 4 consultas | — | 4/4 | — | nada del otro asegurado |
| recuperada | 1 800 | 0 % | 100 % | 3,5 / 4,7 / 6,7 ms | base |

En total, `claims` registró 3 561 lecturas servidas desde la caché.

- **Degradado:** las lecturas cortan en 902 ms (900 ms de presupuesto más la lectura de Redis). Ninguna esperó los 1 500 ms
  de la base. Con 20 recorridos por segundo de 2,7 s cada uno, k6 dejó caer 15 iteraciones por falta de usuarios virtuales;
  ninguna solicitud falló.
- **Caída:** con la base desconectada, pgx falla al instante, así que la caché responde sin esperar el presupuesto.
- **Aislamiento:** con la base caída, el otro asegurado recibe `503 claims_unavailable` para el siniestro ajeno, porque su
  clave en la caché no existe. No ve el radicado ni el historial, y su lista no lo incluye.

El experimento EC-L02 midió p95 de 1.121 ms con el mismo corte de 900 ms; aquí la cola es más corta porque todo corre en la
misma máquina. La medición que cuenta para el criterio 1 es la de staging, a través del API Gateway.
