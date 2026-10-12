# Servicio `origination` (originación)

Camino crítico del negocio: cotiza, perfila, emite y cobra. Es la fachada (*Facade*) del ciclo de vida de la póliza.

**Estado:** `GET /health`, `POST /v1/risk-profiles/resolve` (HA-D01: oferta continua ante caída de la fuente externa) y `GET /v1/risk-profiles/{customer_id}` con las fuentes externas consultadas en paralelo (HA-L01: cotización embebida rápida bajo tráfico pico). El resto de módulos sigue reservado.

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `orchestrator` | Orquestador de cotización: coordina perfilamiento y rating. Consulta primero la caché y luego las fuentes externas en paralelo |
| `profiling` | Perfil de riesgo con Open Finance / Open Data (con consentimiento). **HA-D01:** `Resolver` con la cadena de degradación Open Finance → `Perfil_Riesgo_Cache` (Redis, TTL 30 días) → perfil actuarial base; la oferta degradada sale marcada como provisional. **HU-W1.2-02:** el `Resolver` (con la opción `WithCachedProfileReuse`, activa por defecto en `main`) reutiliza un perfil **completo** de ≤ 30 días sin consultar el ecosistema, comparte una sola consulta entre solicitudes simultáneas del mismo cliente (`singleflight`) y expone `Invalidate` para la recotización. **HA-L01:** `ParallelSource` consulta Open Finance, Open Data y KYC en paralelo (120 ms por fuente, corte total de 700 ms), cancela la fuente lenta y arma el perfil parcial con las señales que llegaron; `CombineProvisional` es la regla de combinación por defecto; con `Weights` se usa `CombineWeighted` (HU-W1.2-02) |
| `rating` | Motor de rating: prima y coberturas. Reglas actuariales aisladas del resto |
| `underwriting` | Suscripción automática: acepta, rechaza o ajusta el riesgo; enruta a decisión humana (bandeja del asesor) cuando aplica |
| `issuance` | Aceptación de la oferta, documento contractual (firma) y activación de la cobertura |
| `billing` | Cobro de la prima y reembolsos contra la pasarela: `Intento_Cobro` con `idempotency_key` única por aceptación, reintentos con backoff (HA-D02) |
| `endorsements` | Modificaciones y endosos con trazabilidad |
| `renewal` / `cancellation` | Renovación y cancelación con prorrateo de reembolso |

Las llamadas entre orquestador, perfilamiento y rating son **en proceso** (sin salto de red).

## Adaptadores (`internal/adapters`)

| Adaptador | Qué hace |
|---|---|
| `http` | Router `chi`: `GET /health` (superficial), `POST /v1/risk-profiles/resolve` y `GET /v1/risk-profiles/{customer_id}?consent_active=…` (misma respuesta, lectura cacheable por el API Gateway por socio; HA-L01) |
| `openfinance` | Puente entre el cliente compartido `pkg/adapters/openfinance` (timeout 700 ms + circuit breaker) y el puerto `RiskProfileSource`; el circuito abierto se informa como `circuit_open` (HA-L01) |
| `opendata` | Puente entre `pkg/adapters/opendata` y el puerto `RiskProfileSource` (HA-L01) |
| `kyc` | Puente entre `pkg/adapters/kyc` y el puerto `RiskProfileSource` (HA-L01) |
| `redis` | `Perfil_Riesgo_Cache` (cache-aside), clave `risk_profile:{customer_id}`, TTL 30 días y timeout propio de 100 ms por operación para que Redis nunca bloquee la oferta |

## Perfil de riesgo: reutilización, cálculo y oferta continua (HU-W1.2-02, HA-D01)

