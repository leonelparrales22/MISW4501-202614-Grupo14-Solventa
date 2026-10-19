# Arquitectura

La arquitectura se documenta en la Wiki: [Visión de arquitectura](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/VisArquitectura), [Arquitectura final](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Arquitectura_final) y [Recopilación de decisiones](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Recopilación-Decisiones-de-Arquitectura).

## Trazabilidad entre el repositorio y la arquitectura

| Elemento del repo | Origen en la arquitectura |
|---|---|
| `services/origination` | Servicio de originación «Facade» con orquestador de cotización, perfilamiento y motor de rating (módulos internos, 0 ms de red) |
| `services/claims` | Servicio de siniestros; caché de lectura Redis (EC-L02); evidencia en S3 |
| `services/partners` | Servicio de gestión de socios B2B (HA-01) |
| `services/audit` | Auditoría y consentimientos, append-only (HA-03) |
| `services/notifications` | Servicio de notificaciones; consumidor SQS + FCM |
| `pkg/adapters` | Capa de adaptadores «library» (circuit breaker, HA-02) |
| `pkg/outbox`, `pkg/idempotency`, `pkg/events` | Transactional outbox, consumidor idempotente, contratos de eventos (HA-D02, HA-05) |
| `stubs/*` | Stubs del ecosistema externo |
| `infra/terraform` | Tarea T4 del plan de sprints |
| `infra/terraform/modules/web-portal`, `environments/*/web`, `scripts/environment` | T5 (versión web): el cliente web corre en el navegador; sus archivos se sirven desde S3 privado detrás de CloudFront, con el API Gateway («Facade») en el mismo dominio. *Arquitectura final* no modela dónde se sirve el portal: es una decisión de despliegue de T5 informada al equipo, no un ADR |
| `tests/*` | k6, Toxiproxy, ZAP |

Decisión: monorepo para el componente backend; web y móvil viven en sus propios repositorios.
