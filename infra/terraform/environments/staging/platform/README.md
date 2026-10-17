# Composición `staging/platform` — plataforma efímera de staging (T4)

Plataforma completa del módulo [`platform`](../../../modules/platform/README.md) en tamaño de experimento: los cinco servicios,
los stubs de proveedores y toda la infraestructura que necesitan. Como el resto de staging, **se levanta para una ventana y se
destruye en la misma sesión**. La topología está en el README del módulo; aquí solo cambian los valores.

> **Estado:** código validado con `terraform validate` y `terraform test` (proveedor simulado). **No se ha aplicado en AWS.**
> Cada ventana exige la autorización del equipo (estimación de costo escrita y aprobada) y destruirla en la misma sesión.

## Valores del ambiente

| Parámetro | Valor | Por qué |
|---|---|---|
| Nombre / VPC | `solventa-stg`, `10.50.0.0/16` | No se cruza con `ha-l01` (`10.40.0.0/16`) |
| Tareas | 1 por servicio y por stub (`origination` 0,5 vCPU / 1 GB, el resto 0,25 vCPU / 0,5 GB); autoescalado por CPU al 60 % hasta 2 | Tamaño de experimento |
| NAT | `nat_mode = "none"` por defecto | Costo |
| RDS / Redis | Single-AZ, backup de 1 día | Costo |
| Ciclo de vida | `ephemeral = true`: sin snapshot final, buckets con `force_destroy`, secretos sin ventana de recuperación (se pueden recrear con el mismo nombre); sin `deletion_protection` | Se destruye tras cada ventana |
| Logs | 3 días | — |
| Usage plans | D-7: 100 req/s, ráfaga 200, sin cuota, por socio | Decisión D-7 vigente |
| Estado remoto | `staging/platform/terraform.tfstate` en `solventa-tfstate-368083581678`, bloqueo nativo de S3 | Convención de T4 |

Etiquetas por defecto: `Project=solventa`, `Env=staging`, `Task=T4`, `TTL=<var.ttl>`.

## Variables

| Variable | Defecto | Descripción |
|---|---|---|
| `ttl` | — (obligatoria) | Fecha `AAAA-MM-DD` límite; etiqueta `TTL` |
| `image_tag` | `null` | SHA de 12 caracteres de `backend-cd` para todas las imágenes; `null` despliega la más reciente de cada repositorio, fijada por *digest* |
| `image_tag_overrides` | `{}` | Etiqueta por imagen que reemplaza a `image_tag` |
| `services_enabled` | `true` | `false` deja todos los servicios en 0 tareas |
| `nat_mode` | `"none"` | `none`, `single` o `per_az` |
| `rds_multi_az` | `false` | Réplica en espera de PostgreSQL |
| `redis_multi_az` | `false` | Réplica de Redis con *failover* automático |
| `cors_allow_origin` | `"*"` | Origen permitido de las rutas humanas |

## Procedimiento

```bash
export AWS_PROFILE=solventa          # cuenta 368083581678
cd infra/terraform/environments/staging/platform
terraform init
terraform apply -var ttl=2026-10-20

# Una vez, tras el primer apply: crea una base y un usuario por servicio.
eval "$(terraform output -raw db_init_command)"

# ... ventana ...

terraform destroy -var ttl=2026-10-20
```

La tarea `db-init` se explica en el README de [`production-demo/platform`](../../production-demo/platform/README.md#procedimiento).

## Costos (aproximados, `us-east-2`; verificar antes de aplicar)

Con los valores por defecto: 8 tareas (≈ US$ 0,11/h), sus 8 IPv4 públicas (≈ US$ 0,04/h), ALB (≈ US$ 0,023/h), RDS y Redis
(≈ US$ 0,016/h cada uno), caché del gateway (≈ US$ 0,02/h) y secretos: **≈ US$ 0,23 por hora** (≈ US$ 170 si quedara encendida un
mes). `nat_mode = "single"` suma ≈ US$ 0,045/h y quita las IPv4 de las tareas.
