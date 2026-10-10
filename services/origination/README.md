# Servicio `origination` (originación)

Camino crítico del negocio: cotiza, perfila, emite y cobra. Es la fachada (*Facade*) del ciclo de vida de la póliza.

**Estado:** esqueleto; solo expone `GET /health`.

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `orchestrator` | Orquestador de cotización: coordina perfilamiento y rating. Consulta primero la caché y luego las fuentes externas en paralelo |
| `profiling` | Perfil de riesgo con Open Finance / Open Data (con consentimiento). Usa `Perfil_Riesgo_Cache` en Redis (TTL 30 días, se invalida al recotizar) |
| `rating` | Motor de rating: prima y coberturas. Reglas actuariales aisladas del resto |
| `underwriting` | Suscripción automática: acepta, rechaza o ajusta el riesgo; enruta a decisión humana (bandeja del asesor) cuando aplica |
| `issuance` | Aceptación de la oferta, documento contractual (firma) y activación de la cobertura |
| `billing` | Cobro de la prima y reembolsos contra la pasarela: `Intento_Cobro` con `idempotency_key` única por aceptación, reintentos con backoff (HA-D02) |
| `endorsements` | Modificaciones y endosos con trazabilidad |
| `renewal` / `cancellation` | Renovación y cancelación con prorrateo de reembolso |

Las llamadas entre orquestador, perfilamiento y rating son **en proceso** (sin salto de red).

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
go run ./services/origination/cmd/origination   # PORT=8080 por defecto
```
