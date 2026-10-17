# Servicio `origination` (originación)

Camino crítico del negocio: cotiza, perfila, emite y cobra. Es la fachada (*Facade*) del ciclo de vida de la póliza.

**Estado:** `GET /health`, `POST /v1/risk-profiles/resolve` (HA-D01: oferta continua ante caída de la fuente externa) y `GET /v1/risk-profiles/{customer_id}` con las fuentes externas consultadas en paralelo (HA-L01: cotización embebida rápida bajo tráfico pico). **HU-W1.3-01:** `POST /v1/offers/{offer_id}/acceptance`, `GET /v1/offers/{offer_id}/acceptance` y `GET /v1/acceptances/{acceptance_id}` (aceptación de la oferta y disparo asíncrono de la suscripción), con autenticación Cognito (`pkg/security`) y **adaptadores en memoria pendientes de reemplazo** (ofertas, suscripción, bandeja del asesor, emisión y persistencia; ver «Aceptación de la oferta»). **HU-W1.1-01:** `POST /v1/assisted-quotes`, `GET /v1/assisted-quotes/{quote_id}` y `GET /v1/customers?document_id=` (cotización asistida por asesor con el mismo perfilamiento y un motor de rating `v1`), con clientes y cotizaciones **en memoria** pendientes de PostgreSQL (ver «Cotización asistida»). **HU-W1.3-03:** `GET /v1/policies/{policy_id}/issuance-status`, `GET /v1/policies/{policy_id}/contract-document` y `GET /v1/documents/{token}` (documento contractual en PDF y notificación de emisión tras la activación de la cobertura), con el repositorio, la auditoría y los documentos **locales o en memoria** pendientes de PostgreSQL, del servicio `audit` y de S3 (ver «Documento contractual»). **HA-D02:** `GET /v1/acceptances/{acceptance_id}/charge`, `GET /v1/issuance-exceptions` y `POST /v1/acceptances/{acceptance_id}/charge/resume` (cobro de la prima sin pérdida ni duplicados ante la caída de la pasarela, con reintentos idempotentes persistidos en **PostgreSQL**; la cobertura se activa solo con el pago confirmado; ver «Cobro de la prima»). El resto de módulos sigue reservado.

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `orchestrator` | Orquestador de cotización: coordina perfilamiento y rating. Consulta primero la caché y luego las fuentes externas en paralelo. **HU-W1.1-01:** `AssistedQuoteService` valida la solicitud del asesor (errores por campo), registra o actualiza al cliente por documento, perfila con el `Resolver` (misma cadena de degradación), califica con el `Rater` y guarda la cotización con vigencia de 24 h |
| `profiling` | Perfil de riesgo con Open Finance / Open Data (con consentimiento). **HA-D01:** `Resolver` con la cadena de degradación Open Finance → `Perfil_Riesgo_Cache` (Redis, TTL 30 días) → perfil actuarial base; la oferta degradada sale marcada como provisional. **HU-W1.2-02:** el `Resolver` (con la opción `WithCachedProfileReuse`, activa por defecto en `main`) reutiliza un perfil **completo** de ≤ 30 días sin consultar el ecosistema, comparte una sola consulta entre solicitudes simultáneas del mismo cliente (`singleflight`) y expone `Invalidate` para la recotización. **HA-L01:** `ParallelSource` consulta Open Finance, Open Data y KYC en paralelo (120 ms por fuente, corte total de 700 ms), cancela la fuente lenta y arma el perfil parcial con las señales que llegaron; `CombineProvisional` es la regla de combinación por defecto; con `Weights` se usa `CombineWeighted` (HU-W1.2-02) |
| `rating` | Motor de rating: prima y coberturas. Reglas actuariales aisladas del resto. **HU-W1.1-01:** `Rater` versión `v1` (tabla mínima: prima mensual = monto × tasa base × factores de edad, riesgo del perfil y plazo), detrás del puerto `ports.Rater` para que HU-W1.2-03 lo amplíe sin cambiar a los llamadores |
| `underwriting` | Suscripción automática: acepta, rechaza o ajusta el riesgo; enruta a decisión humana (bandeja del asesor) cuando aplica |
| `issuance` | **HU-W1.3-03:** `ContractService` recibe la activación de la cobertura (`CoverageActivated`, idempotente por póliza), genera el PDF, lo guarda y lo asocia a la póliza, envía el correo con un enlace firmado y registra cada paso en la auditoría; un trabajador en segundo plano reintenta lo que falle con espera creciente sin repetir lo ya hecho. `ContractReader` sirve el estado y el documento (dueño, `advisor`, `operations` o enlace firmado). Aceptación de la oferta, documento contractual (firma) y activación de la cobertura. **HU-W1.3-01:** `AcceptanceService` registra la aceptación (oferta, fecha y hora, canal) antes de cualquier paso posterior, rechaza la oferta vencida y es idempotente por oferta; `Processor` ejecuta la suscripción en segundo plano, inicia la emisión o enruta a la bandeja del asesor, y reanuda con espera creciente la aceptación cuyo paso posterior falló |
| `billing` | Cobro de la prima y reembolsos contra la pasarela: `Intento_Cobro` con `idempotency_key` única por aceptación, reintentos con backoff (HA-D02). **HA-D02:** `Starter` (el `ports.IssuanceStarter` de las aceptaciones aprobadas: guarda el cobro pendiente con la foto de la aceptación, la oferta y las condiciones, sin llamar a la pasarela, y no cobra lo que no se podría activar), `Worker` (cobra con `pkg/idempotency`: clave `cobro-<aceptación>`, claim atómico en PostgreSQL, esperas crecientes en una ventana de 24 h; solo con el cobro confirmado publica `CoverageActivated` al documento contractual y reintenta la publicación si el consumidor falla) y `Service` (estado del cobro, excepciones de emisión y reanudación) |
| `endorsements` | Modificaciones y endosos con trazabilidad |
| `renewal` / `cancellation` | Renovación y cancelación con prorrateo de reembolso |

