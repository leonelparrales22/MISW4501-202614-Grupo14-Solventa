# `bootstrap/remote-state` — estado remoto de Terraform (T4)

Bucket S3 **`solventa-tfstate-368083581678`** (`us-east-2`, cuenta `368083581678`) donde todas las composiciones de Terraform
de Solventa guardan su estado, con el **bloqueo nativo de S3** (`use_lockfile = true`, sin tabla DynamoDB). Es la parte
«estado remoto de Terraform en S3 con bloqueo» de la tarea técnica T4 del plan de sprints (base técnica del Sprint 1). Las
«variables por ambiente» y los demás módulos base de T4 no están aquí.

Con el estado en un bucket compartido, cualquier integrante del equipo puede planear y aplicar una composición desde su
máquina, el estado no se pierde con un portátil y dos operaciones simultáneas sobre la misma composición no se pisan: la
segunda espera o falla con «Error acquiring the state lock».

| Elemento | Configuración | Por qué |
|---|---|---|
| Bucket | `solventa-tfstate-368083581678`, `force_destroy = false`, `prevent_destroy = true` | El ID de la cuenta hace único el nombre; guarda el estado de todas las composiciones y no se puede borrar |
| Versionado | `Enabled` | Recuperar un estado anterior tras una escritura errónea o un borrado |
| Cifrado | SSE-S3 (`AES256`), sin KMS | Cifrado en reposo sin costo (una llave KMS propia cuesta US$ 1 al mes) |
| Acceso público | Las cuatro opciones del bloqueo de acceso público en `true` | El bucket nunca puede quedar público |
| Propiedad de objetos | `BucketOwnerEnforced` (ACL desactivadas) | El dueño del bucket es dueño de todos los objetos; el acceso se controla solo con IAM y la política |
| Política del bucket | `DenyInsecureTransport`: niega `s3:*` a todos cuando `aws:SecureTransport = false` | Solo se aceptan peticiones por TLS |
| Ciclo de vida | Regla `expire-old-state-versions` sobre todo el bucket: versiones no actuales a los **30 días**, cargas multiparte incompletas a los **7 días** y borrado de los marcadores de borrado huérfanos | Cada operación crea y borra `<clave>.tflock`; con versionado eso deja versiones viejas y marcadores que la regla limpia |
| Etiquetas | `Project=solventa`, `Owner=nicolas`, `Env=shared`, `Purpose=terraform-state`, **sin `TTL`** | Recurso compartido y permanente, no de una ventana de staging |
| Proveedor | `region = "us-east-2"` (validada) y `allowed_account_ids = ["368083581678"]` | Terraform se niega a planear o aplicar en otra región o en otra cuenta (p. ej. un perfil `default` distinto) |

Salidas: `bucket_name`, `bucket_arn` y `region`.

**Es permanente.** No se destruye nunca, ni con Terraform ni desde la consola: tiene el estado de todas las composiciones.
`prevent_destroy` hace fallar cualquier `plan` que lo borre. Cuesta menos de US$ 0,01 al mes (estados de pocos KB y unas
cuantas peticiones por operación).

## Convención de claves

Una clave por composición, con el nombre del ambiente y de la carpeta de la composición, en minúsculas:

```
<ambiente>/<composición>/terraform.tfstate
```

| Composición | Clave |
|---|---|
| `bootstrap/remote-state` (esta) | `bootstrap/remote-state/terraform.tfstate` |
| `environments/staging/ha-l01` | `staging/ha-l01/terraform.tfstate` |
| `environments/staging/ha-s01` | `staging/ha-s01/terraform.tfstate` |
| `environments/production-demo/<composición>` | `production-demo/<composición>/terraform.tfstate` |

Sin *workspaces* de Terraform: con *workspaces* las claves cambian de prefijo y hace falta además permiso para borrar el estado.

## Cómo iniciar una composición contra el bucket

1. En el bloque `terraform { }` de la composición (normalmente `versions.tf`), declara el backend con su clave. Un bloque
   `backend` **no admite variables ni `locals`**: los valores van literales.

   ```hcl
   terraform {
     required_version = ">= 1.11"

     backend "s3" {
       bucket       = "solventa-tfstate-368083581678"
       key          = "staging/ha-l01/terraform.tfstate"
       region       = "us-east-2"
       use_lockfile = true
       encrypt      = true
     }
   }
   ```

2. Inicializa la composición:

   ```bash
   terraform -chdir=infra/terraform/environments/staging/ha-l01 init
   ```

   Si la composición ya tenía un estado local (`terraform.tfstate` en su carpeta), respáldalo y migra ese estado al bucket:

   ```bash
   terraform -chdir=infra/terraform/environments/staging/ha-l01 init -migrate-state
   ```

   Después de migrar, `terraform plan` debe responder «No changes» y el `terraform.tfstate` local se puede borrar.

