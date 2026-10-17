# Composición `production-demo/platform` — plataforma de la entrega evaluable (T4)

Plataforma completa del módulo [`platform`](../../../modules/platform/README.md) para la demo final: los cinco servicios, los
stubs de proveedores y toda la infraestructura que necesitan. Esta carpeta solo fija los valores del ambiente; la topología está
en el README del módulo.

> **Estado:** código validado con `terraform validate` y `terraform test` (proveedor simulado). **No se ha aplicado en AWS**; el
> mismo módulo `platform` se aplicó y destruyó en [`staging/platform`](../../staging/platform/README.md#ventana-del-2026-10-17) el
> 2026-10-17 sin errores. Aplicarla exige la autorización del equipo (estimación de costo escrita y aprobada).

## Valores del ambiente

| Parámetro | Valor | Por qué |
|---|---|---|
| Nombre / VPC | `solventa-demo`, `10.60.0.0/16` | Prefijo de 13 caracteres (límite de 32 de los target groups) |
| Tareas | ADR-02: 3 en `origination` (0,5 vCPU / 1 GB, EXP-L01), 2 en cada otro servicio (0,25 vCPU / 0,5 GB); autoescalado por CPU al 60 % hasta 6 y 4; 1 por stub | ADR-02 / EXP-D03 |
| NAT | `nat_mode = "none"` por defecto | Costo; `per_az` para la demo |
| RDS / Redis | Single-AZ por defecto, backups de 7 días | Multi-AZ en `db.t4g.micro` supera el presupuesto de US$ 20/mes si queda encendido; se activa para la demo |
| Protección | `deletion_protection = true` (RDS, ALB, user pool), snapshot final de RDS, secretos con 7 días de recuperación, buckets sin `force_destroy` | Entrega evaluable |
| Logs | 14 días | — |
| Usage plans | D-7: 100 req/s, ráfaga 200, sin cuota, por socio (`socio-a`, `socio-b`) | Decisión D-7 vigente |
| Estado remoto | `production-demo/platform/terraform.tfstate` en `solventa-tfstate-368083581678`, bloqueo nativo de S3 | Convención de T4 |

Etiquetas por defecto: `Project=solventa`, `Env=production-demo`, `Task=T4`. El proveedor rechaza cualquier cuenta distinta de
`368083581678`.

## Variables

| Variable | Defecto | Descripción |
|---|---|---|
| `image_tag` | `null` | SHA de 12 caracteres publicado por `backend-cd` para todas las imágenes. `null` despliega la imagen más reciente de cada repositorio, fijada por *digest* |
| `image_tag_overrides` | `{}` | Etiqueta por imagen que reemplaza a `image_tag` (el CD solo reconstruye las imágenes que cambiaron, así que un commit puede no tener todas las etiquetas) |
| `services_enabled` | `true` | `false` deja todos los servicios en 0 tareas sin destruir el resto |
| `nat_mode` | `"none"` | `none`, `single` o `per_az` |
| `rds_multi_az` | `false` | Réplica en espera de PostgreSQL en la segunda zona |
| `redis_multi_az` | `false` | Réplica de Redis en la segunda zona con *failover* automático |
| `cors_allow_origin` | `"*"` | Origen permitido de las rutas humanas; fijarlo al origen del portal |

Las etiquetas disponibles de una imagen salen de
`aws ecr describe-images --repository-name solventa/<imagen> --query 'sort_by(imageDetails,&imagePushedAt)[-1].imageTags'`.

## Procedimiento

```bash
export AWS_PROFILE=solventa          # cuenta 368083581678
cd infra/terraform/environments/production-demo/platform
terraform init
terraform plan -out plan.tfplan
terraform apply plan.tfplan

# Una vez, tras el primer apply: crea una base y un usuario por servicio.
eval "$(terraform output -raw db_init_command)"
```

La tarea `db-init` (imagen oficial `postgres:16-alpine`) lee la clave del administrador del secreto que gestiona RDS y la de cada
servicio de su secreto, crea el rol `<servicio>_app` y la base `<servicio>`, y le quita el `CONNECT` a `PUBLIC`: cada servicio
solo puede conectarse a su base. Es idempotente; vuelva a correrla si cambia una contraseña. Su salida queda en el grupo de logs
(`terraform output log_group`, prefijo `db-init`).

### Demo final: tolerancia a fallas

```bash
terraform apply -var nat_mode=per_az -var rds_multi_az=true -var redis_multi_az=true
# ... demo ...
terraform apply                       # vuelve a los valores por defecto
```

- **RDS:** pasar a Multi-AZ es una modificación en línea; RDS crea la réplica desde un snapshot y **no se pierden datos**
  (puede bajar el rendimiento mientras sincroniza, unos minutos). Volver a Single-AZ tampoco borra datos.
- **NAT:** con `per_az` las tareas pasan a las subredes privadas sin IP pública; ECS las reemplaza con un despliegue gradual.
- **Redis:** agrega una réplica en la otra zona con *failover* automático; el contenido es caché y se reconstruye.

### Destruir

`deletion_protection` está activo: hay que desactivarlo en `main.tf` (`deletion_protection = false`), aplicar y luego
`terraform destroy`. RDS deja el snapshot `solventa-demo-final`; bórrelo a mano si no se necesita (cobra almacenamiento).

## Costos (aproximados, precios de lista en `us-east-2`; verificar antes de aplicar)

| Elemento | Por hora | Por mes encendido |
|---|---|---|
| Fargate: 3 × 0,5 vCPU + 11 × 0,25 vCPU (servicios y stubs) | ≈ US$ 0,21 | ≈ US$ 153 |
| IPv4 públicas de las tareas (solo `nat_mode = none`, 14 tareas) | ≈ US$ 0,07 | ≈ US$ 50 |
| ALB interno | ≈ US$ 0,023 + LCU | ≈ US$ 17 |
| RDS `db.t4g.micro` (×2 en Multi-AZ) | ≈ US$ 0,016 | ≈ US$ 12 (24) |
| ElastiCache `cache.t4g.micro` (×2 en Multi-AZ) | ≈ US$ 0,016 | ≈ US$ 12 (24) |
| Caché del stage de API Gateway (0,5 GB, siempre activa en el módulo) | ≈ US$ 0,02 | ≈ US$ 15 |
| NAT Gateway (`per_az`: ×2) | ≈ US$ 0,045 c/u + datos | ≈ US$ 33 c/u |
| Secrets Manager (6 secretos) | — | ≈ US$ 2,4 |

**Encendida todo el mes cuesta del orden de US$ 260**, muy por encima del presupuesto de US$ 20/mes. Debe levantarse solo para la
demo (≈ US$ 0,36 por hora sin NAT ni Multi-AZ) y destruirse después. Con `services_enabled = false` quedan ≈ US$ 60/mes (ALB, RDS,
Redis, caché del gateway y secretos).

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform validate
terraform test
```
