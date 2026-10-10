# `pkg` — librerías compartidas

Módulo Go (`.../pkg`) que enlazan los servicios. Es lo **único** que un servicio puede importar fuera de su propio módulo.

**Estado:** carpetas reservadas; sin código todavía.

| Paquete | Propósito |
|---|---|
| `adapters/{openfinance,opendata,kyc,payments,esignature,iot}` | Capa de adaptadores (*library* en proceso): aísla el modelo interno del formato de cada proveedor externo. Incluye circuit breaker y degradación (≤ 120 ms por dependencia, corte duro 700 ms) |
| `resilience` | Timeouts, reintentos con backoff, circuit breaker |
| `events` | Contratos de eventos y publicación/consumo sobre SNS/SQS |
| `outbox` | *Transactional outbox*: persiste el evento junto con la operación de negocio |
| `idempotency` | Claves de idempotencia y consumidor idempotente |
| `observability` | OpenTelemetry (CloudWatch + X-Ray), logs estructurados con identificador de correlación |
| `httpx` | Utilidades HTTP sobre `chi` (middleware, respuestas de error) |
| `config` | Configuración por variables de entorno |
| `security` | Validación de JWT (Cognito) y propagación de claims |
| `health` | Handler de `GET /health` consumido por los health checks del ALB |

Regla: `pkg/` no importa nada de `services/`.