```
sin consentimiento activo ───────────▶ 422 consent_required (no se consulta nada, ni la caché)
perfil COMPLETO en caché de ≤ 30 días ▶ mode=normal,      source=cache          (cero consultas externas)
Open Finance responde (< 700 ms) ────▶ mode=normal,      source=open_finance  (y calienta la caché)
Open Finance caído, lento o aislado
  └ perfil PARCIAL en caché de ≤ 30 días ▶ mode=provisional, source=cache         (con profile_date)
  └ sin perfil en caché (cliente nuevo) ▶ mode=provisional, source=base_actuarial
```

- El circuit breaker permanece abierto 5 s y se cierra tras 3 éxitos consecutivos en half-open; una sonda fallida lo reabre (combinación validada en EC-D01). Desde HA-L01 abre por **proporción de fallas** (≥ 80 % con al menos 50 llamadas en una ventana de 5 s) **o tras 30 fallas seguidas** (decisión D-3 c, ajustada tras la validación; ver «Cotización en paralelo»); con `BREAKER_FAILURE_RATIO=0` vuelve a abrir con 3 fallas consecutivas, como en EC-D01.
- **Reutilización (HU-W1.2-02, criterio 1):** solo se reutiliza un perfil completo (el que no se armó sin alguna fuente). Si la caché guarda uno parcial (`Partial` se persiste en Redis), se vuelve a consultar el ecosistema y el parcial solo respalda una respuesta provisional si las fuentes fallan. Como un perfil completo se reutiliza antes de consultar, el camino «caché provisional» de HA-D01 solo se alcanza con perfiles parciales. Las entradas escritas antes de este cambio no tienen la marca y se leen como completas. El perfil parcial sigue guardándose en caché, como en HA-L01.
- **Recotización:** `Resolver.Invalidate` descarta el perfil (`ports.RiskProfileCache.Invalidate`, `DEL` en Redis); el orquestador de HU-W1.2-03 lo invoca en proceso, por eso no hay endpoint.
- **Una consulta por cliente:** las solicitudes simultáneas del mismo cliente comparten un único abanico de consultas (`singleflight`); corre desacoplado del llamador, acotado por el corte de 700 ms, así que un cliente que se desconecta no hace fallar a los demás.
- Una falla de Redis nunca rompe la respuesta: si no se puede leer la caché se usa el perfil base; si no se puede escribir, la oferta sigue siendo normal.
- La respuesta incluye lo que pintan las pantallas del prototipo: `mode`, `source`, `profile_date` (E3b), `unavailable_source` y `notice` (`last_profile_in_use` para E3b (perfil parcial en caché), `personalization_unavailable` para E3c y el aviso de E2b). Los textos los traduce cada cliente.
- `consent_active` lo afirma el llamador mientras `audit` no expone la consulta de consentimiento (HU-W1.2-01); entonces se reemplazará por un puerto.
- Fuera de esta historia: respetar/recotizar ofertas provisionales y notificar (HU-W1.2-03), registro del linaje del perfil (HU-W4.1-01).

## Cotización en paralelo (HA-L01)

El `Resolver` de HA-D01 recibe como fuente un `ParallelSource` (*scatter-gather*) que consulta a la vez Open Finance, Open
Data y KYC. Cada fuente tiene su presupuesto (`SOURCE_BUDGET_MS`, 120 ms) dentro de un corte total (`PROFILING_DEADLINE_MS`,
700 ms); la fuente que no llega se **cancela** (la cancelación se propaga hasta la conexión HTTP) y el cliente no la espera.

```
llegan las tres fuentes ─────────────────────────────▶ mode=normal, source=external_sources, partial=false
falta una fuente opcional (Open Data o KYC) ─────────▶ mode=normal, source=external_sources, partial=true
falta Open Finance y OPEN_FINANCE_REQUIRED=true ─────▶ cadena de HA-D01 (cache / base_actuarial), provisional
falta Open Finance y OPEN_FINANCE_REQUIRED=false ────▶ perfil parcial con las que llegaron; provisional solo si no llega ninguna
```

