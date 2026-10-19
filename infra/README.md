# `infra` — infraestructura como código y ambiente local

**Estado:** módulos [`apigateway`](terraform/modules/apigateway/README.md) (HA-L01; authorizer de Cognito y rutas humanas de HA-S01) y [`cognito`](terraform/modules/cognito/README.md) (HA-S01), probados con `terraform test` y proveedor simulado; composiciones efímeras [`staging/ha-l01`](terraform/environments/staging/ha-l01/README.md) y [`staging/ha-s01`](terraform/environments/staging/ha-s01/README.md) (esta última aplicada y destruida el 2026-10-11, con sus resultados en su README); la base del CD ([`bootstrap/cd-ecr`](terraform/bootstrap/cd-ecr/README.md): repositorios ECR y rol OIDC, aplicada el 2026-10-11; el parámetro con la etiqueta de `main` y el rol de despliegue en `production-demo` con aprobación manual están escritos y probados con `terraform test`, **pendientes de aplicar**). **T4:** plataforma completa por ambiente ([`modules/platform`](terraform/modules/platform/README.md) sobre los módulos `network`, `ecr`, `alb`, `ecs`, `rds`, `elasticache`, `messaging`, `s3` y `secrets`) con sus composiciones [`staging/platform`](terraform/environments/staging/platform/README.md) y [`production-demo/platform`](terraform/environments/production-demo/platform/README.md), validadas con `terraform test` y proveedor simulado; `staging/platform` **aplicada y destruida el 2026-10-17** (ventana de validación, resultados en su README) y `production-demo/platform` sin aplicar todavía; `observability` sigue reservado. **HA-D02 (fase AWS):** la plataforma despliega los stubs `email` y `payment-gateway` (HU-W1.3-02) con tareas por stub para tumbar la pasarela en una ventana, guarda el documento contractual en el bucket `documents` con la clave de sus enlaces en Secrets Manager, publica la bandeja (`/v1/review-requests`), las excepciones de emisión y la ruta pública del enlace firmado (`GET /v1/documents/{token}`, con `*/*` como tipo binario), deja originación en una tarea (C5) y documenta el primer `apply` en dos tiempos; `staging/platform` trae etiquetas y palancas opcionales para la ventana ([`ha-d02.example.tfvars`](terraform/environments/staging/platform/ha-d02.example.tfvars)). **T4 (pool permanente):** las dos composiciones de la plataforma toman el user pool permanente de [`bootstrap/identity`](terraform/bootstrap/identity/README.md) (`solventa-users`, con un usuario de prueba por rol) por estado remoto, con la variable `existing_cognito` de `modules/platform`.

- `terraform/modules`: `network`, `ecr`, `ecs`, `alb`, `apigateway`, `rds`, `elasticache`, `messaging` (SNS/SQS con DLQ), `s3`, `secrets`, `cognito`, `platform` (composición de todos), `observability` (reservado).
- `terraform/environments`: `staging` (efímero, solo durante experimentos) y `production-demo` (entrega evaluable). En cada uno, `platform` es la plataforma completa; `staging/ha-l01` y `staging/ha-s01` son ventanas de medición propias.
- Parámetros de costo por variable (mismo criterio en ambos ambientes): `nat_mode` (`none` por defecto, `single`, `per_az` para la demo) y `rds_multi_az` / `redis_multi_az` (`false` por defecto, `true` para la demo final o una prueba de tolerancia a fallas).
- [`terraform/bootstrap/remote-state`](terraform/bootstrap/remote-state/README.md): bucket S3 del estado remoto de todas las composiciones (T4); se aplica una vez y no se destruye.
- [`terraform/bootstrap/cd-ecr`](terraform/bootstrap/cd-ecr/README.md): repositorios ECR permanentes, rol de OIDC para que el workflow `backend-cd` publique las imágenes, parámetro SSM con la etiqueta de `main` y rol para desplegarla en `production-demo` (con aprobación manual); se aplica una vez y no se destruye.
- [`terraform/bootstrap/identity`](terraform/bootstrap/identity/README.md): user pool permanente de Cognito con usuarios de prueba por rol, que `production-demo/platform` y `staging/platform` leen por estado remoto (T4); se aplica una vez y no se destruye.
- `local`: configuración para el ambiente local (Toxiproxy y LocalStack con SNS/SQS).

Parámetros de despliegue fijados por el experimento EXP-D03 (ADR-02): health check del ALB cada 5 s, timeout 3 s, umbral unhealthy 2; deregistration delay 30 s; idle timeout 10 s; autoescalado por CPU al 60 %; 2 tareas por servicio (3 en originación). Ver [Arquitectura final](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Arquitectura_final).

Restricción: todo corre sobre créditos gratuitos de AWS; staging se levanta y destruye por experimento.

## Estado remoto de Terraform

Todas las composiciones guardan su estado en el bucket S3 **`solventa-tfstate-368083581678`** (`us-east-2`), creado por
[`terraform/bootstrap/remote-state`](terraform/bootstrap/remote-state/README.md) (T4): versionado, cifrado SSE-S3, sin acceso
público, solo TLS y versiones anteriores durante 30 días. El bloqueo es el **nativo de S3** (`use_lockfile = true`, sin tabla
DynamoDB), por lo que se necesita **Terraform 1.11 o superior**.

Convención de claves: una por composición, **`<ambiente>/<composición>/terraform.tfstate`** (p. ej.
`staging/ha-l01/terraform.tfstate`), sin *workspaces*. Bloque `backend` de ejemplo (los valores van literales: un `backend` no
admite variables):

```hcl
terraform {
  required_version = ">= 1.11"

  backend "s3" {
    bucket       = "solventa-tfstate-368083581678"
    key          = "staging/ha-l01/terraform.tfstate"
    region       = "us-east-2"
    use_lockfile = true
    encrypt      = true
  }
}
```

El [README del bootstrap](terraform/bootstrap/remote-state/README.md) explica cómo iniciar una composición contra el bucket,
cómo migrar un estado local, los permisos IAM necesarios, cómo restaurar una versión anterior y qué hacer con un bloqueo colgado.
