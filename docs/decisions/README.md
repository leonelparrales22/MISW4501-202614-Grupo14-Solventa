# Bitácora de decisiones del repositorio

Registro de las decisiones tomadas por el equipo sobre **este repositorio** (estructura, convenciones, herramientas, flujo de trabajo). Las decisiones de arquitectura del sistema están en la [Wiki](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Recopilación-Decisiones-de-Arquitectura).

**Cómo usarla:** solo se agrega una fila cuando el equipo aprueba una decisión; los agentes de IA no deciden por su cuenta, preguntan. Si una decisión reemplaza a otra, se marca la anterior como *Reemplazada* y se enlaza la nueva. Las filas no se borran.

| # | Fecha | Decisión | Motivo | Dónde se aplica | Estado |
|---|---|---|---|---|---|
| 1 | 2026-10-10 | Monorepo para el componente backend; web y móvil viven en repositorios propios | Un solo ciclo de versionado y CI para servicios, `pkg/` y `stubs/` | Estructura del repo | Vigente |
| 2 | 2026-10-10 | Un servicio por capacidad de negocio: `origination`, `claims`, `partners`, `audit`, `notifications` | Alinear fronteras con la arquitectura de la Wiki y facilitar la modificabilidad | `services/` | Vigente |
| 3 | 2026-10-10 | Todo el repositorio en inglés (código, comentarios, OpenAPI, commits, PR y archivos como `AGENTS.md`); solo los `README.md` en español | Convención uniforme para el equipo y los agentes | `AGENTS.md` §3 | Vigente |
| 4 | 2026-10-10 | HTTP con `chi/v5`; el resto sobre la biblioteca estándar y `pgx/v5`; sin framework web ni ORM | Poca sobrecarga, latencia predecible y enrutamiento extensible a futuro | `services/*/cmd`, `README.md` | Vigente |
| 5 | 2026-10-10 | `develop` es la rama de integración; los PR se abren contra ella | Proteger `main` como rama de entregas | `AGENTS.md` §7 | Vigente |
| 6 | 2026-10-10 | Merge `feature → develop` con **squash**; `develop → main` con **merge commit** | Historial limpio por historia y sin divergencia entre `develop` y `main` | `AGENTS.md` §7.1 | Vigente |
| 7 | 2026-10-10 | Fronteras entre servicios verificadas con `depguard`, con una regla por servicio | `depguard` compara por prefijo: una sola regla sobre `services/` bloquearía también los imports del propio servicio | `.golangci.yml` | Vigente |
| 8 | 2026-10-10 | `make lint` se ejecuta en el CI (`backend-ci`) | Que la regla de lint de la Definición de Terminado no dependa de que cada persona la corra | `.github/workflows/backend-ci.yml` | Vigente |
| 9 | 2026-10-10 | La cobertura mínima (70 %) se revisa manualmente; no hay script que la fuerce | Decisión del equipo por ahora | `AGENTS.md` §5 | Vigente |
| 10 | 2026-10-10 | Las decisiones del repo se registran en esta bitácora; no se usan ADRs por archivo | Un solo archivo, fácil de leer para personas y agentes, sin duplicar la Wiki | `AGENTS.md` §6 | Vigente |
| 11 | 2026-10-10 | En `claims`, `assessors` es dueño de los prestadores, la asignación del más cercano (distancia y especialidad), el seguimiento y el cierre; `assistance` recibe las solicitudes móviles, administra la bandeja y el despacho, y llama a `assessors` para elegir prestador | Una sola implementación del cálculo de distancia (HU-W2.3-01, reutilizada por HU-W2.3-03) y fronteras claras entre módulos | `services/claims/README.md` | Vigente |
| 12 | 2026-10-10 | El adaptador y el stub de telemetría/IoT se llaman `iot` (`pkg/adapters/iot`, `stubs/iot`) | Evitar confusión con `pkg/observability` (OpenTelemetry) | `pkg/README.md`, `stubs/README.md`, `go.work` | Vigente |
| 13 | 2026-10-10 | Excepción temporal al 70 % de cobertura mientras los servicios no tengan suite de pruebas; `make test` debe seguir pasando y cada PR declara la cobertura actual y la brecha | Proyecto en etapa inicial, sin pruebas todavía; evita reportar un requisito incumplido como si se cumpliera | `AGENTS.md` §5 | Vigente |
