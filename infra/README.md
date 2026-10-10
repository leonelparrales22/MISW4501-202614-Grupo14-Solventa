# `infra` — infraestructura como código y ambiente local

**Estado:** carpetas reservadas; sin código todavía.

- `terraform/modules`: `network`, `ecr`, `ecs`, `alb`, `apigateway`, `rds`, `elasticache`, `messaging` (SNS/SQS con DLQ), `s3`, `secrets`, `cognito`, `observability`.
- `terraform/environments`: `staging` (efímero, solo durante experimentos) y `production-demo` (entrega evaluable).
- `local`: configuración para el ambiente local (Toxiproxy y LocalStack con SNS/SQS).

Parámetros de despliegue fijados por el experimento EXP-D03 (ADR-02): health check del ALB cada 5 s, timeout 3 s, umbral unhealthy 2; deregistration delay 30 s; idle timeout 10 s; autoescalado por CPU al 60 %; 2 tareas por servicio (3 en originación). Ver [Arquitectura final](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Arquitectura_final).

Restricción: todo corre sobre créditos gratuitos de AWS; staging se levanta y destruye por experimento.
