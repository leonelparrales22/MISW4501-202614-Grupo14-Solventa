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
| `normal` | latencia 85 ± 55 ms en las tres fuentes (≈ 18 % de las llamadas sobre 120 ms; EXP-L01 midió 17 %) | 100 % HTTP 200; parte de los perfiles parciales; con `OPEN_FINANCE_REQUIRED=true`, ≈ 18 % provisionales (Open Finance no llegó) |
| `degradado` | latencia 150 ± 120 ms (≈ 62 % sobre 120 ms; EXP-L01 midió 61 %) | 100 % HTTP 200; más parciales y más provisionales |
| `caida` | Open Data caída; las otras con el perfil `normal` | 100 % HTTP 200; Open Data como `error` y luego `circuit_open` (el circuito la aísla sin esperar) |

Al final de cada fase k6 imprime: solicitudes, fallas, `provisional`, `partial`, porcentaje de llamadas faltantes por fuente
y latencias (indicativas). Los umbrales locales son `http_req_failed == 0` y `checks == 1`: si una solicitud se pierde o una
respuesta no cumple el contrato, el script termina con error.

Variables del script: `SMOKE_RAMP_UP` / `SMOKE_SUSTAIN` (por defecto `10s` / `20s` por fase; la rampa completa de EXP-L01 es
`1m` / `3m`) y `MODES` (por defecto `"true false"`).

## Resultado de referencia (humo local, 2026-10-10)

Rampa corta de `run.sh` (10 s + 20 s por fase, ≈ 2 100 solicitudes por fase). 0 % de fallas y 100 % de checks en todas las fases;
Schemathesis: 3 operaciones, 101/101 en cada check.

| `OPEN_FINANCE_REQUIRED` | Fase | Provisionales | Parciales |
|---|---|---|---|
| `true` | normal | 19,2 % | 27,1 % |
| `true` | degradado | 63,2 % | 32,1 % |
| `true` | caída de Open Data | 18,6 % | 81,4 % |
| `false` | normal | 0,9 % | 43,9 % |
| `false` | degradado | 25,8 % | 69,3 % |
| `false` | caída de Open Data | 3,3 % | 96,7 % |

La latencia local (p95 ≈ 122 ms) solo confirma que el abanico no espera más que el presupuesto; no es la medición de CA-1.

## Qué significa `OPEN_FINANCE_REQUIRED` (D-2)

- `true` (defecto): si Open Finance no llega dentro de su presupuesto (lenta, con error o con el circuito abierto), se cancelan
  las demás consultas y la oferta sale **provisional** desde la caché o el perfil base (cadena de HA-D01). Se privilegia la
  exactitud del precio. Con el perfil `normal` se espera cerca de un 17–18 % de cotizaciones provisionales.
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

## Trazabilidad

HA-L01 (CA-2, CA-3; CA-1 como humo) · EXP-L01 (rama `ExperimentoL01_perfilamientoParalelo`: rampa 8 → 83 req/s y perfiles de
latencia) · Estrategia de pruebas: carga con k6, inyección de fallas con Toxiproxy y contrato con Schemathesis.
