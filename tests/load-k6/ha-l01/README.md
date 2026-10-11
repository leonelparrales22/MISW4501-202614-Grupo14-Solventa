# HA-L01 — Cotización embebida rápida bajo tráfico pico (escenario local)

Comprueba, con el servicio de originación real y las tres fuentes externas simuladas, que el perfilamiento consulta Open
Finance, Open Data y KYC **en paralelo**, que cada fuente respeta su presupuesto de 120 ms (corte total de 700 ms), que la
fuente lenta se cancela y que el perfil sale **parcial** con las señales que llegaron, sin perder ninguna solicitud.

> **Local = humo.** Las latencias que se miden aquí (portátil + Docker) son solo indicativas. El p95 ≤ 400 ms y el
> p99 ≤ 800 ms de CA-1, la caché del gateway (CA-4) y el rate limiting (CA-5) se miden en staging, a través del API Gateway
> (guiones `k6/gateway-cache.js` y `k6/rate-limit.js`, y `k6/peak.js` con `STRICT_LATENCY=1`).

## Qué levanta

`docker-compose.yml` (proyecto `ha-l01`): Redis, los stubs `openfinance`, `opendata` y `kyc`, Toxiproxy con un proxy delante
de cada stub y el servicio `origination` publicado en `http://localhost:8080` (la URL que usa el cliente web en desarrollo;
se cambia con `ORIGINATION_PORT`). La API de Toxiproxy queda en `localhost:8475` para no chocar con el escenario de HA-D01.
`k6` y `schemathesis` arrancan bajo demanda (perfil `tools`).

## Cómo correrlo

```bash
cd tests/load-k6/ha-l01
./run.sh
```

Requiere Docker con `docker compose`. El script construye las imágenes, corre las fases con `OPEN_FINANCE_REQUIRED=true` y
luego con `false` (decisión D-2), ejecuta la prueba de contrato y destruye el ambiente (`down -v`) al terminar, aunque falle.

| Fase | Falla inyectada (Toxiproxy) | Qué se espera |
|---|---|---|
| `normal` | latencia 85 ± 55 ms en las tres fuentes (≈ 18 % de las llamadas sobre 120 ms; EXP-L01 midió 17 %) | 100 % HTTP 200; parte de los perfiles parciales; con `OPEN_FINANCE_REQUIRED=true`, ≈ 18 % provisionales (Open Finance no llegó); **ningún circuito se abre** |
| `degradado` | latencia 150 ± 120 ms (≈ 62 % sobre 120 ms; EXP-L01 midió 61 %) | 100 % HTTP 200; las fuentes siguen vivas y por debajo del 80 % de fallas, así que **ningún circuito debería abrirse** (ver la salvedad abajo); ≈ 62 % provisionales con `true` y ≈ 25 % con `false` |
| `caida` | Open Data caída; las otras con el perfil `normal` | 100 % HTTP 200; Open Data como `error` y luego `circuit_open` (el circuito la aísla sin esperar); Open Finance y KYC sin transiciones |

Al final de cada fase k6 imprime: solicitudes, fallas, `provisional`, `partial`, porcentaje de llamadas faltantes por fuente
y latencias (indicativas), y el script agrega las transiciones del circuit breaker que registró originación durante la fase.
Al recrear originación imprime también la configuración del circuito que quedó activa. Los umbrales locales son
`http_req_failed == 0` y `checks == 1`: si una solicitud se pierde o una respuesta no cumple el contrato, el script termina con
error (las transiciones se informan, no hacen fallar el script).

El circuit breaker de cada fuente cuenta el corte por presupuesto como falla y abre por proporción (al menos el 80 % de las
llamadas fallidas con un mínimo de 50 llamadas en una ventana de 5 s) **o tras 30 fallas seguidas**, contadas por el propio
circuito sin reiniciarse al terminar cada ventana: así una fuente colgada queda aislada aunque haya poco tráfico. La cola normal
de latencia y una fuente degradada pero viva no lo abren; una fuente caída o colgada de forma persistente queda aislada. Ver el
README de `services/origination`.

Variables del script: `SMOKE_RAMP_UP` / `SMOKE_SUSTAIN` (por defecto `10s` / `20s` por fase; la rampa completa de EXP-L01 es
`1m` / `3m`), `MODES` (por defecto `"true false"`) y los parámetros del circuito, que el compose toma del entorno con los
valores por defecto del servicio: `BREAKER_FAILURE_RATIO` (0.8), `BREAKER_MIN_REQUESTS` (50), `BREAKER_WINDOW_MS` (5000),
`BREAKER_CONSECUTIVE_FAILURES` (30), `BREAKER_OPEN_TIMEOUT_MS` (5000) y `BREAKER_HALF_OPEN_SUCCESSES` (3). Por ejemplo:
`BREAKER_FAILURE_RATIO=0.5 ./run.sh`.

