# `bootstrap/cd-ecr` — base del despliegue continuo (publicación y despliegue)

Repositorios **ECR permanentes** y el **rol de IAM** que el workflow [`backend-cd`](../../../../.github/workflows/backend-cd.yml)
asume mediante OIDC para publicar en ellos, el **parámetro de SSM** con la etiqueta de la última versión de `main` y el **rol de
despliegue** con el que ese workflow hace `terraform apply` de `production-demo/platform` tras una aprobación manual.

Los repositorios se declaran con el módulo [`ecr`](../../modules/ecr/README.md). Antes de que existiera el módulo vivían
directamente aquí; los bloques `moved` de `main.tf` los trasladan en el estado, así que el `plan` siguiente al cambio solo debe
mostrar los traslados (`has moved to module.ecr...`), **sin destruir ni crear repositorios**. Revíselo antes de aplicar.

Se aplica una vez, a mano, por alguien con permisos de IAM y ECR en la cuenta `368083581678` (los del grupo `solventa-devs`
bastan; no hace falta ser administrador). **Aplicada el 2026-10-11**: 19 recursos creados y `terraform plan` sin diferencias.
Es permanente, como [`remote-state`](../remote-state/README.md): no es de una ventana de staging.

**Alcance:** publicación de imágenes (`develop` y `main`) y despliegue de `main` en `production-demo` (ver
[Cómo se despliega](#cómo-se-despliega-en-production-demo)). `staging` no se despliega desde el CD.

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
| Permisos extra del rol de publicación | `ssm:PutParameter` sobre ese parámetro; `ecs:DescribeClusters`, `ecs:ListServices` y `ecs:DescribeServices` (servicios de `solventa-demo`) | Registrar la etiqueta y saber si `production-demo` está encendido antes de pedir la aprobación |
| Rol de despliegue | `solventa-github-actions-deploy`; confianza solo para `repo:…@1325296709:environment:production-demo` | Solo lo asume el job `deploy`, que corre en el *Environment* `production-demo` de GitHub (aprobación manual); ninguna ejecución de rama ni PR |
| Permisos del rol de despliegue | `ReadOnlyAccess` (el `plan` refresca toda la plataforma); estado y *lock* de `production-demo/platform`; lectura del estado del pool permanente (`bootstrap/identity/terraform.tfstate`) y de los parámetros `/solventa/production-demo/*` de SSM, incluida la URL del portal (con `kms:Decrypt` solo vía SSM); `secretsmanager:GetSecretValue` sobre `solventa-demo/*`; registrar y desregistrar *task definitions*, etiquetarlas, `ecs:UpdateService` sobre los servicios de `solventa-demo` e `iam:PassRole` de los roles `solventa-demo-*` solo hacia `ecs-tasks` | Escribe únicamente lo que cambia una etiqueta nueva; además el workflow rechaza un plan que toque algo fuera de ECS |
| Estado | `bootstrap/cd-ecr/terraform.tfstate` en el bucket del estado remoto | Convención de claves de T4 |

Salidas: `role_arn`, `deploy_role_arn`, `image_tag_parameter`, `oidc_provider_arn` y `repository_urls`.

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
`solventa-github-actions-ecr` y `solventa-github-actions-deploy` no deben cambiar sin actualizar ese archivo. No hay secretos ni
variables que configurar en GitHub.

### Requisito en GitHub: el *Environment* `production-demo`

Un administrador del repositorio lo crea una vez en **Settings → Environments → New environment** con el nombre exacto
`production-demo`:

- **Required reviewers:** los miembros del equipo que pueden aprobar un despliegue.
- **Deployment branches and tags:** *Selected branches*, solo `main`.

Sin él, GitHub crea el *Environment* automáticamente sin revisores la primera vez que corre el job y el despliegue **no espera
aprobación**. Créelo antes de aplicar este bootstrap.

## Cómo se publica

1. Un *push* a `develop` o `main` que toque `services/`, `stubs/`, `pkg/` o el workspace de Go dispara `backend-cd`.
2. [`scripts/ci/changed-images.sh`](../../../../scripts/ci/changed-images.sh) decide qué imágenes reconstruir: la del directorio
   que cambió, o **todas** si cambió algo compartido (`pkg/`, `go.work`, `.dockerignore`, el propio CD). En `main` se
   reconstruyen **siempre todas**, para que el commit tenga todas sus etiquetas y se despliegue con un solo `image_tag`.
3. Cada imagen se construye con el contexto en la raíz y se publica como `solventa/<imagen>:<sha de 12 caracteres>`.
4. Si esa etiqueta ya existe (reejecución del mismo commit), no se vuelve a publicar.

`develop` y `main` publican en los mismos repositorios, con el SHA como única etiqueta: no hay etiquetas por ambiente. Por eso
"la imagen más reciente" puede ser de `develop`; la versión de `main` es la que indica el parámetro
`/solventa/production-demo/image-tag`. Cada composición `environments/*/platform` elige qué desplegar con `image_tag` /
`image_tag_overrides` (o la imagen más reciente, fijada por *digest*).

## Cómo se despliega en `production-demo`

Solo en `main`, después de publicar **todas** las imágenes del commit:

1. **`release`:** escribe el SHA de 12 caracteres en `/solventa/production-demo/image-tag` (siempre, aunque el ambiente esté
   apagado) y consulta con [`scripts/ci/demo-status.sh`](../../../../scripts/ci/demo-status.sh) si el ambiente está encendido:
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