- Cuando falla Open Finance con `OPEN_FINANCE_REQUIRED=true`, Open Data y KYC **no se cancelan antes de tiempo**: cada una termina
  dentro de su propio presupuesto, su circuit breaker registra solo su resultado real (una fuente sana nunca queda `circuit_open`
  porque falló otra) y `sources[]` muestra su estado verdadero. La respuesta provisional nunca se demora más que el presupuesto común.
- `sources[]` informa cada fuente con `status` (`ok`, `timeout`, `error`, `circuit_open`) y `latency_ms`; lo pinta la pantalla
  E2/E2b. En modo provisional muestra qué fuente falló. Con una sola fuente configurada (Open Finance, como en
  `tests/resilience/ha-d01`) la respuesta sigue siendo la de HA-D01 (`source=open_finance`).
- `OPEN_FINANCE_REQUIRED` (decisión D-2, por defecto `true`) privilegia la exactitud del precio: sin la señal financiera la
  oferta sale provisional. **Consecuencia esperada:** con el perfil de latencia `normal` de EXP-L01 (Open Finance supera
  120 ms cerca del 17 % de las veces) alrededor del 17 % de las cotizaciones salen provisionales. Con `false` las fuentes son
  equivalentes y casi ninguna sale provisional, pero más perfiles quedan parciales. En el humo local (2026-10-11, umbral 0,8 y
  mínimo de 50 llamadas, tres corridas): perfil `normal`, `true` → 18,4–19,1 % provisionales y 26–29 % parciales; `false` →
  0,3–0,7 % provisionales y ≈ 44,5 % parciales. Perfil `degradado` (≈ 62 % de llamadas sobre 120 ms, fuentes vivas): `true` →
  61–65 % provisionales; `false` → 23–25 %, sin circuitos abiertos en falso en 12 fases. Detalle en
  [`tests/load-k6/ha-l01`](../../tests/load-k6/ha-l01/README.md).
- La regla de combinación del servicio es la **provisional actuarial** de HU-W1.2-02 (`CombineWeighted`, versión `v1`): promedio
  ponderado de los `score` que llegaron, con pesos Open Finance 0,5, Open Data 0,3 y KYC 0,2 renormalizados sobre las fuentes
  presentes; la categoría sale del score combinado (< 0,2 `preferred`, > 0,8 `high_risk`, resto `standard`) y la fecha es la de
  la fuente de mayor prioridad presente. Los pesos son provisionales hasta que actuaría defina la fórmula y se configuran con
  `SCORE_WEIGHT_*`. Sin pesos, `ParallelSource` conserva `CombineProvisional` (decisión D-4: promedio simple).
- El corte por presupuesto **cuenta como falla** del circuit breaker de la fuente, igual que un error, un 5xx o una conexión
  rechazada, y cada circuito abre (decisión D-3 c, ajustada tras la validación; modo `FailureRatio` de `pkg/resilience`):
  - por **proporción**: cuando, en una ventana de `BREAKER_WINDOW_MS` (5 s), hubo al menos `BREAKER_MIN_REQUESTS` llamadas (50)
    y fallaron al menos `BREAKER_FAILURE_RATIO` de ellas (80 %); o
  - tras **`BREAKER_CONSECUTIVE_FAILURES` fallas seguidas** (30), que cuenta el propio circuito sin reiniciarlas al terminar cada
    ventana (solo un éxito o un cambio de estado las reinician). Es la red de seguridad para poco tráfico: con menos de 50
    llamadas por ventana y por instancia la proporción nunca se evalúa, y aun así una fuente colgada queda aislada (si fallan
    todas las llamadas, las 30 seguidas llegan antes que las 50 que necesita la proporción).

  Así una fuente caída o colgada de forma persistente queda aislada con cualquier tráfico (HA-D01 CA-1) y el cliente deja de
  esperarla, mientras que la cola normal de latencia (≈ 17 % de cortes con el perfil `normal` de EXP-L01) y una fuente
  degradada pero viva (≈ 62 % de cortes, perfil `degradado`) no abren el circuito. Half-open como en EC-D01: 5 s abierto, una
  sonda fallida (también la cortada por el presupuesto) reabre y 3 éxitos seguidos cierran.
  **Por qué 50 llamadas:** con un mínimo de 20, la proporción se evaluaba con tan pocas llamadas que una fuente degradada
  llegaba a veces al 80 % por azar, y una vez abierto su circuito casi nunca cierra (3 éxitos seguidos con ≈ 38 % de éxito por
  sonda); en el humo local pasó una vez en 10 fases `degradado`. Con 50 no se vio en 12 fases `degradado`, y la red de 30 fallas
  seguidas sigue aislando una fuente colgada con poco tráfico. `BREAKER_MIN_REQUESTS` es configurable.
