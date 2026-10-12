# `infra` — infraestructura como código y ambiente local

**Estado:** módulos [`apigateway`](terraform/modules/apigateway/README.md) (HA-L01; authorizer de Cognito y rutas humanas de HA-S01) y [`cognito`](terraform/modules/cognito/README.md) (HA-S01), probados con `terraform test` y proveedor simulado; composiciones efímeras [`staging/ha-l01`](terraform/environments/staging/ha-l01/README.md) y [`staging/ha-s01`](terraform/environments/staging/ha-s01/README.md) (esta última aplicada y destruida el 2026-10-11, con sus resultados en su README); la base del CD ([`bootstrap/cd-ecr`](terraform/bootstrap/cd-ecr/README.md): repositorios ECR y rol OIDC, aplicada el 2026-10-11); el resto de carpetas sigue reservado.

- `terraform/modules`: `network`, `ecr`, `ecs`, `alb`, `apigateway`, `rds`, `elasticache`, `messaging` (SNS/SQS con DLQ), `s3`, `secrets`, `cognito`, `observability`.
- `terraform/environments`: `staging` (efímero, solo durante experimentos) y `production-demo` (entrega evaluable).
- [`terraform/bootstrap/remote-state`](terraform/bootstrap/remote-state/README.md): bucket S3 del estado remoto de todas las composiciones (T4); se aplica una vez y no se destruye.
- [`terraform/bootstrap/cd-ecr`](terraform/bootstrap/cd-ecr/README.md): repositorios ECR permanentes y rol de OIDC para que el workflow `backend-cd` publique las imágenes; se aplica una vez y no se destruye.
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
