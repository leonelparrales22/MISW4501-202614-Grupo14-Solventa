# `bootstrap/cd-ecr` — base del despliegue continuo (publicación y despliegue)

Repositorios **ECR permanentes** y el **rol de IAM** que el workflow [`backend-cd`](../../../../.github/workflows/backend-cd.yml)
asume mediante OIDC para publicar en ellos, el **parámetro de SSM** con la etiqueta de la última versión de `main` y dos **roles de
despliegue**: el de `staging`, que usa el CD actual, y el de `production-demo`, que se conserva sin uso para cuando el CD vuelva a
desplegar ahí.

Los repositorios se declaran con el módulo [`ecr`](../../modules/ecr/README.md). Antes de que existiera el módulo vivían
directamente aquí; los bloques `moved` de `main.tf` los trasladan en el estado, así que el `plan` siguiente al cambio solo debe
mostrar los traslados (`has moved to module.ecr...`), **sin destruir ni crear repositorios**. Revíselo antes de aplicar.

Se aplica una vez, a mano, por alguien con permisos de IAM y ECR en la cuenta `368083581678` (los del grupo `solventa-devs`
bastan; no hace falta ser administrador). **Aplicada el 2026-10-11**: 19 recursos creados y `terraform plan` sin diferencias.
Es permanente, como [`remote-state`](../remote-state/README.md): no es de una ventana de staging.

