# Resultados iniciales — EC-D01

Fecha de ejecución: 12 de septiembre de 2026. Ambiente local con Docker Compose, Redis en contenedor y Open Finance deshabilitado mediante Toxiproxy.

Cada corrida usó 10 VUs durante 10 segundos. Antes de la falla se precalentaron cinco perfiles. Para TTL `1s` se esperó su vencimiento antes de iniciar la carga; para `24h` la respuesta degradada usó Redis. En ambas variantes la respuesta se marcó como provisional.

| Umbral CB | TTL | Solicitudes | p95 | Fallos HTTP | Checks |
|---:|---:|---:|---:|---:|---:|
| 2 | 1s | 36.814 | 5,58 ms | 0 % | 100 % |
| 2 | 24h | 36.840 | 5,60 ms | 0 % | 100 % |
| 3 | 1s | 36.095 | 5,81 ms | 0 % | 100 % |
| 3 | 24h | 37.414 | 5,71 ms | 0 % | 100 % |
| 5 | 1s | 35.679 | 6,08 ms | 0 % | 100 % |
| 5 | 24h | 35.866 | 5,92 ms | 0 % | 100 % |

## Interpretación

Las seis combinaciones sostienen el objetivo de disponibilidad del POC: 100 % de respuestas normales o degradadas, sin superar el presupuesto de 700 ms. En la prueba dirigida previa, el circuito abrió después del número configurado de fallos y el fallback de Redis devolvió una oferta provisional.

Estos resultados no justifican aún elegir un TTL: ambos TTL cumplen latencia, pero el trade-off de frescura requiere una política de negocio. Tampoco cubren latencia intermitente o recuperación bajo carga; ambas deben probarse antes de fijar el ADR.

## Segunda fase — latencia, recuperación e intermitencia

Configuración evaluada: umbral `3`, TTL `24h`, ventana de recuperación `5s`, 10 VUs. Se mantuvieron cinco perfiles precalentados en Redis.

| Escenario | Resultado de disponibilidad | p95 | Máximo | Resultado de resiliencia |
|---|---:|---:|---:|---|
| Latencia fija de 800 ms | 100 % HTTP 200; respuesta provisional | 5,89 ms | 743,93 ms | El circuito abrió tras tres timeouts. |
| Recuperación tras retirar la latencia | 100 % HTTP 200; respuesta normal | 11,10 ms | 203,90 ms | Transición `open → half_open → closed`. |
| Latencia intermitente de 800 ms, 50 % de toxicidad | 100 % HTTP 200; respuestas normales o provisionales | 7,04 ms | 942,94 ms | Se observó aleteo. |

La intermitencia produjo secuencias `closed → open → closed` repetidas mientras seguían ocurriendo timeouts. Por tanto, **la hipótesis queda refutada en su condición de ausencia de aleteo**: el umbral simple no es suficiente bajo latencia intermitente.

Se recomienda reemplazar el cierre inmediato del estado `half_open` por un cierre condicionado a varios éxitos consecutivos y limitar a una sola petición de prueba en ese estado. También se observaron timeouts aislados hacia Redis bajo esta carga, por lo que la próxima iteración debe usar un cliente Redis con *pool* de conexiones y métricas separadas de caché.

## Tercera fase — prueba de la corrección half-open

Se implementó una sola petición de prueba en `half_open` y un requisito de tres éxitos consecutivos antes del cierre. La prueba con latencia intermitente de 800 ms al 50 % usó 10 VUs durante 20 segundos.

| Métrica | Resultado |
|---|---:|
| Respuestas HTTP exitosas | 100 % |
| p95 | 6,95 ms |
| Máximo | 721,25 ms |
| Solicitudes | 48.832 |

La corrección evitó cerrar con un único éxito, pero no eliminó el aleteo: se observaron tres éxitos half-open consecutivos y cierres seguidos por reaperturas durante la misma ventana de intermitencia. No debe aprobarse aún como ADR.

La siguiente iteración debe añadir histéresis temporal: espaciar las peticiones de prueba en `half_open`, exigir una ventana mínima de estabilidad antes de cerrar y descartar resultados de solicitudes iniciadas antes de la última transición del circuito. Esas medidas evitan que ráfagas de éxitos y fallos concurrentes invaliden la decisión de estado.