**Versión mínima de Terraform: 1.11.** El bloqueo nativo de S3 (`use_lockfile`) llegó en la 1.10 y es estable desde la 1.11,
que además marcó como obsoleto el bloqueo con DynamoDB. Con una versión anterior, `init` falla porque no reconoce
`use_lockfile`. `encrypt = true` cifra el estado y el archivo de bloqueo.

### Permisos IAM para usar el backend

| Acción | Recurso |
|---|---|
| `s3:ListBucket` | `arn:aws:s3:::solventa-tfstate-368083581678` |
| `s3:GetObject`, `s3:PutObject` | `arn:aws:s3:::solventa-tfstate-368083581678/<clave>` |
| `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject` | `arn:aws:s3:::solventa-tfstate-368083581678/<clave>.tflock` |

Cualquier usuario de la cuenta con `s3:GetObject` sobre el bucket puede leer todos los estados, que guardan en texto plano
los valores sensibles que Terraform conoce. Si hace falta restringirlo por prefijo, es una decisión del equipo.

## Primera aplicación (una sola vez)

Este bucket guarda también su propio estado, pero para crearlo Terraform necesita un estado antes de que el bucket exista.
Por eso se aplica una única vez, así:

1. `terraform init` y `terraform apply` **sin** bloque `backend` (estado local en la carpeta).
2. Bloque `backend "s3"` en `versions.tf` con la clave `bootstrap/remote-state/terraform.tfstate` y
   `terraform init -migrate-state`, que copia el estado local al bucket. La migración no borra el estado local: queda en disco
   como respaldo hasta comprobar la copia.
3. `terraform state list` (los 7 recursos) y `terraform plan` → «No changes»; recién entonces se borra el estado local del
   disco. Desde ahí el versionado del bucket conserva las versiones anteriores del estado.

Para aplicarlo, el usuario necesita `s3:CreateBucket`, `s3:PutBucketOwnershipControls`, `s3:PutBucketPublicAccessBlock`,
`s3:PutBucketVersioning`, `s3:PutEncryptionConfiguration`, `s3:PutBucketPolicy`, `s3:PutLifecycleConfiguration`,
`s3:PutBucketTagging` y las lecturas `s3:Get*` y `s3:ListBucket` sobre el bucket, además de los permisos de la tabla anterior.

## Cómo se recupera el estado del *bootstrap*

El estado vive en el propio bucket, así que basta con clonar el repositorio:

```bash
terraform -chdir=infra/terraform/bootstrap/remote-state init
terraform -chdir=infra/terraform/bootstrap/remote-state plan   # «No changes»
```

Para cambiar la configuración del bucket se edita el código y se aplica de nuevo; el bloqueo impide dos `apply` a la vez. Si
el estado se perdiera del todo, la configuración se recupera con `terraform import` de los 7 recursos.

## Cómo restaurar una versión anterior de un estado

El versionado conserva durante 30 días las versiones anteriores de cada estado. Para volver a una de ellas, con nadie
trabajando sobre esa composición:

1. Lista las versiones de la clave:

   ```bash
   aws s3api list-object-versions --bucket solventa-tfstate-368083581678 --prefix staging/ha-l01/terraform.tfstate
   ```

2. Copia la versión elegida sobre la actual (queda como una versión nueva; la actual no se pierde):

   ```bash
   aws s3api copy-object --bucket solventa-tfstate-368083581678 --key staging/ha-l01/terraform.tfstate \
     --copy-source "solventa-tfstate-368083581678/staging/ha-l01/terraform.tfstate?versionId=<VersionId>"
   ```

3. Comprueba el resultado con `terraform plan` en la composición.

## Bloqueo colgado

Si un proceso de Terraform se interrumpe, `<clave>.tflock` puede quedar en el bucket y las siguientes operaciones fallan con
«Error acquiring the state lock» y la información del bloqueo (incluido su `ID`). Una persona, tras confirmar con el equipo
que nadie está planeando ni aplicando esa composición, lo libera:

```bash
terraform -chdir=<carpeta de la composición> force-unlock <ID>
```

## Pruebas (locales, sin AWS)

```bash
terraform -chdir=infra/terraform/bootstrap/remote-state init -backend=false
terraform -chdir=infra/terraform/bootstrap/remote-state validate
terraform -chdir=infra/terraform/bootstrap/remote-state test
```

[`tests/remote-state.tftest.hcl`](tests/remote-state.tftest.hcl) usa un proveedor simulado (`mock_provider "aws"`): no
necesita credenciales ni crea nada. Sus 8 casos, todos con `command = plan`, afirman el nombre del bucket y que cada ajuste
apunta a él, el rechazo de otra región, el versionado, el cifrado SSE-S3, el bloqueo de acceso público y `BucketOwnerEnforced`,
la política que niega el acceso sin TLS, el ciclo de vida y las etiquetas sin `TTL`. `prevent_destroy` no se puede afirmar
con un `plan` simulado: se comprueba con `terraform plan -destroy` contra el bucket real, que debe fallar.