## Resultado de referencia (humo local, 2026-10-11: umbral 0,8, mínimo de 50 llamadas, 30 fallas seguidas)

Rampa corta de `run.sh` (10 s + 20 s por fase, ≈ 2 100 solicitudes por fase), tres corridas. 0 % de fallas y 100 % de checks en
todas las fases; Schemathesis: 3 operaciones, 101/101 en cada check.

| `OPEN_FINANCE_REQUIRED` | Fase | Provisionales | Parciales | Transiciones del circuito |
|---|---|---|---|---|
| `true` | normal | 18,4 % / 19,1 % / 18,4 % | 29,1 % / 28,2 % / 25,9 % | ninguna |
| `true` | degradado | 63,8 % / 63,5 % / 63,7 % | 32,2 % / 31,5 % / 31,1 % | ninguna |
| `true` | caída de Open Data | 19,3 % / 20,4 % / 19,8 % | 80,7 % / 79,6 % / 80,2 % | solo Open Data (abre y las sondas reabren cada 5 s) |
| `false` | normal | 0,5 % / 0,7 % / 0,3 % | 44,6 % / 44,4 % / 44,5 % | ninguna |
| `false` | degradado | 23,0 % / 24,8 % / 24,7 % | 71,5 % / 70,7 % / 70,7 % | ninguna |
| `false` | caída de Open Data | 2,7 % / 2,5 % / 3,5 % | 97,3 % / 97,5 % / 96,5 % | solo Open Data |

Además se repitió 3 veces por modo solo la fase `degradado` (61,2–64,5 % provisionales con `true` y 24,1–24,9 % con `false`): en
total, **ninguna transición en 12 fases `degradado` ni en 6 fases `normal`**.

**Por qué 50 llamadas (fuente degradada pero viva):** con un mínimo de 20, la proporción se evaluaba con tan pocas llamadas que una
fuente con ≈ 62 % de cortes llegaba a veces al 80 % por azar, y una vez abierto el circuito casi nunca cerraba (el half-open
necesita 3 éxitos seguidos, ≈ 5 % con esa fuente). En el humo del 2026-10-10 pasó una vez en 10 fases `degradado` (KYC, con
`false`: 39,1 % de provisionales en lugar de ≈ 25 %). Con 50 la red de 30 fallas seguidas sigue aislando una fuente colgada con
poco tráfico: con Open Data colgada y ≈ 2 cotizaciones por segundo (≈ 9 llamadas por ventana), las 30 primeras la esperan hasta el
presupuesto y desde la 31.ª sale `circuit_open` sin llamarla. `BREAKER_MIN_REQUESTS` sigue siendo configurable.

Con el umbral anterior (50 %) el perfil `degradado` abría los tres circuitos y daba ≈ 96–99 % de provisionales con cualquier
valor de `OPEN_FINANCE_REQUIRED`; con 0,8 vuelve a los valores de EXP-L01 (≈ 62 % con `true`, ≈ 25 % con `false`).

La latencia local (p95 ≈ 122 ms) solo confirma que el abanico no espera más que el presupuesto; no es la medición de CA-1.

## Qué significa `OPEN_FINANCE_REQUIRED` (D-2)

- `true` (defecto): si Open Finance no llega dentro de su presupuesto (lenta, con error o con el circuito abierto), la oferta sale
  **provisional** desde la caché o el perfil base (cadena de HA-D01). Open Data y KYC no se cancelan antes de tiempo: cada una
  termina dentro de su presupuesto y su circuito registra solo su resultado real. Se privilegia la exactitud del precio. Con el
  perfil `normal` se espera cerca de un 17–18 % de cotizaciones provisionales.
- `false`: las fuentes son equivalentes; basta con una señal para calcular el perfil (parcial). La oferta solo sale provisional
  si no llega ninguna fuente.

## Control manual

```bash
docker compose -p ha-l01 up -d --build --wait origination
./toxiproxy.sh normal            # perfil de latencia normal
./toxiproxy.sh degradado         # perfil degradado
./toxiproxy.sh caida kyc         # una fuente caída (openfinance | opendata | kyc)
./toxiproxy.sh up                # restablecer
curl -s -X POST localhost:8080/v1/risk-profiles/resolve -H 'Content-Type: application/json' \
  -d '{"customer_id":"c1","consent_active":true}'
curl -s 'localhost:8080/v1/risk-profiles/c1?consent_active=true'
docker compose -p ha-l01 --profile tools down -v
```

