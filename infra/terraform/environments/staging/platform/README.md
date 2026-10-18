# Composición `staging/platform` — plataforma efímera de staging (T4)

Plataforma completa del módulo [`platform`](../../../modules/platform/README.md) en tamaño de experimento: los cinco servicios,
los stubs de proveedores y toda la infraestructura que necesitan. Como el resto de staging, **se levanta para una ventana y se
destruye en la misma sesión**. La topología está en el README del módulo; aquí solo cambian los valores.

> **Estado:** **aplicada y destruida el 2026-10-17** (16:29–16:50 hora de Colombia, 21:29–21:50 UTC, `us-east-2`, 248 recursos;
> estado remoto en el bucket de T4). Resultados en [Ventana del 2026-10-17](#ventana-del-2026-10-17). Cada ventana nueva exige la
> autorización del equipo (estimación de costo escrita y aprobada) y destruirla en la misma sesión.

## Valores del ambiente

| Parámetro | Valor | Por qué |
|---|---|---|
| Nombre / VPC | `solventa-stg`, `10.50.0.0/16` | No se cruza con `ha-l01` (`10.40.0.0/16`) |
| Tareas | 1 por servicio y por stub (`origination` 0,5 vCPU / 1 GB, el resto 0,25 vCPU / 0,5 GB); autoescalado por CPU al 60 % hasta 2, salvo `origination`, fija en 1 | Tamaño de experimento; `origination` en una tarea mientras aceptaciones y bandeja estén en memoria ([C5](../../../modules/platform/README.md#una-tarea-de-originación-c5)) |
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

## Ventana del 2026-10-17

Ventana corta de validación de T4 con la configuración mínima: sin NAT, Multi-AZ apagado, `db.t4g.micro` y `cache.t4g.micro`, una
tarea por servicio, RDS sin protección contra borrado y con `skip_final_snapshot = true`. Imágenes: la más reciente de cada
repositorio (`origination` `0f2fa894ccee`; el resto `d1f03ac786a2`).

| Paso | Resultado |
|---|---|
| `terraform plan` | 248 por crear, 0 cambios, 0 destrucciones; 0 NAT Gateways y 0 EIP; `desired_count = 1` en los 8 servicios |
| `terraform apply` | **248 creados en 8 min 43 s**, sin errores |
| `db-init` | Código de salida 0: cinco bases y cinco roles `<servicio>_app` creados. Repetida: código 0 (idempotente; solo actualiza la contraseña) |
| Aislamiento entre bases | `claims_app` entra a `claims`; a `origination` recibe `FATAL: permission denied for database "origination"` (`User does not have CONNECT privilege`) |
| Servicios ECS | Los 8 estables (`services-stable`), 1/1 tarea, despliegue `COMPLETED` |
| ALB | Los 5 target groups `healthy` con el health check de EXP-D03 |
| `GET /v1/risk-profiles/{id}` con API key de socio | `200` en 0,72 s, perfil desde `external_sources` (API Gateway → VPC link → ALB → `origination` → stubs por Cloud Map) |
| El mismo `GET` otra vez | `200` en 0,36 s, misma respuesta (caché del stage) |
| `POST /v1/risk-profiles/resolve` | `200` con `source: "cache"`: ElastiCache Redis responde |
| Sin API key | `403` |
| Ruta humana sin token / con token falsificado | `401` / `401` (authorizer de Cognito) |
| *Preflight* CORS | `200` |
| Logs de los servicios | Sin errores (el único es el rechazo provocado en la prueba de aislamiento) |
| `terraform destroy` | **248 destruidos en 9 min 34 s**, sin errores |
| `terraform state list` | Vacío |
| Revisión de la cuenta | 0 instancias y snapshots RDS, 0 ElastiCache, 0 secretos, 0 ALB, 0 VPC `10.50.0.0/16`, 0 EIP sueltas, 0 buckets, 0 API, 0 user pools, 0 grupos de logs. El clúster, los servicios y las task definitions quedan `INACTIVE` (sin costo); la API de etiquetas aún lista registros de recursos ya borrados |

Duración total: unos 21 minutos con recursos encendidos; costo estimado ≈ US$ 0,10.

No se probó en esta ventana: el *rate limiting* bajo carga (medido en [`ha-l01`](../ha-l01/README.md)), el inicio de sesión con un
usuario real de Cognito (medido en [`ha-s01`](../ha-s01/README.md)) ni los modos `single` / `per_az` del NAT y Multi-AZ.

## Costos (aproximados, `us-east-2`; verificar antes de aplicar)

Con los valores por defecto: 8 tareas (≈ US$ 0,11/h), sus 8 IPv4 públicas (≈ US$ 0,04/h), ALB (≈ US$ 0,023/h), RDS y Redis
(≈ US$ 0,016/h cada uno), caché del gateway (≈ US$ 0,02/h) y secretos: **≈ US$ 0,23 por hora** (≈ US$ 170 si quedara encendida un
mes). `nat_mode = "single"` suma ≈ US$ 0,045/h y quita las IPv4 de las tareas.
