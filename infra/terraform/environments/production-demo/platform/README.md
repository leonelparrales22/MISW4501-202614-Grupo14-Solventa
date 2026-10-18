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
| Tareas | ADR-02: 2 en cada servicio (0,25 vCPU / 0,5 GB) con autoescalado por CPU al 60 % hasta 4; `origination` (0,5 vCPU / 1 GB, EXP-L01) **en 1 tarea sin autoescalado** por ahora; 1 por stub | ADR-02 / EXP-D03. `origination` se desvía temporalmente de las 3 tareas de ADR-02 mientras aceptaciones, bandeja y emisión estén en memoria ([C5](../../../modules/platform/README.md#una-tarea-de-originación-c5), HA-D02); vuelve a 3 (hasta 6) cuando estén en PostgreSQL |
| NAT | `nat_mode = "none"` por defecto | Costo; `per_az` para la demo |
| RDS / Redis | Single-AZ por defecto, backups de 7 días | Multi-AZ en `db.t4g.micro` supera el presupuesto de US$ 20/mes si queda encendido; se activa para la demo |
| Identidad | User pool permanente `solventa-users` de [`bootstrap/identity`](../../../bootstrap/identity/README.md), leído por `terraform_remote_state` (clave `bootstrap/identity/terraform.tfstate`) y pasado al módulo en `existing_cognito`; esta composición no crea pool | El portal y la app usan siempre el mismo pool y los mismos clientes |
| Protección | `deletion_protection = true` (RDS y ALB), snapshot final de RDS, secretos con 7 días de recuperación, buckets sin `force_destroy`; el user pool es el permanente de `bootstrap/identity`, con su propio candado | Entrega evaluable |
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

**Aplique siempre con `image_tag` explícito** (el SHA de 12 caracteres que publicó `backend-cd` desde `main`). Con `image_tag = null`
cada servicio toma la imagen más reciente de su repositorio, y en ECR pueden convivir imágenes de prueba de una ventana de staging
(por ejemplo `solventa/origination:prueba-ha-d02` y `solventa/email:prueba-ha-d02`, HA-D02) hasta que se borren.

## Procedimiento

Antes de todo, [`bootstrap/identity`](../../../bootstrap/identity/README.md) debe estar **aplicada**: esta composición lee su
estado (pool, emisor, JWKS y clientes) y sin él ni siquiera se puede planear.

El **primer** `apply` va en dos tiempos (ver «Primer `apply` en dos tiempos» en el README del módulo): `origination` (con el cobro
de la prima) y `audit` abren PostgreSQL al arrancar y necesitan que `db-init` haya creado su rol.

```bash
export AWS_PROFILE=solventa          # cuenta 368083581678
cd infra/terraform/environments/production-demo/platform
terraform init

# 1. Todo menos las tareas.
terraform plan -out plan.tfplan -var image_tag=<sha de 12> -var services_enabled=false
terraform apply plan.tfplan

# 2. Una vez: crea una base y un usuario por servicio. Copie y ejecute el comando literal que imprime la salida
#    (no use eval), y espere a que la tarea termine con código 0.
terraform output -raw db_init_command

# 3. Las tareas.
terraform plan -out plan.tfplan -var image_tag=<sha de 12>
terraform apply plan.tfplan
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
`terraform destroy`. RDS deja el snapshot `solventa-demo-final`; bórrelo a mano si no se necesita (cobra almacenamiento). El user
pool no pertenece a esta composición: `destroy` no lo toca.

## Costos (aproximados, precios de lista en `us-east-2`; verificar antes de aplicar)

| Elemento | Por hora | Por mes encendido |
|---|---|---|
| Fargate: 1 × 0,5 vCPU + 13 × 0,25 vCPU (servicios y stubs) | ≈ US$ 0,19 | ≈ US$ 135 |
| IPv4 públicas de las tareas (solo `nat_mode = none`, 14 tareas) | ≈ US$ 0,07 | ≈ US$ 50 |
| ALB interno | ≈ US$ 0,023 + LCU | ≈ US$ 17 |
| RDS `db.t4g.micro` (×2 en Multi-AZ) | ≈ US$ 0,016 | ≈ US$ 12 (24) |
| ElastiCache `cache.t4g.micro` (×2 en Multi-AZ) | ≈ US$ 0,016 | ≈ US$ 12 (24) |
| Caché del stage de API Gateway (0,5 GB, siempre activa en el módulo) | ≈ US$ 0,02 | ≈ US$ 15 |
| NAT Gateway (`per_az`: ×2) | ≈ US$ 0,045 c/u + datos | ≈ US$ 33 c/u |
| Secrets Manager (7 secretos: 5 bases, la clave de los enlaces del documento y la del administrador de RDS) | — | ≈ US$ 2,8 |

**Encendida todo el mes cuesta del orden de US$ 240**, muy por encima del presupuesto de US$ 20/mes. Debe levantarse solo para la
demo (≈ US$ 0,33 por hora sin NAT ni Multi-AZ) y destruirse después. Con `services_enabled = false` quedan ≈ US$ 60/mes (ALB, RDS,
Redis, caché del gateway y secretos).

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform validate
terraform test
```

Las pruebas reemplazan el estado remoto de `bootstrap/identity` con `override_data` (un pool simulado): no leen S3 ni necesitan
credenciales. Comprueban el tamaño de ADR-02, los interruptores de la demo final y que la plataforma usa el pool permanente.
