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
| Protección | `deletion_protection = true` (RDS, ALB, user pool), snapshot final de RDS, secretos con 7 días de recuperación, buckets sin `force_destroy`. Desde T5 son las variables `deletion_protection` y `ephemeral`, con esos mismos valores por defecto: solo el comando de apagado las invierte (D-18) | Entrega evaluable |
| Logs | 14 días | — |
| Usage plans | D-7: 100 req/s, ráfaga 200, sin cuota, por socio (`socio-a`, `socio-b`); `portal-embebido` (10 req/s, ráfaga 20), el socio de demostración cuya key agrega CloudFront (T5, D-3) | Decisión D-7 vigente |
| Portal web (T5) | La URL del portal sale del parámetro SSM `/solventa/production-demo/web/url` que publica [`production-demo/web`](../web/README.md); con ella, los enlaces de los correos apuntan al portal (D-11) y CORS se fija a su origen (D-12 b) | Mismo origen para el portal; sin el sitio, todo queda como antes |
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
| `cors_allow_origin` | `null` | Origen permitido de las rutas humanas. `null` lo fija al origen del portal cuando existe y, si no, `*` (T5, D-12 b) |
| `portal_base_url` | `null` | Origen del portal; `null` lo lee del parámetro SSM `/solventa/production-demo/web/url` (T5) |
| `deletion_protection` | `true` | Candado de RDS y del ALB; solo `down.sh` lo pone en `false` (T5, D-18) |
| `ephemeral` | `false` | Sin snapshot final, buckets con `force_destroy` y secretos sin ventana de recuperación; solo `down.sh` lo pone en `true` (T5, D-18) |

**Aplique siempre con `image_tag` explícito.** La etiqueta de production-demo es el SHA del *merge* a `main`: el CD de `main` (pipeline
de Maycol) construye todas las imágenes con ese SHA y lo publica en el parámetro SSM **`/solventa/production-demo/image-tag`**. Ese
parámetro es de ese pipeline: T5 solo lo lee (decisión #69 de Nicolás, acordada con Maycol). Con `image_tag = null` cada servicio
tomaría la imagen más reciente de su repositorio, y en ECR pueden convivir imágenes de prueba de una ventana de staging (por ejemplo
`solventa/origination:prueba-ha-d02`), así que nunca se aplica production-demo sin etiqueta.

```bash
MSYS_NO_PATHCONV=1 aws ssm get-parameter --name /solventa/production-demo/image-tag --query Parameter.Value --output text
```

## Encender y apagar con un comando (T5)

`scripts/environment/up.sh` y `down.sh` (documentados en [`scripts/README.md`](../../../../../scripts/README.md)) encienden y
apagan el ambiente completo en el orden correcto (D-16). **El harness de agentes nunca los corre con production-demo** (R8): los
corre una persona desde su terminal, con credenciales de la cuenta `368083581678`, unas dos horas antes de la demo (D-17).

```
Prerrequisitos (una vez): bootstrap/identity aplicado (pool permanente) · develop → main integrado en backend y front ·
  el CD de main publicó las imágenes y el parámetro /solventa/production-demo/image-tag con el SHA (no "none") · un checkout limpio del front
  en origin/main (FRONT_DIR), con Node 24 y npm, si se va a publicar el front con el comando
Encender:
  FRONT_DIR=<checkout del front> scripts/environment/up.sh production-demo --confirm-production-demo          (planifica y se detiene)
  FRONT_DIR=<checkout del front> scripts/environment/up.sh production-demo --confirm-production-demo --yes    (aplica)
      etiqueta del parámetro SSM → plataforma sin tareas → db-init → sitio (production-demo/web) →
      plataforma con tareas y la URL del portal → build local de main + publicar-portal.sh → humo
  (con --skip-front, el front lo publica el pipeline de main; el humo se corre después de que publique)
Apagar (al terminar la demo, el mismo día):
  scripts/environment/down.sh production-demo --confirm-production-demo --yes
      deletion_protection=false + ephemeral=true → destroy de la plataforma; quedan el sitio y el pool (≈ US$ 0/h)
```

- **Variables que pasan los comandos:** en production-demo solo `image_tag` (del parámetro SSM) y `services_enabled` por fase, y en
  `down.sh` además `deletion_protection=false` y `ephemeral=true`. **Nunca** `nat_mode`, `rds_multi_az` ni `redis_multi_az`: quedan los
  valores por defecto de esta composición, los mismos que usa el CD de `main` (#69).
- **Sin etiqueta no se enciende:** el parámetro lo crea el Terraform del CD de `main` con el valor inicial **`none`** (SSM no admite un
  valor vacío). Con `none`, vacío o sin parámetro, `up.sh` se detiene con «falta correr el CD de main»; con cualquier valor que no sea un
  SHA de 12 caracteres hexadecimales, se detiene con error. No busca «la más reciente».
- **Coordinación con el CD de `main` (pipeline de Maycol, #68 y #69):** ese pipeline construye y publica las imágenes y la etiqueta
  **siempre**; solo aplica Terraform si el ambiente está encendido; y su aprobación **no se da mientras corre `up.sh` o `down.sh`** (los
  dos aplicarían el mismo estado; el bloqueo nativo del estado remoto impide dos `apply` a la vez, pero el segundo fallaría a medias).
- Las tareas de `origination` arrancan una sola vez, ya con la URL del portal (la fase `site` va antes de `platform`). Con una sola tarea
  (C5), ningún `apply` que la redespliegue durante la demo.
- **Volver a encender antes de 7 días no choca:** `down.sh` borra los secretos sin ventana de recuperación (`ephemeral = true`). Puede
  haber unos segundos de consistencia eventual; si `up.sh` falla en `platform-base` por eso, se repite la fase.

## Procedimiento manual (referencia)

Es lo que hacen por dentro las fases `platform-base`, `db-init` y `platform` de `up.sh`. El **primer** `apply` va en dos tiempos (ver «Primer `apply` en dos tiempos» en el README del módulo): `origination` (con el cobro
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

Lo hace `down.sh production-demo` (ver «Encender y apagar con un comando»). A mano, con las variables de D-18:

```bash
terraform plan -out off.tfplan -var image_tag=<sha de 12> -var services_enabled=false -var deletion_protection=false -var ephemeral=true
terraform apply off.tfplan            # baja los candados y pone el modo efímero (sin llamadas de borrado)
terraform destroy -var image_tag=<sha de 12> -var services_enabled=false -var deletion_protection=false -var ephemeral=true
```

Con `ephemeral = true` RDS no deja snapshot final, los buckets se borran con sus objetos y los secretos se borran sin ventana de
recuperación. Sin esas variables, RDS deja el snapshot `solventa-demo-final` (cobra almacenamiento) y los secretos quedan 7 días en
borrado programado.

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
