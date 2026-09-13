# Resultados — EC-D02: Cobro idempotente con reintentos

Evidencia completa (raw JSON/logs) en `resultados/raw/`. Generado en parte
por `python3 scripts/analizar-resultados.py` (ejecutar para regenerar las
tablas con datos frescos).

## 1. Hallazgo clave: el diseño ingenuo SÍ duplicó un cobro real

Antes de aplicar ninguna corrección, se probó el diseño tal como lo
describe la hipótesis original del experimento: `idempotency_key` con
constraint `UNIQUE` en Postgres + reintentos con backoff, **sin ningún
paso de "reclamo" antes de llamar la pasarela**.

Al crear una aceptación con la pasarela en modo "respuesta ambigua" y
disparar un único `POST /procesar-cobro` manual, el conteo real de
llamadas a la pasarela (`GET /debug/cobros`) ya mostraba **2** llamadas
para la misma `idempotency_key` — **antes siquiera de correr el escenario
de concurrencia deliberado**. La causa: el worker de reintentos en
background queda elegible para procesar cualquier intento "pendiente"
desde el instante en que se crea, así que compite de forma real (no
sintética) con cualquier disparo síncrono del journey. Transcript completo
en [`resultados/raw/diseno-ingenuo-evidencia-duplicado-real.txt`](raw/diseno-ingenuo-evidencia-duplicado-real.txt).

Esto confirma directamente la fila 3 de la tabla de interpretación del
diseño del experimento: *"Dos workers concurrentes generan dos cobros
para la misma aceptación → el constraint de unicidad no bloquea
correctamente la carrera: se revisa el nivel de aislamiento de la
transacción o se agrega un lock explícito"*.

## 2. Corrección aplicada (ADR-01): claim atómico

Se agregó `Store.ReclamarPendiente`: un `UPDATE` condicional de una sola
sentencia (`WHERE estado = 'pendiente'`) que transiciona el intento a
`en_proceso` antes de llamar la pasarela. Postgres serializa esa escritura
a nivel de fila, así que solo un llamador concurrente puede ganarla; el
resto se retira sin llamar la pasarela. Detalle completo y alternativas
consideradas en la sección ADR de la hoja de wiki.

## 3. Resultado tras la corrección

### 3.1 Escenarios cualitativos (estado final en la base de datos propia)

| Escenario | Estado final | Intentos | Último error |
|---|---|---|---|
| Normal (baseline) | confirmado | 0 | — |
| Caída total | confirmado | 2 | connection refused (recuperado automáticamente por el worker) |
| Respuesta ambigua | confirmado | 2 | context deadline exceeded (recuperado con la misma idempotency_key) |
| Ventana de reintentos vencida (8s de prueba) | fallido | 6 | ventana vencida, sin quedar atascado en "pendiente" |

Ninguna aceptación se perdió en ningún escenario: incluso la que agotó la
ventana quedó en un estado terminal explícito (`fallido`), no en un limbo.

### 3.2 Duplicidad real del lado del proveedor (escenario ambiguo)

| idempotency_key | Requests recibidos (red) | Cargos reales procesados | ¿Duplicado real? |
|---|---|---|---|
| cobro-d2a83e3f-... | 3 | **1** | no |

Que "requests recibidos" sea > 1 es normal y esperado (reintentos legítimos
tras una respuesta ambigua); lo que importaba como hallazgo es que
"cargos reales" se mantuviera en 1, y así fue — **siempre que la pasarela
real soporte idempotency keys** (ver contrafáctico en 3.4).

### 3.3 Concurrencia dirigida (10 repeticiones, 20 goroutines simultáneas cada una)

| Repetición | Filas confirmadas en BD | Requests al gateway | Cargos reales | ¿Duplicado? |
|---|---|---|---|---|
| 1–10 | 1 | 1 | 1 | no (las 10) |

**0 de 10 repeticiones mostraron duplicidad real.** Con el claim atómico,
de las 20 goroutines que compiten por la misma aceptación, exactamente una
gana el reclamo y las 19 restantes se retiran sin siquiera llamar la
pasarela (`requests_al_gateway = 1`, no solo `cargos_reales = 1`) — la
corrección es más fuerte que "evitar cobrar dos veces": evita incluso
intentarlo dos veces.

### 3.4 Contrafáctico: ¿qué pasaría sin soporte de idempotencia en la pasarela?

La corrección de ADR-01 resuelve la concurrencia **dentro de nuestro
sistema**. Pero el caso ambiguo (fila 2 de la tabla de interpretación)
depende de una premisa externa: que la pasarela real deduplique por
`idempotency_key`. Para no dar esa premisa por sentada, se corrió el
mismo stub con `SOPORTA_IDEMPOTENCIA=false` (ver
[`resultados/raw/contrafactico_pasarela_sin_idempotencia.json`](raw/contrafactico_pasarela_sin_idempotencia.json)):
2 requests con la misma clave produjeron **2 cargos reales** — la
duplicidad que la fila 2 anticipa si esa dependencia externa falla.

## 4. Conclusión

- **Hipótesis original (sin corrección): REFUTADA.** El diseño ingenuo
  permitió una llamada real duplicada a la pasarela bajo una condición de
  carrera genuina, no solo bajo un escenario sintético.
- **Hipótesis corregida (con el claim atómico de ADR-01): CONFIRMADA**
  para la concurrencia dentro del sistema (0/10 duplicados reales,
  incluso a nivel de request, no solo de cargo).
- **Dependencia externa documentada, no asumida ciegamente:** el caso
  ambiguo solo queda resuelto si la pasarela real soporta idempotency
  keys — se debe verificar contractualmente con el proveedor real (Open
  Finance/pagos) antes de construcción (Etapa 2), y considerarse un
  riesgo de arquitectura si no se puede confirmar.