- Los tres adaptadores comparten un `http.Client` con un pool de `HTTP_MAX_CONNS_PER_HOST` conexiones por host (EXP-L01).
- `GET /v1/risk-profiles/{customer_id}?consent_active=true` devuelve lo mismo que el `POST` para que el API Gateway lo sirva
  desde su caché por socio (clave = ruta + query + API key; TTL 60 s, decisión D-6) sin recalcular.

## Configuración (variables de entorno)

| Variable | Defecto | Descripción |
|---|---|---|
| `PORT` | `8080` | Puerto HTTP |
| `REDIS_ADDR` | — (obligatoria) | Dirección de Redis / ElastiCache |
| `OPEN_FINANCE_URL` | — (obligatoria) | URL base de Open Finance (en local, el stub detrás de Toxiproxy) |
| `OPEN_FINANCE_TIMEOUT_MS` | `700` | Corte duro de la dependencia |
| `BREAKER_CONSECUTIVE_FAILURES` | `30` con proporción · `3` con `BREAKER_FAILURE_RATIO=0` | Fallas seguidas que abren el circuito. Con proporción (por defecto) es la red de seguridad para poco tráfico: las cuenta el propio circuito sin reiniciarlas al terminar cada ventana (solo con un éxito o un cambio de estado). Con `BREAKER_FAILURE_RATIO=0` es el modo de EC-D01 |
| `BREAKER_OPEN_TIMEOUT_MS` | `5000` | Tiempo abierto antes de las sondas |
| `BREAKER_HALF_OPEN_SUCCESSES` | `3` | Éxitos consecutivos que cierran el circuito |
| `BREAKER_FAILURE_RATIO` | `0.8` | Proporción de llamadas fallidas (incluido el corte por presupuesto) que abre el circuito dentro de la ventana (D-3 c). Entre 0 y 1; `0` vuelve a las fallas consecutivas de EC-D01 (no recomendado con el presupuesto de 120 ms: la cola normal de latencia abriría el circuito) |
| `BREAKER_MIN_REQUESTS` | `50` | Llamadas mínimas dentro de la ventana antes de evaluar la proporción. Con menos (p. ej. 20) una fuente degradada pero viva llega a veces al 80 % por azar y su circuito se abre en falso |
| `BREAKER_WINDOW_MS` | `5000` | Ventana de conteo del circuito cerrado; los conteos empiezan de cero en cada ventana |
| `PROFILE_CACHE_TTL_HOURS` | `720` | Vigencia del perfil en caché (30 días) |
| `OPEN_DATA_URL` | vacío | URL base de Open Data. Vacía: la fuente queda fuera del abanico (aviso en el log) |
| `KYC_URL` | vacío | URL base de KYC. Vacía: la fuente queda fuera del abanico (aviso en el log) |
| `PROFILE_REUSE_ENABLED` | `true` | Reutiliza el perfil completo de ≤ 30 días sin consultar las fuentes (HU-W1.2-02). Con `false` se vuelve al comportamiento de HA-D01 / HA-L01: siempre se consulta primero y la caché solo respalda una respuesta provisional. Sirve como interruptor de apagado |
| `SCORE_WEIGHT_OPEN_FINANCE` | `0.5` | Peso de Open Finance en el score (HU-W1.2-02). Entre 0 y 1; son relativos, `0` deja la fuente fuera del score |
| `SCORE_WEIGHT_OPEN_DATA` | `0.3` | Peso de Open Data en el score |
| `SCORE_WEIGHT_KYC` | `0.2` | Peso de KYC en el score |
| `SOURCE_BUDGET_MS` | `120` | Presupuesto de latencia de cada fuente (CA-2 de HA-L01) |
| `PROFILING_DEADLINE_MS` | `700` | Corte total del abanico (CA-2 de HA-L01) |
| `HTTP_MAX_CONNS_PER_HOST` | `200` | Pool de conexiones por host del cliente HTTP compartido (EXP-L01) |
| `OPEN_FINANCE_REQUIRED` | `true` | Sin Open Finance no se calcula el perfil en línea y la oferta sale provisional (D-2). `false`: fuentes equivalentes |