**Alcance:** publicación de imágenes (`develop` y `main`) y despliegue en **`staging`**: automático con cada merge a `main` y
manual (*Run workflow*) desde `develop` (ver [Cómo se despliega](#cómo-se-despliega-en-staging)). `production-demo` no se despliega
desde el CD: lo enciende `scripts/environment/up.sh production-demo` con la etiqueta de `main` que el CD deja en SSM.

| Elemento | Configuración | Por qué |
|---|---|---|
| Repositorios | `solventa/<imagen>` para `audit`, `claims`, `notifications`, `origination`, `partners`, `kyc`, `opendata`, `openfinance`, `email` y `payment-gateway` (los dos últimos desde HU-W1.3-02, aplicados el 2026-10-18) | Uno por Dockerfile existente en `services/` y `stubs/`: 10 |
| Etiquetas de imagen | `IMMUTABLE` | La etiqueta es el SHA del commit y nunca se sobrescribe: un SHA siempre es la misma imagen y el rollback es redesplegar una etiqueta anterior |
| Escaneo | `scan_on_push = true` | Vulnerabilidades visibles en cada publicación, sin costo adicional del escaneo básico |
| Ciclo de vida | Imágenes sin etiqueta a los 7 días; se conservan las 30 más recientes | Evita acumular almacenamiento (créditos gratuitos) |
| Proveedor OIDC | `token.actions.githubusercontent.com`, audiencia `sts.amazonaws.com` | GitHub Actions obtiene credenciales temporales; **no hay llaves de AWS guardadas en GitHub** |
| Rol | `solventa-github-actions-ecr` | Lo asume `backend-cd` |
| Confianza del rol | Igualdad exacta del claim `sub` con `repo:leonelparrales22@52500190/MISW4501-202614-Grupo14-Solventa@1325296709:ref:refs/heads/{develop,main}` | Solo ejecuciones sobre esas ramas; **nunca un PR**, ni un fork, ni otra rama. El repositorio usa *subject claims* inmutables, así que el `sub` lleva los IDs numéricos del dueño y del repo (sobreviven a un cambio de nombre y un repo nuevo con el mismo nombre no hereda el acceso) |
| Permisos del rol | `ecr:GetAuthorizationToken` (no admite recurso) y las acciones de *push* + `DescribeImages` + `BatchGetImage` (buildx lee manifiestos al publicar) solo sobre los 10 repositorios | Mínimo privilegio: no puede descargar capas, borrar imágenes ni tocar otros servicios |
| Etiqueta de `main` | Parámetro SSM `/solventa/production-demo/image-tag` (`String`, valor inicial `none`, `ignore_changes` sobre el valor) | Terraform lo crea y el CD solo actualiza el valor; `scripts/environment/up.sh` lo lee para encender `production-demo` con la última versión de `main` |
| Permisos extra del rol de publicación | `ssm:PutParameter` sobre ese parámetro; `ecs:DescribeClusters`, `ecs:ListServices` y `ecs:DescribeServices` (servicios de `solventa-demo`) | Registrar la etiqueta; la consulta del cluster `solventa-demo` queda sin uso mientras el CD no despliegue en `production-demo` |
| Rol de despliegue en `staging` | `solventa-github-actions-deploy-staging`; confianza solo para `repo:…@1325296709:environment:staging` | Lo asume el job `deploy` actual, que corre en el *Environment* `staging` de GitHub; ninguna ejecución de rama ni PR |
| Permisos del rol de `staging` | Los mismos del rol de `production-demo` (más abajo), pero sobre `staging/platform`, `/solventa/staging/*`, `solventa-stg/*` y los servicios y roles `solventa-stg-*` | Escribe únicamente lo que cambia una etiqueta nueva |
| Rol de despliegue en `production-demo` (sin uso) | `solventa-github-actions-deploy`; confianza solo para `repo:…@1325296709:environment:production-demo` | Lo asumía el job `deploy` de #206, en el *Environment* `production-demo` de GitHub (aprobación manual); hoy ningún job lo usa |
| Permisos del rol de `production-demo` | `ReadOnlyAccess` (el `plan` refresca toda la plataforma); estado y *lock* de `production-demo/platform`; lectura del estado del pool permanente (`bootstrap/identity/terraform.tfstate`) y de los parámetros `/solventa/production-demo/*` de SSM, incluida la URL del portal (con `kms:Decrypt` solo vía SSM); `secretsmanager:GetSecretValue` sobre `solventa-demo/*`; registrar y desregistrar *task definitions*, etiquetarlas, `ecs:UpdateService` sobre los servicios de `solventa-demo` e `iam:PassRole` de los roles `solventa-demo-*` solo hacia `ecs-tasks` | Escribe únicamente lo que cambia una etiqueta nueva; además el workflow rechaza un plan que toque algo fuera de ECS |
| Estado | `bootstrap/cd-ecr/terraform.tfstate` en el bucket del estado remoto | Convención de claves de T4 |

Salidas: `role_arn`, `staging_deploy_role_arn`, `deploy_role_arn` (`production-demo`), `image_tag_parameter`, `oidc_provider_arn` y `repository_urls`.

## Primera aplicación (una sola vez)

Requiere Terraform 1.11 o superior y credenciales de AWS de la cuenta `368083581678`. Si tiene varios perfiles, seleccione el de
Solventa antes de empezar y compruebe la cuenta (`aws sts get-caller-identity` debe mostrar `368083581678`).

```bash
export AWS_PROFILE=solventa
cd infra/terraform/bootstrap/cd-ecr
terraform init
terraform plan
terraform apply
```

Una cuenta solo puede tener **un** proveedor OIDC por URL. Si `apply` falla con `EntityAlreadyExists`, la cuenta ya tiene el de
GitHub: aplique con `-var create_oidc_provider=false -var existing_oidc_provider_arn=<arn>`.

Los IDs (`github_owner_id`, `github_repository_id`) salen de `gh api repos/<dueño>/<repo> --jq '.owner.id, .id'`. Si alguien
desactiva los *subject claims* inmutables del repositorio (Settings → Actions → General), el `sub` vuelve al formato por nombre y
el rol deja de aceptar las ejecuciones: la opción actual se consulta con
`gh api repos/<dueño>/<repo>/actions/oidc/customization/sub`.

Los ARN de los roles están fijados en `backend-cd.yml` (`AWS_ROLE_ARN` y `AWS_DEPLOY_ROLE_ARN`); los nombres
`solventa-github-actions-ecr` y `solventa-github-actions-deploy-staging` no deben cambiar sin actualizar ese archivo. No hay secretos ni
variables que configurar en GitHub.

### Requisito en GitHub: el *Environment* `staging`

Un administrador del repositorio lo crea una vez en **Settings → Environments → New environment** con el nombre exacto `staging`:

- **Required reviewers:** ninguno (el despliegue de `main` es automático).
- **Deployment branches and tags:** *Selected branches*, solo `develop` y `main`.

El rol de `staging` confía en cualquier job del *Environment* `staging`, sin mirar la rama. La restricción de ramas es la que
impide que un workflow de otra rama o de un PR lo use. Si GitHub lo crea solo (la primera vez que corre el job), queda abierto a
todas las ramas: créelo antes de aplicar este bootstrap.

### Para cuando el CD vuelva a desplegar en `production-demo`: su *Environment*

Un administrador del repositorio lo crea una vez en **Settings → Environments → New environment** con el nombre exacto
`production-demo`:

- **Required reviewers:** los miembros del equipo que pueden aprobar un despliegue.
- **Deployment branches and tags:** *Selected branches*, solo `main`.

Sin él, GitHub crea el *Environment* automáticamente sin revisores la primera vez que corre el job y el despliegue **no espera
aprobación**. Créelo antes de que un job vuelva a usarlo.

## Cómo se publica

1. `backend-cd` corre con todo *push* a `main` (sin filtro de rutas, para que cada merge despliegue) y con *Run workflow*
   sobre `develop` o `main`. Un *push* a `develop` **no** lo dispara, y *Run workflow* sobre otra rama no hace nada.
2. Cada ejecución construye **las 10 imágenes** del commit
   ([`scripts/ci/changed-images.sh`](../../../../scripts/ci/changed-images.sh) sin base), para que tenga todas sus etiquetas y
   se despliegue con un solo `image_tag`.
3. Cada imagen se construye con el contexto en la raíz y se publica como `solventa/<imagen>:<sha de 12 caracteres>`.
4. Si esa etiqueta ya existe (reejecución del mismo commit), no se vuelve a publicar.

Como `develop` solo publica cuando alguien lanza el pipeline, `scripts/environment/up.sh staging` (que toma, por imagen, la
etiqueta más reciente de `develop`) puede encontrar imágenes viejas: lance *Run workflow* sobre `develop` antes de encender
`staging`, o justo después (con el ambiente encendido, además despliega).

`develop` y `main` publican en los mismos repositorios, con el SHA como única etiqueta: no hay etiquetas por ambiente. Por eso
"la imagen más reciente" puede ser de `develop`; la versión de `main` es la que indica el parámetro
`/solventa/production-demo/image-tag`. Cada composición `environments/*/platform` elige qué desplegar con `image_tag` /
`image_tag_overrides` (o la imagen más reciente, fijada por *digest*).

## Cómo se despliega en `staging`

| Disparo | Qué despliega | Aprobación |
|---|---|---|
| Merge a `main` | El commit del merge | Ninguna: automático |
| *Run workflow* sobre `develop` (Actions → backend-cd → Run workflow → *Branch: develop*) | El último commit de `develop` | Ninguna: lo lanza una persona |
| *Push* a `develop` | Nada: el pipeline no corre | — |

Después de publicar **todas** las imágenes del commit:

1. **`release`** (solo `main`): escribe el SHA de 12 caracteres en `/solventa/production-demo/image-tag`, que
   `up.sh production-demo` usa para encender la demo con la última versión de `main`.
2. **`deploy`** (*Environment* `staging`) consulta con [`scripts/ci/environment-status.sh`](../../../../scripts/ci/environment-status.sh)
   si `staging` está encendido: el cluster `solventa-stg` existe **y todos sus servicios tienen tareas**.
3. **Ambiente apagado** (lo normal, por costo): termina en verde con un aviso. No hace `apply`, porque crearía el ambiente
   completo. También cuenta como apagado un cluster con algún servicio en 0 tareas: `up.sh` los deja así mientras prepara la base
   de datos, y una ventana de prueba puede bajar un stub a 0 (`stub_desired_count_overrides`). `scripts/environment/up.sh staging`
   (desde la terminal de una persona) enciende el ambiente; después se vuelve a lanzar el CD o se espera el siguiente merge.
4. **Ambiente encendido:** [`scripts/ci/staging-vars.sh`](../../../../scripts/ci/staging-vars.sh) lee de las etiquetas del
   cluster el `ttl` y los `extra_tags` con los que `up.sh` lo encendió, y el job corre
   `terraform plan -var image_tag=<sha>` en `staging/platform` con esos valores. Sin ellos el plan cambiaría las etiquetas de
   todos los recursos.
5. [`scripts/ci/check-deploy-plan.sh`](../../../../scripts/ci/check-deploy-plan.sh) rechaza el plan si cambia algo que no sea
   una *task definition* o un servicio de ECS, o si un servicio pasa de 0 tareas a alguna. En ese caso el job falla sin aplicar.
   El CD aplica los valores por defecto de `staging/platform/variables.tf` para todo lo demás, así que las palancas de una
   ventana (`origination_environment`, por ejemplo) volverían a su valor por defecto: **no lance el CD durante una ventana de
   prueba**.
6. `terraform apply` del plan revisado y [`scripts/ci/verify-ecs-deploy.sh`](../../../../scripts/ci/verify-ecs-deploy.sh):
   espera a que todos los servicios estén estables y comprueba que cada uno corra la etiqueta nueva. Si el *circuit breaker* de
   ECS revierte un servicio a la versión anterior, el job falla.

Los despliegues van de a uno; si llegan dos seguidos, el más nuevo reemplaza al que esperaba turno. No lance el CD mientras
alguien corre `up.sh staging` o `down.sh staging` (el *lock* de Terraform frenaría a uno de los dos).

## Despliegue en `production-demo` (sin uso por ahora)

El CD desplegó en `production-demo` con aprobación manual (#206) hasta que el equipo decidió desplegar solo en `staging`. El rol
`solventa-github-actions-deploy` y su política siguen en AWS para volver a hacerlo: bastaría con un job como el de `staging` que
use ese rol, el *Environment* `production-demo` (con revisores, solo `main`), el cluster `solventa-demo` y
`production-demo/platform` con sus valores por defecto. El flujo de entonces, como referencia:

Solo en `main`, después de publicar **todas** las imágenes del commit:

1. **`release`:** escribe el SHA de 12 caracteres en `/solventa/production-demo/image-tag` (siempre, aunque el ambiente esté
   apagado) y consultaba con [`scripts/ci/environment-status.sh`](../../../../scripts/ci/environment-status.sh) si el ambiente está encendido:
   el cluster `solventa-demo` existe **y todos sus servicios tienen tareas**.
2. **Ambiente apagado** (lo normal, por costo): el workflow termina en verde con un aviso. No hace `apply`, porque crearía el
   ambiente completo. También cuenta como apagado un cluster con servicios en 0 tareas, que es lo que deja `up.sh` mientras
   prepara la base de datos: un despliegue los encendería antes de tiempo. `scripts/environment/up.sh production-demo`
   despliega esa etiqueta cuando se encienda.
3. **Ambiente encendido:** el job `deploy` espera la aprobación de un revisor del *Environment* `production-demo`. **No apruebe
   mientras alguien corre `up.sh` o `down.sh`** (el *lock* de Terraform frenaría a uno de los dos, pero es mejor no chocar), ni en
   plena demo: `origination` guarda aceptaciones y bandeja en memoria y un despliegue las borra.
4. Ya aprobado, vuelve a comprobar que el ambiente siga encendido y corre `terraform plan -var image_tag=<sha>` en
   `production-demo/platform`, con los valores por defecto versionados en `variables.tf` (los mismos que aplica `up.sh`).
5. [`scripts/ci/check-deploy-plan.sh`](../../../../scripts/ci/check-deploy-plan.sh) rechaza el plan si cambia algo que no sea
   una *task definition* o un servicio de ECS (por ejemplo, un `nat_mode` cambiado en un PR o un ambiente a medio encender), o
   si un servicio pasa de 0 tareas a alguna. En ese caso el job falla sin aplicar y hay que aplicar a mano después de revisar el
   plan. Como el CD aplica los valores por defecto de `production-demo/platform/variables.tf`, `nat_mode`, `rds_multi_az` y
   `redis_multi_az` se cambian ahí, en un PR, y no con `-var`.
6. `terraform apply` del plan revisado y [`scripts/ci/verify-ecs-deploy.sh`](../../../../scripts/ci/verify-ecs-deploy.sh):
   espera a que todos los servicios estén estables y comprueba que cada uno corra la etiqueta nueva. Si el *circuit breaker* de
   ECS revierte un servicio a la versión anterior, el job falla.

Un despliegue pendiente de aprobación no frena la publicación de los commits siguientes. Si llega un merge nuevo a `main`, su
despliegue reemplaza al que seguía esperando.

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform validate
terraform test
```

Usan un proveedor simulado: no necesitan credenciales y no crean nada.

## Costos

Los repositorios vacíos no cuestan. El almacenamiento de ECR cuesta ≈ US$ 0,10 por GB al mes; diez imágenes distroless de pocos
MB y la política de ciclo de vida lo mantienen muy por debajo de US$ 1 al mes.
