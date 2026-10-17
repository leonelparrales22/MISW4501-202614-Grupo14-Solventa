# `bootstrap/cd-ecr` — base del despliegue continuo (publicación de imágenes)

Repositorios **ECR permanentes** y el **rol de IAM** que el workflow [`backend-cd`](../../../../.github/workflows/backend-cd.yml)
asume mediante OIDC para publicar en ellos. Es la primera mitad del CD: construir y publicar imágenes. Las composiciones
`environments/*/platform` (T4) leen estos repositorios y despliegan las imágenes en ECS con `terraform apply`.

Los repositorios se declaran con el módulo [`ecr`](../../modules/ecr/README.md). Antes de que existiera el módulo vivían
directamente aquí; los bloques `moved` de `main.tf` los trasladan en el estado, así que el `plan` siguiente al cambio solo debe
mostrar los traslados (`has moved to module.ecr...`), **sin destruir ni crear repositorios**. Revíselo antes de aplicar.

Se aplica una vez, a mano, por alguien con permisos de IAM y ECR en la cuenta `368083581678` (los del grupo `solventa-devs`
bastan; no hace falta ser administrador). **Aplicada el 2026-10-11**: 19 recursos creados y `terraform plan` sin diferencias.
Es permanente, como [`remote-state`](../remote-state/README.md): no es de una ventana de staging.

**Alcance:** solo publica imágenes. El despliegue lo hacen las composiciones `environments/*/platform`.

| Elemento | Configuración | Por qué |
|---|---|---|
| Repositorios | `solventa/<imagen>` para `audit`, `claims`, `notifications`, `origination`, `partners`, `kyc`, `opendata`, `openfinance` | Uno por Dockerfile existente en `services/` y `stubs/` |
| Etiquetas de imagen | `IMMUTABLE` | La etiqueta es el SHA del commit y nunca se sobrescribe: un SHA siempre es la misma imagen y el rollback es redesplegar una etiqueta anterior |
| Escaneo | `scan_on_push = true` | Vulnerabilidades visibles en cada publicación, sin costo adicional del escaneo básico |
| Ciclo de vida | Imágenes sin etiqueta a los 7 días; se conservan las 30 más recientes | Evita acumular almacenamiento (créditos gratuitos) |
| Proveedor OIDC | `token.actions.githubusercontent.com`, audiencia `sts.amazonaws.com` | GitHub Actions obtiene credenciales temporales; **no hay llaves de AWS guardadas en GitHub** |
| Rol | `solventa-github-actions-ecr` | Lo asume `backend-cd` |
| Confianza del rol | Igualdad exacta del claim `sub` con `repo:leonelparrales22@52500190/MISW4501-202614-Grupo14-Solventa@1325296709:ref:refs/heads/{develop,main}` | Solo ejecuciones sobre esas ramas; **nunca un PR**, ni un fork, ni otra rama. El repositorio usa *subject claims* inmutables, así que el `sub` lleva los IDs numéricos del dueño y del repo (sobreviven a un cambio de nombre y un repo nuevo con el mismo nombre no hereda el acceso) |
| Permisos del rol | `ecr:GetAuthorizationToken` (no admite recurso) y las acciones de *push* + `DescribeImages` + `BatchGetImage` (buildx lee manifiestos al publicar) solo sobre los 8 repositorios | Mínimo privilegio: no puede descargar capas, borrar imágenes ni tocar otros servicios |
| Estado | `bootstrap/cd-ecr/terraform.tfstate` en el bucket del estado remoto | Convención de claves de T4 |

Salidas: `role_arn`, `oidc_provider_arn` y `repository_urls`.

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

El ARN del rol (`role_arn`) está fijado en `backend-cd.yml` (`AWS_ROLE_ARN`); el nombre `solventa-github-actions-ecr` no debe
cambiar sin actualizar ese archivo. No hay secretos ni variables que configurar en GitHub.

## Cómo se publica

1. Un *push* a `develop` o `main` que toque `services/`, `stubs/`, `pkg/` o el workspace de Go dispara `backend-cd`.
2. [`scripts/ci/changed-images.sh`](../../../../scripts/ci/changed-images.sh) decide qué imágenes reconstruir: la del directorio
   que cambió, o **todas** si cambió algo compartido (`pkg/`, `go.work`, `.dockerignore`, el propio CD).
3. Cada imagen se construye con el contexto en la raíz y se publica como `solventa/<imagen>:<sha de 12 caracteres>`.
4. Si esa etiqueta ya existe (reejecución del mismo commit), no se vuelve a publicar.

`develop` y `main` publican en los mismos repositorios, con el SHA como única etiqueta: no hay etiquetas por ambiente. Cada
composición `environments/*/platform` elige qué desplegar con `image_tag` / `image_tag_overrides` (o la imagen más reciente,
fijada por *digest*).

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform validate
terraform test
```

Usan un proveedor simulado: no necesitan credenciales y no crean nada.

## Costos

Los repositorios vacíos no cuestan. El almacenamiento de ECR cuesta ≈ US$ 0,10 por GB al mes; ocho imágenes distroless de pocos
MB y la política de ciclo de vida lo mantienen muy por debajo de US$ 1 al mes.