Las llamadas entre orquestador, perfilamiento y rating son **en proceso** (sin salto de red).

## Adaptadores (`internal/adapters`)

| Adaptador | Qué hace |
|---|---|
| `http` | Router `chi`: `GET /health` (superficial), `POST /v1/risk-profiles/resolve` y `GET /v1/risk-profiles/{customer_id}?consent_active=…` (misma respuesta, lectura cacheable por el API Gateway por socio; HA-L01). Rutas humanas de aceptación (HU-W1.3-01) con `security.Authenticate` + `RequireRole`, montadas solo si `AUTH_ISSUER` está definido |
| `postgres` | Base de datos propia de originación (`database/sql` + `pgx` v5 por defecto, EXP-D03). `Migrate` aplica al arrancar las migraciones embebidas de `migrations/` con `pg_advisory_lock` y `schema_migrations` (decisión D-3 de HA-D02). `ChargeStore`: tabla `charge_attempt`, cada transición en una sola sentencia condicional (HA-D02) |
| `payments` | Puente entre `pkg/adapters/payments` (cabecera `Idempotency-Key`, corte de 700 ms, circuit breaker) y el puerto `PaymentGateway` (HA-D02) |
| `pdf` | Genera el documento contractual (`go-pdf/fpdf`): datos de la póliza, asegurado, coberturas, prima, vigencia y condiciones generales; es determinista, así que su *checksum* prueba lo que recibió el asegurado (HU-W1.3-03) |
| `filestore` | Repositorio documental sobre un directorio (`DOCUMENTS_DIR`): una clave por versión, sin sobrescribir con contenido distinto y sin salir del directorio. Se reemplaza por S3 cuando exista el módulo de infraestructura (HU-W1.3-03) |
| `links` | Enlaces al documento firmados con HMAC-SHA256 y con vencimiento (`DOCUMENT_LINK_SECRET`, `DOCUMENT_LINK_TTL_HOURS`) (HU-W1.3-03) |
| `email` | Puente entre `pkg/adapters/email` (timeout, circuit breaker, envío idempotente) y el puerto `Notifier`; redacta el correo de emisión (HU-W1.3-03) |
| `memory` | **Pendiente de reemplazo (HU-W1.3-01, HU-W1.1-01, HU-W1.3-03).** `ContractStore` e `IssuanceAuditLog` (emisión contractual y su auditoría, en lugar de PostgreSQL y del servicio `audit`). `CustomerStore` y `QuoteStore` (clientes por documento y cotizaciones asistidas, en lugar de PostgreSQL). `AcceptanceStore` (en lugar de PostgreSQL: la aceptación resiste fallas de las dependencias pero no un reinicio del servicio), `OfferStub` (ofertas de demostración `demo-<escenario>-<sufijo>` en lugar de HU-W1.2-03), `UnderwriterStub` (reglas provisionales en lugar del motor de suscripción), `ReviewQueueStub` (en lugar de la bandeja de HU-W1.1-02) e `IssuanceStub` (solo cuando no hay pasarela configurada: registra el inicio de la emisión sin cobrar ni activar; con `PAYMENT_GATEWAY_URL` el cobro es el de HA-D02) |
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
| `AUTH_ISSUER`, `AUTH_CLIENT_IDS`, `AUTH_JWKS_URL`, `AUTH_TOKEN_USE`, `AUTH_LEEWAY_SECONDS` | vacío | Verificación del JWT de Cognito (HA-S01, ver [docs/api](../../docs/api/README.md#autenticación-y-autorización-ha-s01)). Con `AUTH_ISSUER` vacío las rutas de aceptación (HU-W1.3-01) **no se montan**; si está definido, `AUTH_CLIENT_IDS` es obligatoria |
| `EMAIL_URL` | vacío | URL del proveedor de correo (`stubs/email` en local). Con valor vacío **no se cablea** el documento contractual (HU-W1.3-03); si está definida, `DOCUMENTS_DIR` y `DOCUMENT_LINK_SECRET` son obligatorias. **Obligatoria si `PAYMENT_GATEWAY_URL` está definida** (decisión D-16 b de HA-D02): todo cobro confirmado produce su documento y su correo; si falta, originación no arranca y el error lo dice |
| `DOCUMENTS_DIR` | — | Directorio del repositorio documental (se crea si no existe; permisos de solo el propietario) |
| `DOCUMENT_LINK_SECRET` | — | Secreto (≥ 32 caracteres) con el que se firman los enlaces; viene del entorno o de Secrets Manager, nunca del código |
| `DOCUMENT_LINK_TTL_HOURS` | `168` | Vigencia del enlace seguro del correo |
| `DOCUMENTS_PUBLIC_BASE_URL` | `http://localhost:$PORT` | Base pública de los enlaces (el API Gateway en AWS) |
| `EMAIL_TIMEOUT_MS` | `2000` | Corte duro de cada envío de correo |
| `CONTRACT_POLL_INTERVAL_MS`, `CONTRACT_ATTEMPT_TIMEOUT_MS` | `500`, `10000` | Cada cuánto el trabajador busca emisiones pendientes (una activación nueva lo despierta enseguida) y el tope de un intento |
| `CONTRACT_RETRY_BASE_MS`, `CONTRACT_RETRY_MAX_MS` | `1000`, `15000` | Espera tras el primer fallo (se duplica) y su tope; caben varios intentos dentro de los 60 s |
| `CONTRACT_MAX_ATTEMPTS` | `12` | Intentos fallidos antes de marcar la emisión `failed` (el documento sigue disponible) |
| `LOG_LEVEL` | `info` | Nivel de los logs JSON (`debug`, `info`, `warn`, `error`). Cada línea lleva `service`, `correlation_id` y, dentro de una petición, `trace_id`/`span_id` |
| `ENVIRONMENT` | `local` | Etiqueta `deployment.environment` de la telemetría |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | vacío | URL base del colector OTLP/HTTP (p. ej. `http://localhost:4318`; en AWS, el sidecar ADOT hacia CloudWatch y X-Ray). Vacío: las trazas y métricas se generan pero no se exportan |
| `ACCEPTANCE_POLL_INTERVAL_MS` | `1000` | Cada cuánto el procesador busca aceptaciones pendientes (además, cada aceptación nueva lo despierta enseguida) |
| `ACCEPTANCE_ATTEMPT_TIMEOUT_MS` | `5000` | Tope de un intento de procesamiento de una aceptación |
| `ACCEPTANCE_RETRY_BASE_MS` | `2000` | Espera tras el primer intento fallido; se duplica en cada falla |
| `ACCEPTANCE_RETRY_MAX_MS` | `60000` | Tope de la espera entre intentos (≥ `ACCEPTANCE_RETRY_BASE_MS`) |
| `UNDERWRITING_STUB_FLAKY_FAILURES` | `2` | Intentos que falla la suscripción simulada para las ofertas `demo-flaky-*` (escenario de reanudación) |
| `UNDERWRITING_REVIEW_SCORE_THRESHOLD` | `0.8` | Puntaje del perfil por encima del cual la suscripción simulada envía una oferta normal al asesor (motivo `high_risk_score`); 0 es el menor riesgo. Mayor que 0 y hasta 1. Es una regla propia de la revisión, independiente de las categorías de precio |
| `PAYMENT_GATEWAY_URL` | vacío | URL de la pasarela de pagos (en local, `stubs/payment-gateway` en el puerto 9040, detrás de Toxiproxy). Vacía: la aceptación aprobada solo registra el inicio de la emisión (`IssuanceStub`), sin cobro ni activación de la cobertura, con aviso en el log; si además hay `EMAIL_URL`, el log avisa que no se emitirán documentos contractuales. Definida: el cobro de HA-D02 se monta con las rutas humanas y son obligatorias `DATABASE_URL`, `EMAIL_URL` y `AUTH_ISSUER`; si falta alguna, originación no arranca y el error dice cuál |
| `DATABASE_URL` | vacío | PostgreSQL de originación (secreto: viene del entorno o de Secrets Manager, nunca se registra). **Obligatoria si `PAYMENT_GATEWAY_URL` está definida** (decisión D-2b): el cobro siempre es durable |
| `PAYMENT_GATEWAY_TIMEOUT_MS` | `700` | Corte duro de cada intento de cobro (decisión D-9); un corte solo vuelve ambigua la respuesta, que se reintenta con la misma clave |
| `RETRY_WINDOW_SECONDS` | `86400` | Ventana de reintentos del cobro (24 h, ADR-01); al vencer, el cobro queda como excepción de emisión |
| `CHARGE_RETRY_BASE_MS`, `CHARGE_RETRY_MAX_MS` | `500`, `60000` | Espera tras el primer intento fallido (se duplica) y su tope (decisión D-4 A, valores de EC-D02) |
| `CHARGE_POLL_INTERVAL_MS` | `1000` | Cada cuánto el trabajador busca cobros y activaciones vencidos (un cobro nuevo o reanudado lo despierta enseguida) |
| `CHARGE_CLAIM_LEASE_MS` | `30000` | Vigencia del reclamo de un cobro o de la publicación de su activación (decisión D-8); debe ser mayor que `PAYMENT_GATEWAY_TIMEOUT_MS` |

`OPEN_FINANCE_TIMEOUT_MS` queda como tope del adaptador de Open Finance (Open Data y KYC usan 700 ms); en el recorrido el corte
efectivo de cada fuente es el menor entre ese tope y `SOURCE_BUDGET_MS`. Los `BREAKER_*` se aplican a cada fuente por separado.

## Aceptación de la oferta (HU-W1.3-01)

La aceptación es el **punto de no pérdida** del recorrido: se persiste (oferta, fecha y hora, canal) antes de invocar cualquier
dependencia, y la suscripción corre después, en segundo plano.

```
oferta vencida ─────────────────────▶ 422 offer_expired (no se registra nada; el cliente pide una oferta nueva)
oferta vigente ─────────────────────▶ 202 status=registered  ──(procesador asíncrono)──┐
oferta ya aceptada ─────────────────▶ 200 la aceptación existente (sin repetir nada)    │
                                                                                       ▼
  suscripción aprueba ─────────────▶ status=issuance_pending (se inicia la emisión, sin intervención manual)
  suscripción enruta a revisión ───▶ status=in_review (solicitud en la bandeja del asesor con solicitante, oferta y motivo)
  falla un paso posterior ─────────▶ sigue registered, processing.attempts+1 y nuevo intento con espera creciente
```

- **Rutas humanas** (`cognitoJwt`): solo el asegurado (`insured`) dueño de la oferta (`sub` = dueño) acepta; el dueño, `advisor` y
  `operations` consultan. Las respuestas llevan `Cache-Control: no-store`.
- **Idempotente por oferta:** una oferta tiene a lo sumo una aceptación; reenviar la aceptación devuelve la existente, aunque la
  oferta haya vencido después. No se usa cabecera `Idempotency-Key` (la clave del cobro llega con HA-D02).
- **Reanudación (CA-5):** la decisión de suscripción se guarda antes de actuar sobre ella, así que un reintento solo repite el paso
  idempotente que falló (encolar en la bandeja o iniciar la emisión). Espera de 2 s duplicándose hasta 60 s, sin límite de intentos.
- **Pendiente (adaptadores en memoria de `internal/adapters/memory`):** persistencia en PostgreSQL (hoy una aceptación no sobrevive a
  un reinicio del servicio), ofertas reales de HU-W1.2-03 (hoy `OfferStub` con ofertas `demo-<escenario>-<sufijo>`: `active`,
  `review`, `highrisk`, `expired`, `flaky`; la oferta se crea en la primera consulta y pertenece a quien la consulta), el motor de
  suscripción (hoy reglas provisionales: oferta provisional → `provisional_offer`; puntaje > `UNDERWRITING_REVIEW_SCORE_THRESHOLD`
  (0,8 por defecto) → `high_risk_score`; si no, aprueba),
  la bandeja de HU-W1.1-02 y el cobro y la emisión de HU-W1.3-02 / HA-D02. Generar una oferta nueva tras `offer_expired` es de
  HU-W1.2-03. No publica eventos todavía.
- Contrato para el front: [`docs/contratos/HU-W1.3-01-api.md`](../../docs/contratos/HU-W1.3-01-api.md).

## Documento contractual y notificación (HU-W1.3-03)

Cuando se activa la cobertura, el servicio genera **sin intervención manual** el documento contractual (condiciones, coberturas, prima y
vigencia) en máximo 60 s, lo guarda en el repositorio documental asociado de forma permanente a la póliza, avisa al asegurado por correo con un
enlace seguro y registra la fecha y hora, la versión del documento y el resultado de la entrega. El contrato para el front (pantalla E5) está en
[`docs/contratos/HU-W1.3-03-api.md`](../../docs/contratos/HU-W1.3-03-api.md).

```
CoverageActivated ─▶ Receive (persiste y despierta al trabajador; idempotente por póliza)
                      └▶ genera PDF ─▶ guarda y asocia (document_ready) ─▶ firma el enlace ─▶ correo (completed) ─▶ auditoría
                          cada paso se persiste antes del siguiente: tras una falla solo se repite el paso que falló
                          falla del correo ─▶ reintento con espera creciente (el documento ya está disponible) ─▶ tras N intentos: failed
```

- El evento lo publica el cobro de HA-D02 tras confirmarse el pago (ver «Cobro de la prima»; monto asegurado y plazo de demostración hasta
  HU-W1.2-03); en `docs/events/README.md` están su payload y la asignación pendiente del nombre `PolicyIssued`.
- El envío es idempotente por `policy-issued:{policy}:v{version}`; el documento nunca se sobrescribe con contenido distinto.
- Observabilidad: spans `contract.advance`, `.generate` y `.notify`; el histograma `contract.activation_to_available` (SLO de 60 s) y los
  contadores `contract.slo.breached`, `contract.attempt.failed` (por paso) y `contract.issuance.completed`; los logs llevan `policy_id` y
  `correlation_id`.
- Pendiente: PostgreSQL y el servicio `audit` para la emisión y su auditoría, S3 para los documentos y el evento en SNS con *outbox* (HU-W1.3-02).

## Cobro de la prima (HA-D02)

Cuando la suscripción aprueba una aceptación, originación cobra la prima **sin perderla ni duplicarla** aunque la pasarela esté caída o
responda de forma ambigua (ADR-01, experimento EC-D02), y activa la cobertura **solo** con el pago confirmado (CA-4). El contrato para el
front (pantalla E4b) y para operaciones está en [`docs/contratos/HA-D02-api.md`](../../docs/contratos/HA-D02-api.md).

```
suscripción aprueba ─▶ billing.Starter: INSERT charge_attempt (pending, clave cobro-<aceptación>, foto) ─▶ issuance_pending
billing.Worker (cada tarea) ─▶ claim atómico (UPDATE condicional con lease) ─▶ solo el ganador llama a la pasarela
   ├─ 2xx ──────────────▶ confirmed + pol-<aceptación> + vigencia (misma sentencia) ─▶ CoverageActivated ─▶ documento contractual
   ├─ rechazo explícito ─▶ failed (payment_rejected) ─▶ excepción de emisión
   └─ ambiguo / caída ──▶ pending, attempts+1, espera 500 ms ×2 hasta 60 s, misma clave
                          └─ al vencer la ventana de 24 h ─▶ failed (retry_window_expired) ─▶ excepción ─▶ operaciones reanuda
```

- **Clave determinística** `cobro-` + `acceptance_id`, nunca aleatoria: cada reintento la envía en `Idempotency-Key` y la pasarela no cobra
  dos veces la misma clave (requisito de cualquier proveedor, decisión D-10). El `UNIQUE` no basta: el **claim** de una sola sentencia hace
  que solo un trabajador llame a la pasarela; con *lease* de 30 s, un cobro cuyo trabajador murió se vuelve a reclamar (D-8).
- **Activación:** al confirmarse, la misma sentencia fija `policy_id`, `activated_at` y la vigencia; el trabajador publica en proceso
  `domain.CoverageActivated` al `ports.CoverageActivationHandler` de HU-W1.3-03. Si el consumidor falla, se reintenta el mismo evento con
  espera creciente y **sin ventana** (el pago ya está hecho). Un claim de publicación evita que dos tareas publiquen a la vez.
- **No se cobra lo que no se puede activar** (D-17): si la foto no alcanza para el evento (por ejemplo, sin correo del titular), no se crea
  el cobro y la aceptación sigue `registered`, reintentándose.
- **Configuración:** con `PAYMENT_GATEWAY_URL` son obligatorias `DATABASE_URL`, `EMAIL_URL` y `AUTH_ISSUER` (D-2b, D-16 b y la
  decisión de Nicolás sobre m-3: sin las rutas humanas, los cobros guardados y las activaciones pagadas quedarían sin procesar); si falta
  alguna, originación no arranca. Sin pasarela no hay cobro
  ni activación: el stub que simulaba la activación se retiró (D-14).
- **Datos:** la foto (titular, prima, monto asegurado, plazo y coberturas) se guarda en `charge_attempt`, así que el evento sale igual
  aunque las ofertas sigan en memoria. Monto asegurado, plazo y coberturas son de demostración (`demoCoverageTerms` en `cmd`) hasta
  HU-W1.2-03. Una aceptación aún sin cobro se pierde con un reinicio (limitación heredada de HU-W1.3-01); un cobro ya creado no.
- **Observabilidad:** spans `charge.attempt` y `charge.activation.publish`; métricas `charge.attempt.completed` (por `outcome`:
  `confirmed`, `rejected`, `ambiguous`, `not_sent`), `charge.failed` (por `reason`), `charge.activation.published`,
  `charge.activation.publish_failed` y el histograma `charge.acceptance_to_confirmed`; logs con `acceptance_id`, `policy_id` y
  `correlation_id`, **sin datos personales**.
- Fuera de esta historia: el medio de pago y la pantalla de rechazo (HU-W1.3-02), la pantalla de monitoreo O1/O1b (HU-W1.3-04) y los
  eventos `ChargeCompleted`/`PolicyIssued` en SNS con *outbox* (HU-W1.3-02).
- Escenario con fallas inyectadas: [`tests/resilience/ha-d02`](../../tests/resilience/ha-d02/README.md).

## Observabilidad

`cmd/origination` instala `pkg/observability` al arrancar: logs JSON (también los de `log`), una traza por petición (`GET /v1/...`, nombrada por la ruta de chi, no por la URL) y las métricas `http.server.requests` y `http.server.duration`. `X-Correlation-Id` entra, se propaga por el contexto y vuelve en la respuesta; si falta o no es válido se genera uno. Las rutas nuevas heredan todo esto: basta registrar `httpadapter.WithObservability` como **primera** opción del router. Para instrumentar un caso de uso, `observability.Tracer(...)` y `observability.Meter(...)`, y registrar con `slog.*Context(ctx, ...)` para que el log lleve los identificadores.

## Cotización asistida (HU-W1.1-01)

Un asesor registra o recupera al cliente y obtiene prima y coberturas con el **mismo** perfilamiento que el canal embebido
(`profiling.Resolver`, con su cadena de degradación) y el motor `rating`. Solo el rol `advisor` usa estas rutas (`pkg/security`); se
montan junto con las de aceptación cuando `AUTH_ISSUER` está definido. El contrato para el front está en
[`docs/contratos/HU-W1.1-01-api.md`](../../docs/contratos/HU-W1.1-01-api.md).

```
solicitud inválida ────────────────▶ 422 validation_failed (todos los campos con su error; no se perfila ni se guarda nada)
sin consentimiento ────────────────▶ 422 consent_required
fuentes externas caídas ───────────▶ 201, mode=provisional (perfil en caché o perfil actuarial base) + notice
cotización generada ───────────────▶ 201, vigencia de 24 h (expires_at)
pasadas 24 h ──────────────────────▶ GET: status=expired, can_formalize=false, blocked_reason=quote_expired
```

- El cliente existente se recupera por documento (`GET /v1/customers`); cotizar con un documento conocido conserva su `customer_id` (de él
  depende la caché de perfil) y actualiza sus datos de contacto.
- La prima es **mensual** y la registra `rating_version`. La tabla `v1` es provisional: HU-W1.2-03 puede reemplazar el `Rater`.
- `consent_active` lo afirma el asesor en la solicitud mientras `audit` no exponga la consulta de consentimientos (HU-W1.2-01).
- Pendiente: PostgreSQL para clientes y cotizaciones y la formalización de la compra (HU-W1.1-03).
- Observabilidad: spans `assisted_quote.create`, `.profile` y `.rate`, la métrica `assisted_quotes.created` por `mode` y `profile_source`, y logs con `quote_id`, `customer_id` y `correlation_id` (nunca el documento ni el correo).

## Requisitos de calidad que gobiernan el servicio

- Cotización p95 ≤ 250 ms; perfilamiento en línea p95 ≤ 400 ms / p99 ≤ 800 ms (HA-L01).
- Presupuesto de 120 ms por dependencia externa y corte duro a 700 ms; si una falla, se arma el perfil con lo que llegó o se responde con caché/oferta provisional (HA-D01).
- Cobro idempotente: una `idempotency_key` única por aceptación; los reintentos nunca duplican un cobro confirmado (HA-D02).
- Despliegue: 3 tareas Fargate (camino crítico), autoescalado por CPU al 60 %.

## Datos y eventos

- **BD propia:** PostgreSQL (originación); hoy la tabla `charge_attempt` del cobro (HA-D02), con migraciones en `migrations/`. Caché: ElastiCache Redis (`internal/adapters/redis`, patrón cache-aside).
- **Publica:** `PolicyIssued`, `ChargeCompleted` (con outbox). Ver [catálogo de eventos](../../docs/events/README.md). La aceptación (HU-W1.3-01) no publica eventos: la suscripción, la bandeja y el inicio de la emisión ocurren en proceso. El cobro (HA-D02) publica **en proceso** `CoverageActivated` al documento contractual solo con el pago confirmado; `ChargeCompleted` y `PolicyIssued` en SNS siguen pendientes de HU-W1.3-02.
- **Consume:** ninguno por ahora.
- **Depende de:** `pkg/adapters` (Open Finance, Open Data, KYC, pagos, firma), `pkg/outbox`, `pkg/idempotency`, `pkg/resilience`.

## Historias asociadas

Épica web 1 (F1.1–F1.6; implementada: HU-W1.3-01, con dependencias en memoria) y las historias de arquitectura HA-L01, HA-D01 y HA-D02 en la [Wiki](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Historias-de-usuario-y-Historias-de-arquitectura).
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

Aceptación de la oferta (HU-W1.3-01): agregar las variables `AUTH_*` del pool de Cognito; en local, el de Moto que siembra
[`tests/security/ha-s01/local`](../../tests/security/ha-s01/local/README.md) (`AUTH_ISSUER`, `AUTH_JWKS_URL` y `AUTH_CLIENT_IDS`
que imprime `seed.sh`). `tests/integration/acceptance_test.go` ejercita cada criterio de aceptación de punta a punta por HTTP.

Cobro de la prima (HA-D02) y pantalla E5 en local: el cobro necesita PostgreSQL, la pasarela y el correo. Se pueden levantar solo esos
servicios del escenario de HA-D02 y apuntar originación a ellos. El compose publica en `127.0.0.1` PostgreSQL en el 5440
(`POSTGRES_PORT`), la pasarela en el 9140 y el correo en el 9130; la contraseña de PostgreSQL la elige cada quien en `PG_PASSWORD`:

```
export PG_PASSWORD='<contraseña local>'
docker compose -f tests/resilience/ha-d02/docker-compose.yml up -d --wait postgres payment-gateway email
PAYMENT_GATEWAY_URL=http://localhost:9140 DATABASE_URL="postgres://origination:$PG_PASSWORD@localhost:5440/origination?sslmode=disable" \
  EMAIL_URL=http://localhost:9130 DOCUMENTS_DIR=/tmp/solventa-docs DOCUMENT_LINK_SECRET=... <variables AUTH_* de Moto> \
  go run ./services/origination/cmd/origination
```

Al terminar, `docker compose -f tests/resilience/ha-d02/docker-compose.yml down -v`. `tests/integration/charge_test.go` (PostgreSQL de
Testcontainers; se salta sin Docker) ejercita cada criterio de HA-D02 con la emisión contractual real.
