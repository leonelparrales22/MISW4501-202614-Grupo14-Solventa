# Despliegue en AWS

Guía paso a paso para desplegar Solventa en AWS con Terraform, en cada ambiente:

| Ambiente | Composición | Para qué | Vida |
|---|---|---|---|
| **staging** | [`infra/terraform/environments/staging/platform`](infra/terraform/environments/staging/platform/README.md) | Experimentos y pruebas | Se levanta para una ventana y **se destruye en la misma sesión** |
| **production-demo** | [`infra/terraform/environments/production-demo/platform`](infra/terraform/environments/production-demo/platform/README.md) | Entrega evaluable y demo final | Se levanta para la demo y se destruye después |

Las dos crean la plataforma completa del módulo [`platform`](infra/terraform/modules/platform/README.md): VPC en dos zonas, ALB
interno, ECS Fargate con los cinco servicios y los stubs, RDS PostgreSQL (una base y un usuario por servicio), ElastiCache Redis,
SNS/SQS con DLQ, S3, Secrets Manager, Cognito y API Gateway con *usage plans* por socio (decisión D-7).

> **Antes de aplicar cualquier ambiente:** cuesta dinero mientras exista (ver [Costos](#7-costos)). Toda ventana necesita la
> autorización del equipo con una estimación de costo escrita, y hay que destruirla al terminar.

---

## 1. Requisitos (una vez por equipo)

| Herramienta | Versión | Verificar |
|---|---|---|
| Terraform | **1.11 o superior** (el bloqueo nativo de S3 lo exige) | `terraform version` |
| AWS CLI | v2 | `aws --version` |
| Credenciales de la cuenta **368083581678**, región **us-east-2** | — | `aws sts get-caller-identity` |

Si tiene varios perfiles de AWS, seleccione el de Solventa en cada terminal. Los proveedores de Terraform rechazan cualquier otra
cuenta (`allowed_account_ids`), pero conviene comprobarlo antes:

```bash
export AWS_PROFILE=solventa          # PowerShell: $env:AWS_PROFILE = "solventa"
export AWS_REGION=us-east-2          # PowerShell: $env:AWS_REGION = "us-east-2"
aws sts get-caller-identity --query Account --output text   # debe imprimir 368083581678
```

Todos los comandos de esta guía se escriben desde la **raíz del repositorio**.

---

## 2. Lo que ya existe y no se vuelve a crear

| Pieza | Composición | Estado |
|---|---|---|
| Bucket del estado remoto `solventa-tfstate-368083581678` | [`bootstrap/remote-state`](infra/terraform/bootstrap/remote-state/README.md) | Aplicado; permanente |
| Repositorios ECR `solventa/*` y rol OIDC del CD | [`bootstrap/cd-ecr`](infra/terraform/bootstrap/cd-ecr/README.md) | Aplicado el 2026-10-11; permanente |

**Una sola vez, tras incorporar los módulos de T4:** los repositorios ECR pasaron al módulo `modules/ecr`. Hay que aplicar el
bootstrap para trasladarlos en el estado, **sin** recrearlos:

```bash
terraform -chdir=infra/terraform/bootstrap/cd-ecr init
terraform -chdir=infra/terraform/bootstrap/cd-ecr plan
```

El plan solo debe mostrar líneas `has moved to module.ecr...` y `0 to add, 0 to change, 0 to destroy`. **Si propone destruir o
crear un repositorio, deténgase y avise al equipo.** Si todo cuadra:

```bash
terraform -chdir=infra/terraform/bootstrap/cd-ecr apply
```

---

## 3. Las imágenes

No se construyen a mano. El workflow `backend-cd` publica en ECR cada vez que hay un *push* a `develop` o `main` que toca
`services/`, `stubs/` o `pkg/`, con la etiqueta `solventa/<imagen>:<SHA de 12 caracteres>`. Solo reconstruye las imágenes que
cambiaron, así que **un mismo commit no siempre tiene las ocho etiquetas**.

Hay dos formas de elegir qué se despliega:

| Opción | Cuándo | Cómo |
|---|---|---|
| La imagen más reciente de cada repositorio | Lo normal | No pase `image_tag` (queda `null`); cada imagen se fija por *digest* |
| Una etiqueta concreta | Desplegar o volver a una versión exacta | `-var image_tag=<sha>` y, para las imágenes que no se reconstruyeron en ese commit, `-var 'image_tag_overrides={kyc="<otro sha>"}'` |

Ver la etiqueta más reciente de una imagen:

```bash
aws ecr describe-images --repository-name solventa/origination \
  --query 'sort_by(imageDetails,&imagePushedAt)[-1].imageTags' --output text
```

---

## 4. Staging

Variable obligatoria: `ttl`, la fecha límite (`AAAA-MM-DD`) en que todo debe estar destruido; queda como etiqueta `TTL` en cada
recurso. En los ejemplos se usa `2026-10-20`; **use la fecha de su ventana y repítala en cada comando** (`plan`, `apply`, `destroy`).

### 4.1 Levantar

```bash
cd infra/terraform/environments/staging/platform
terraform init
terraform plan -var ttl=2026-10-20 -out staging.tfplan
terraform apply staging.tfplan
```

Tarda unos 15 minutos (RDS y ElastiCache son lo más lento).

### 4.2 Crear las bases y los usuarios (solo la primera vez)

La tarea `db-init` crea, para cada servicio, la base `<servicio>` y el usuario `<servicio>_app`, y le quita a `PUBLIC` el permiso
de conexión, así que cada servicio solo entra a su base:

```bash
eval "$(terraform output -raw db_init_command)"
# PowerShell: Invoke-Expression (terraform output -raw db_init_command)
```

Revise su salida a los 1-2 minutos. Debe terminar con `database <servicio> ready, owned by <servicio>_app` para los cinco:

```bash
aws logs tail "$(terraform output -raw log_group)" --log-stream-name-prefix db-init --since 15m
```

Es idempotente: si falla o cambia una contraseña, se vuelve a correr.

### 4.3 Verificar

```bash
CLUSTER="$(terraform output -raw ecs_cluster)"

# 1. Los servicios quedaron estables (espera hasta ~10 minutos)
aws ecs wait services-stable --cluster "$CLUSTER" \
  --services origination claims partners audit notifications openfinance opendata kyc

# 2. Tareas en ejecución por servicio
aws ecs describe-services --cluster "$CLUSTER" --services origination claims partners audit notifications \
  --query 'services[].[serviceName,runningCount,desiredCount]' --output table

# 3. Una petición de socio a través del API Gateway (usage plan y API key de socio-a)
terraform output -json api_key_values          # copie el valor de "socio-a"
curl -i -H "x-api-key: <valor de socio-a>" \
  "$(terraform output -raw invoke_url)/v1/risk-profiles/cliente-123?consent_active=true"

# 4. Logs de los servicios
aws logs tail "$(terraform output -raw log_group)" --follow
```

El ALB es interno: no responde desde internet, solo a través del API Gateway.

### 4.4 Destruir (al terminar la ventana, en la misma sesión)

```bash
terraform destroy -var ttl=2026-10-20
```

Staging es efímero: no deja snapshot de RDS, vacía los buckets y borra los secretos sin ventana de recuperación.

---

## 5. Production-demo

Sin `ttl`. Tiene protección contra borrado (RDS, ALB y user pool de Cognito), snapshot final de RDS, 7 días de backups y el
tamaño de ADR-02: 3 tareas en `origination` y 2 en cada otro servicio.

### 5.1 Levantar

```bash
cd infra/terraform/environments/production-demo/platform
terraform init
terraform plan -out demo.tfplan
terraform apply demo.tfplan
```

Después, igual que en staging: [crear las bases](#42-crear-las-bases-y-los-usuarios-solo-la-primera-vez) con
`db_init_command` y [verificar](#43-verificar) (el clúster se llama `solventa-demo`).

### 5.2 Activar la tolerancia a fallas para la demo final

Por costo, NAT y Multi-AZ están **apagados por defecto**. Para la demo:

```bash
terraform apply -var nat_mode=per_az -var rds_multi_az=true -var redis_multi_az=true
```

| Variable | Efecto | ¿Se pierden datos? |
|---|---|---|
| `nat_mode=per_az` | Un NAT Gateway por zona; las tareas pasan a subredes privadas sin IP pública (ECS las reemplaza gradualmente) | No |
| `rds_multi_az=true` | Réplica en espera de PostgreSQL en la otra zona con *failover* automático. Modificación en línea; el rendimiento puede bajar unos minutos mientras sincroniza | **No** |
| `redis_multi_az=true` | Réplica de Redis en la otra zona con *failover* automático | No (es caché) |

Al terminar la demo, vuelva a los valores por defecto:

```bash
terraform apply
```

`nat_mode` también admite `single` (un solo NAT compartido, más barato pero sin tolerancia a la caída de esa zona).

### 5.3 Pausar sin destruir

Deja la base, la caché y el gateway, con todos los servicios en 0 tareas:

```bash
terraform apply -var services_enabled=false
```

Aun así cuesta unos US$ 60 al mes; si no se va a usar en días, es mejor destruir.

### 5.4 Destruir

1. En `infra/terraform/environments/production-demo/platform/main.tf` cambie `deletion_protection = true` por `false`.
2. Aplique ese cambio y destruya:

   ```bash
   terraform apply
   terraform destroy
   ```

3. RDS deja el snapshot `solventa-demo-final`. Si no se necesita, bórrelo (cobra almacenamiento):

   ```bash
   aws rds delete-db-snapshot --db-snapshot-identifier solventa-demo-final
   ```

4. Vuelva a dejar `deletion_protection = true` en `main.tf` (no lo suba en `false`).

---

## 6. Actualizar o revertir una versión

Con el ambiente arriba, un `apply` con otra etiqueta despliega la nueva versión. ECS reemplaza las tareas gradualmente, y el ALB
deja 30 s a cada tarea que sale para terminar sus peticiones:

```bash
terraform apply -var image_tag=<sha nuevo>            # staging: agregue -var ttl=...
```

- Sin `image_tag`, un `apply` toma la imagen más reciente de cada repositorio.
- **Rollback:** el mismo comando con el SHA anterior. Las etiquetas son inmutables, así que un SHA siempre es la misma imagen.
- Si las tareas nuevas no pasan el *health check*, el *circuit breaker* de ECS vuelve solo a la versión anterior.

---

## 7. Costos

Aproximados, con precios de lista en `us-east-2`; verifíquelos antes de pedir la autorización.

| Ambiente y configuración | Por hora | Si quedara encendido un mes |
|---|---|---|
| staging (defecto) | ≈ US$ 0,23 | ≈ US$ 170 |
| production-demo (defecto: sin NAT ni Multi-AZ) | ≈ US$ 0,36 | ≈ US$ 260 |
| + `nat_mode=per_az` | + ≈ US$ 0,09 (y desaparecen ≈ US$ 0,07 de IPv4 públicas) | + ≈ US$ 66 |
| + `rds_multi_az` y `redis_multi_az` | + ≈ US$ 0,03 | + ≈ US$ 24 |
| production-demo con `services_enabled=false` | ≈ US$ 0,08 | ≈ US$ 60 |

El presupuesto del proyecto es de unos US$ 20 al mes: **ningún ambiente puede quedarse encendido**. Se levanta, se usa y se
destruye. El desglose está en el README de cada composición.

---

## 8. Problemas frecuentes

| Síntoma | Causa | Qué hacer |
|---|---|---|
| `the cached package ... does not match any of the checksums recorded in the dependency lock file` | Al `.terraform.lock.hcl` le falta el hash de su sistema operativo | `terraform providers lock -platform=windows_amd64 -platform=linux_amd64 -platform=darwin_amd64 -platform=darwin_arm64` en esa carpeta y suba el lock actualizado |
| `Error acquiring the state lock` | Otra persona está aplicando, o una ejecución se cortó | Confirme con el equipo que nadie está aplicando y luego `terraform force-unlock <ID del error>` |
| `A secret with this name is already scheduled for deletion` (production-demo) | Se destruyó hace menos de 7 días y el secreto sigue en su ventana de recuperación | Restaurarlo: `aws secretsmanager restore-secret --secret-id solventa-demo/<servicio>/database`, o borrarlo ya: `aws secretsmanager delete-secret --secret-id ... --force-delete-without-recovery` |
| `destroy` falla por `deletion protection` | Protección activa en production-demo | [Paso 5.4](#54-destruir) |
| `DBSnapshotAlreadyExists: solventa-demo-final` | Quedó el snapshot de un destroy anterior | Bórrelo (paso 5.4.3) o renómbrelo antes de destruir |
| Un servicio se queda en 0 tareas en ejecución | La imagen o la etiqueta no existe, o el contenedor falla al arrancar | `aws ecs describe-services --cluster <clúster> --services <servicio> --query 'services[0].events[:5]'` y los logs del servicio |
| `CannotPullContainerError` | Etiqueta inexistente en ECR | Revise la etiqueta ([sección 3](#3-las-imágenes)) o despliegue sin `image_tag` |
| La API responde `403 Forbidden` | Falta el encabezado `x-api-key` o la clave no es de un socio | `terraform output -json api_key_values` |
| La API responde `429 Too Many Requests` | El socio superó su usage plan (100 req/s, ráfaga 200, D-7) | Esperado; cada socio tiene su límite |

---

## Referencias

- [`infra/README.md`](infra/README.md): estado de la infraestructura y convenciones del estado remoto.
- [`infra/terraform/modules/platform/README.md`](infra/terraform/modules/platform/README.md): topología, rutas, variables y pendientes conocidos.
- READMEs de [staging](infra/terraform/environments/staging/platform/README.md) y de
  [production-demo](infra/terraform/environments/production-demo/platform/README.md): valores de cada ambiente y costos detallados.
- [`docs/runbooks/README.md`](docs/runbooks/README.md): otros procedimientos operativos (aprovisionar usuarios de Cognito, etc.).