`OPEN_FINANCE_TIMEOUT_MS` queda como tope del adaptador de Open Finance (Open Data y KYC usan 700 ms); en el recorrido el corte
efectivo de cada fuente es el menor entre ese tope y `SOURCE_BUDGET_MS`. Los `BREAKER_*` se aplican a cada fuente por separado.

## Requisitos de calidad que gobiernan el servicio

- Cotización p95 ≤ 250 ms; perfilamiento en línea p95 ≤ 400 ms / p99 ≤ 800 ms (HA-L01).
- Presupuesto de 120 ms por dependencia externa y corte duro a 700 ms; si una falla, se arma el perfil con lo que llegó o se responde con caché/oferta provisional (HA-D01).
- Cobro idempotente: una `idempotency_key` única por aceptación; los reintentos nunca duplican un cobro confirmado (HA-D02).
- Despliegue: 3 tareas Fargate (camino crítico), autoescalado por CPU al 60 %.

## Datos y eventos

- **BD propia:** PostgreSQL (originación). Caché: ElastiCache Redis (`internal/adapters/redis`, patrón cache-aside).
- **Publica:** `PolicyIssued`, `ChargeCompleted` (con outbox). Ver [catálogo de eventos](../../docs/events/README.md).
- **Consume:** ninguno por ahora.
- **Depende de:** `pkg/adapters` (Open Finance, Open Data, KYC, pagos, firma), `pkg/outbox`, `pkg/idempotency`, `pkg/resilience`.

## Historias asociadas

Épica web 1 (F1.1–F1.6) y las historias de arquitectura HA-L01, HA-D01 y HA-D02 en la [Wiki](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Historias-de-usuario-y-Historias-de-arquitectura).
Experimentos relacionados: [EXP-L01](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/EXP-L01-—-Perfilamiento-en-paralelo), [EC-D01](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/EC‐D01-—-Circuit-breaker-y-caché-de-perfil-de-riesgo), [EC-D02](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/EC‐D02-—-Cobro-idempotente-con-reintentos).

## Desarrollo

```
REDIS_ADDR=localhost:6379 OPEN_FINANCE_URL=http://localhost:9000 \
  go run ./services/origination/cmd/origination
```

Pruebas: `go test ./services/origination/...` (incluye `tests/integration`, que ejercita cada criterio de aceptación de HA-D01). La simulación de la caída con Toxiproxy y k6 está en [`tests/resilience/ha-d01`](../../tests/resilience/ha-d01/README.md).

Con las tres fuentes (HA-L01), agregar `OPEN_DATA_URL=http://localhost:9010 KYC_URL=http://localhost:9020`. `tests/integration/parallel_quoting_test.go`
verifica los presupuestos, la cancelación sin conexiones ni goroutines remanentes y el perfil parcial; el escenario de carga local con
Toxiproxy, k6 y Schemathesis está en [`tests/load-k6/ha-l01`](../../tests/load-k6/ha-l01/README.md).
