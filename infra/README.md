# `infra` — infraestructura como código y ambiente local

**Estado:** módulos [`apigateway`](terraform/modules/apigateway/README.md) (HA-L01; authorizer de Cognito y rutas humanas de HA-S01) y [`cognito`](terraform/modules/cognito/README.md) (HA-S01), probados con `terraform test` y proveedor simulado; composiciones efímeras [`staging/ha-l01`](terraform/environments/staging/ha-l01/README.md) y [`staging/ha-s01`](terraform/environments/staging/ha-s01/README.md) (esta última aplicada y destruida el 2026-10-11, con sus resultados en su README); el resto de carpetas sigue reservado.

- `terraform/modules`: `network`, `ecr`, `ecs`, `alb`, `apigateway`, `rds`, `elasticache`, `messaging` (SNS/SQS con DLQ), `s3`, `secrets`, `cognito`, `observability`.
- `terraform/environments`: `staging` (efímero, solo durante experimentos) y `production-demo` (entrega evaluable).
- `local`: configuración para el ambiente local (Toxiproxy y LocalStack con SNS/SQS).

Parámetros de despliegue fijados por el experimento EXP-D03 (ADR-02): health check del ALB cada 5 s, timeout 3 s, umbral unhealthy 2; deregistration delay 30 s; idle timeout 10 s; autoescalado por CPU al 60 %; 2 tareas por servicio (3 en originación). Ver [Arquitectura final](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Arquitectura_final).

Restricción: todo corre sobre créditos gratuitos de AWS; staging se levanta y destruye por experimento.
