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

Etiquetas por defecto: `Project=solventa`, `Env=staging`, `Task=T4`, `TTL=<var.ttl>`. `extra_tags` agrega otras (por ejemplo
`Owner` y `Story` para filtrar el costo de una ventana en Cost Explorer) y no puede reemplazar esas cuatro.

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
| `stub_desired_count_overrides` | `{}` | Tareas por stub; `{ payment-gateway = 0 }` tumba la pasarela y `{}` la restaura (HA-D02, D-6) |
| `origination_environment` | `{}` | Variables extra de `origination` para la ventana (p. ej. `RETRY_WINDOW_SECONDS`, `CHARGE_RETRY_MAX_MS`); no puede reemplazar las de la plataforma |
| `seed_users` | `{}` | Usuarios de prueba del user pool (asegurado, asesor, operaciones o socio) para obtener tokens reales |
| `seed_user_password` | `null` (sensible) | Contraseña permanente de los usuarios de prueba; **solo** por `TF_VAR_seed_user_password`, nunca en un archivo |
| `extra_tags` | `{}` | Etiquetas adicionales (`Owner`, `Story`…) |

[`ha-d02.example.tfvars`](ha-d02.example.tfvars) trae los valores de la ventana de HA-D02 (tres usuarios ficticios `@example.com`,
ventana de reintentos de 300 s, espera máxima de 15 s y `Owner=nicolas`, `Story=HA-D02`), sin contraseña ni etiquetas de imagen.

## Procedimiento

El primer `apply` va **en dos tiempos** (ver «Primer `apply` en dos tiempos» en el README del
[módulo](../../../modules/platform/README.md#primer-apply-en-dos-tiempos)): `origination` y `audit` abren PostgreSQL al arrancar y
necesitan que `db-init` haya creado su rol. `db-init` se lanza con el **comando literal** de `terraform output -raw db_init_command`,
copiado y ejecutado tal cual: nunca con `eval`, para que la mutación de AWS quede a la vista (y del *hook* del harness).

```bash
export AWS_PROFILE=solventa          # cuenta 368083581678
cd infra/terraform/environments/staging/platform
terraform init

# 1. Todo menos las tareas (P1).
terraform plan -out p1.tfplan -var ttl=2026-10-20 -var image_tag=<sha de 12> -var services_enabled=false
terraform apply p1.tfplan

# 2. Una vez: crea una base y un usuario por servicio. Imprime el comando y ejecútelo tal cual;
#    espere a que la tarea termine con código 0 («database <svc> ready» ×5 en el log, prefijo db-init).
terraform output -raw db_init_command

# 3. Las tareas (P2): solo cambian los desired_count y el autoescalado.
terraform plan -out p2.tfplan -var ttl=2026-10-20 -var image_tag=<sha de 12>
terraform apply p2.tfplan

# ... ventana ...

terraform destroy -auto-approve -input=false -var ttl=2026-10-20 -var image_tag=<sha de 12>
terraform state list                  # vacío
```

Una ventana con usuarios de prueba agrega `-var-file=ha-d02.example.tfvars` a los tres comandos de Terraform y pasa la contraseña en
el mismo comando con `TF_VAR_seed_user_password` (la ventana de HA-D02 la toma de la variable de usuario de Windows
`SOLVENTA_HA_D02_SEED_PASSWORD`, que se borra al terminar). La tarea `db-init` se explica en el README de
[`production-demo/platform`](../../production-demo/platform/README.md#procedimiento).

### Caída de la pasarela en una ventana (HA-D02)

```bash
# Tumbar solo la pasarela: el plan debe cambiar únicamente el desired_count de payment-gateway.
terraform plan -out down.tfplan <mismas variables> -var 'stub_desired_count_overrides={"payment-gateway"=0}'
terraform apply down.tfplan
# Restaurarla con un plan preparado de antemano, para aplicarlo en cuanto haga falta.
terraform plan -out restore.tfplan <mismas variables>
terraform apply restore.tfplan
```

Las aceptaciones, la bandeja y la emisión de `origination` viven en memoria (C5): cualquier `apply` que reinicie `origination` las
borra. Por eso la imagen va fijada por etiqueta y los `apply` de la caída solo tocan el servicio `payment-gateway`.

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

## Ventana de HA-D02 (AAAA-MM-DD)

Pendiente: se llena al cerrar la ventana de HA-D02 (paso C1 de su plan) con las horas, los recursos, el humo de
[`tests/resilience/ha-d02/staging`](../../../../../tests/resilience/ha-d02/README.md) y la verificación del `destroy`.

## Costos (aproximados, `us-east-2`; verificar antes de aplicar)

Con los valores por defecto: 10 tareas (`origination` 0,5 vCPU / 1 GB y 9 de 0,25 vCPU / 0,5 GB: los otros cuatro servicios y los
cinco stubs, ≈ US$ 0,136/h), sus 10 IPv4 públicas (≈ US$ 0,05/h), ALB (≈ US$ 0,023/h), RDS y Redis (≈ US$ 0,016–0,019/h cada uno),
caché del gateway (≈ US$ 0,02/h) y 7 secretos: **≈ US$ 0,27 por hora** (≈ US$ 195 si quedara encendida un mes). Con
`services_enabled = false` quedan ≈ US$ 0,08/h. `nat_mode = "single"` suma ≈ US$ 0,045/h y quita las IPv4 de las tareas.
