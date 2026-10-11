# Servicio `origination` (originación)

Camino crítico del negocio: cotiza, perfila, emite y cobra. Es la fachada (*Facade*) del ciclo de vida de la póliza.

**Estado:** `GET /health` y `POST /v1/risk-profiles/resolve` (HA-D01: oferta continua ante caída de la fuente externa). El resto de módulos sigue reservado.

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `orchestrator` | Orquestador de cotización: coordina perfilamiento y rating. Consulta primero la caché y luego las fuentes externas en paralelo |
| `profiling` | Perfil de riesgo con Open Finance / Open Data (con consentimiento). **HA-D01:** `Resolver` con la cadena de degradación Open Finance → `Perfil_Riesgo_Cache` (Redis, TTL 30 días) → perfil actuarial base; la oferta degradada sale marcada como provisional. La invalidación al recotizar llega con HU-W1.2-02 |
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
| `http` | Router `chi`: `GET /health` (superficial) y `POST /v1/risk-profiles/resolve` |
| `openfinance` | Puente entre el cliente compartido `pkg/adapters/openfinance` (timeout 700 ms + circuit breaker) y el puerto `RiskProfileSource` |
| `redis` | `Perfil_Riesgo_Cache` (cache-aside), clave `risk_profile:{customer_id}`, TTL 30 días y timeout propio de 100 ms por operación para que Redis nunca bloquee la oferta |

## Oferta continua ante caída de la fuente (HA-D01)

```
sin consentimiento activo ───────────▶ 422 consent_required (no se consulta nada)
Open Finance responde (< 700 ms) ────▶ mode=normal,      source=open_finance  (y calienta la caché)
Open Finance caído, lento o aislado
  └ perfil en caché de ≤ 30 días ────▶ mode=provisional, source=cache          (con profile_date)
  └ sin perfil en caché (cliente nuevo) ▶ mode=provisional, source=base_actuarial
```

- El circuit breaker abre con 3 fallas consecutivas, permanece abierto 5 s y se cierra tras 3 éxitos consecutivos en half-open (combinación validada en EC-D01).
- Una falla de Redis nunca rompe la respuesta: si no se puede leer la caché se usa el perfil base; si no se puede escribir, la oferta sigue siendo normal.
- La respuesta incluye lo que pintan las pantallas del prototipo: `mode`, `source`, `profile_date` (E3b), `unavailable_source` y `notice` (`last_profile_in_use` para E3b, `personalization_unavailable` para E3c y el aviso de E2b). Los textos los traduce cada cliente.
- `consent_active` lo afirma el llamador mientras `audit` no expone la consulta de consentimiento (HU-W1.2-01); entonces se reemplazará por un puerto.
- Fuera de esta historia: respetar/recotizar ofertas provisionales y notificar (HU-W1.2-03), consulta concurrente de las tres fuentes (HA-L01 / HU-W1.2-02).

## Configuración (variables de entorno)

| Variable | Defecto | Descripción |
|---|---|---|
| `PORT` | `8080` | Puerto HTTP |
| `REDIS_ADDR` | — (obligatoria) | Dirección de Redis / ElastiCache |
| `OPEN_FINANCE_URL` | — (obligatoria) | URL base de Open Finance (en local, el stub detrás de Toxiproxy) |
| `OPEN_FINANCE_TIMEOUT_MS` | `700` | Corte duro de la dependencia |
| `BREAKER_CONSECUTIVE_FAILURES` | `3` | Fallas consecutivas que abren el circuito |
| `BREAKER_OPEN_TIMEOUT_MS` | `5000` | Tiempo abierto antes de las sondas |
| `BREAKER_HALF_OPEN_SUCCESSES` | `3` | Éxitos consecutivos que cierran el circuito |
| `PROFILE_CACHE_TTL_HOURS` | `720` | Vigencia del perfil en caché (30 días) |

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