## Guiones para staging (a través del API Gateway)

| Guion | Criterio | Qué hace | Umbrales |
|---|---|---|---|
| `k6/ready.js` | compuerta | 30 cotizaciones seguidas con la clave del socio A antes de cualquier carga: comprueba el recorrido gateway → VPC Link → ALB → originación → Toxiproxy → stubs | 100 % de respuestas `200` con el arreglo `sources`; ≥ 50 % con alguna fuente `ok` (≈ 99 % con el perfil `normal`, ≈ 76 % con `degradado`, 0 % si Toxiproxy no creó los proxies) |
| `k6/peak.js` con `STRICT_LATENCY=1` y `API_KEY` | CA-1 | Rampa de EXP-L01 contra el stage, tres corridas | `p(95)<400`, `p(99)<800`, `http_req_failed<1%` |
| `k6/gateway-cache.js` | CA-4 | Por cliente: el socio A lee dos veces y el socio B una (`GET /v1/risk-profiles/{id}`) | la segunda lectura de A es idéntica a la primera (> 95 %); la de B nunca coincide con la de A (100 %) |
| `k6/rate-limit.js` | CA-5 | Socio A a 200 req/s (sobre su plan de 100) y socio B a 50 req/s al mismo tiempo | A recibe `429`; B 0 % de `429` y p95 < 400 ms |

Variables: `BASE_URL` (URL del stage), `API_KEY` / `API_KEY_A` / `API_KEY_B` (claves de prueba de los socios). La imagen de la
tarea k6 de la ventana se construye con el `Dockerfile` de esta carpeta (`docker build -t <repo-ecr-k6>:<image_tag> .`) y ejecuta
`k6/sequence.sh`. La infraestructura está en `infra/terraform/environments/staging/ha-l01`.

**Secuencia de la tarea k6 (`k6/sequence.sh`):**

1. **Compuerta:** hasta `READY_ATTEMPTS` intentos (10) de `ready.js`, separados `READY_INTERVAL_S` segundos (30). Si ninguno pasa,
   imprime `== HA-L01 NOT READY: no load sent` y queda en espera **sin enviar carga** (el recorrido está roto: se corrige antes de
   medir y no se gastan peticiones). Las dos variables solo existen para acortar el ensayo local; la tarea de AWS usa los defectos.
2. **Pasos de `K6_SEQUENCE`** (por defecto `peak cache ratelimit`), en ese orden: `peak` corre `peak.js` `PEAK_RUNS` veces (por
   defecto 3, de 1 a 3), `cache` corre `gateway-cache.js` y `ratelimit` corre `rate-limit.js`. Así se puede repetir un paso suelto
   (p. ej. `K6_SEQUENCE=cache`) sin repetir toda la secuencia. Un valor fuera de esos conjuntos no envía carga.
3. Cada corrida imprime su inicio y su fin en UTC y su código de salida (`== HA-L01 <paso> run <n> start <ISO>` /
   `== HA-L01 <paso> run <n> end <ISO> exit <código>`; 0 = todos los umbrales cumplidos), para recortar las métricas de
   CloudWatch por corrida. El resumen de `peak.js` lleva `PROFILE` (perfil de Toxiproxy y número de corrida) y
   `OPEN_FINANCE_REQUIRED`, que la tarea recibe de la composición.
4. Al final imprime `== HA-L01 staging sequence finished (exit status <0|1>; …)` y queda en espera: el servicio ECS no repite la
   secuencia; la ventana se destruye con `terraform destroy`.

Ensayo local de la imagen (sin gateway: la compuerta y `peak` pasan; `cache` y `ratelimit` fallan como se espera porque no hay
caché ni límite):

```bash
docker compose -p ha-l01 up -d --wait origination && ./toxiproxy.sh normal
docker build -t solventa-ha-l01-k6:local .
docker run -d --name k6-dry --network ha-l01_default -e BASE_URL=http://origination:8080 -e API_KEY_A=x -e API_KEY_B=y \
  -e RAMP_UP=5s -e SUSTAIN=10s -e CUSTOMERS=20 -e DURATION=10s -e PEAK_RUNS=1 solventa-ha-l01-k6:local
docker logs -f k6-dry       # hasta "staging sequence finished"; luego docker rm -f k6-dry
docker compose -p ha-l01 --profile tools down -v
```

## Trazabilidad

HA-L01 (CA-2, CA-3; CA-1 como humo) · EXP-L01 (rama `ExperimentoL01_perfilamientoParalelo`: rampa 8 → 83 req/s y perfiles de
latencia) · Estrategia de pruebas: carga con k6, inyección de fallas con Toxiproxy y contrato con Schemathesis.
