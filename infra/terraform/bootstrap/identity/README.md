# `bootstrap/identity` — user pool permanente de los canales humanos (T4)

User pool de Amazon Cognito **permanente y compartido** del portal web y de la app móvil: el pool `solventa-users`, con los cuatro
grupos de rol, los clientes `web` y `mobile` y **un usuario de prueba permanente por rol**. Lo leen las dos composiciones de la
plataforma, [`production-demo/platform`](../../environments/production-demo/platform/README.md) y
[`staging/platform`](../../environments/staging/platform/README.md), que no crean pool propio.

Es permanente porque un pool que nace y muere con cada ventana de staging estrena identificadores cada vez: el portal y la APK
tendrían que reconfigurarse y recompilarse, la app móvil no tendría un pool estable contra el cual conectarse y los usuarios de prueba
desaparecerían con el `destroy`. Con este pool, región, ID del pool y clientes se configuran **una sola vez** y las ventanas de
staging usan siempre los mismos cuatro usuarios.

> **Estado:** **aplicado el 2026-10-18** en `us-east-2` (15 recursos; un `plan` posterior da «No changes»). User pool
> `solventa-users` con ID `us-east-2_EWwg8NL2C` y candado de borrado `ACTIVE`; clientes `solventa-users-web`
> (`v16a9q5ba8hhh5keo76fjd4e1`) y `solventa-users-mobile` (`6dri40bi3sk74cr0jb5djq43c0`); emisor
> `https://cognito-idp.us-east-2.amazonaws.com/us-east-2_EWwg8NL2C`. Ninguno de estos valores es secreto. No se vuelve a aplicar
> salvo para cambiar la contraseña de los usuarios de prueba (ver «Aplicación única» y el runbook).

Se aplica una vez y no se destruye, como [`remote-state`](../remote-state/README.md) y [`cd-ecr`](../cd-ecr/README.md).

| Elemento | Configuración | Por qué |
|---|---|---|
| Pool | `solventa-users`, creado con el módulo [`cognito`](../../modules/cognito/README.md) de HA-S01 | Mismo pool que define la arquitectura (proveedor de identidad): alta solo administrativa, inicio de sesión con el correo |
| Plan | Essentials | Gratis hasta 10 000 usuarios activos al mes |
| Candado | `deletion_protection = true` (`ACTIVE` en AWS) | AWS rechaza borrar el pool mientras esté activo; `prevent_destroy` no se puede fijar desde fuera del módulo |
| Grupos | `insured`, `advisor`, `operations`, `partner` | Son los roles de `pkg/security` (claim `cognito:groups`) |
| Clientes | `solventa-users-web` y `solventa-users-mobile` (claves `web` y `mobile`), públicos, sin secreto, `ALLOW_USER_PASSWORD_AUTH` + `ALLOW_REFRESH_TOKEN_AUTH`, con revocación | Valores por defecto del módulo `cognito` |
| Vigencias | ID y access token: 5 min · renovación web: 12 h · renovación móvil: 30 días | Valores por defecto del módulo `cognito` (HA-S01) |
| Usuarios de prueba | Cuatro, uno por grupo (tabla siguiente), con contraseña permanente y sin invitación | Pruebas y demostraciones en todos los ambientes |
| Estado | `bootstrap/identity/terraform.tfstate` en `solventa-tfstate-368083581678`, bloqueo nativo de S3, cifrado, desde el primer `init` | Convención de claves de T4 |
| Etiquetas | `Project=solventa`, `Owner=nicolas`, `Env=shared`, `Task=T4`, `Purpose=identity`, **sin `TTL` ni `Story`** | Recurso compartido y permanente, no de una ventana |
| Proveedor | `region = "us-east-2"` (validada) y `allowed_account_ids = ["368083581678"]` | Terraform se niega a planear o aplicar en otra región o en otra cuenta |

## Usuarios de prueba

| Rol (grupo) | Usuario | `custom:partner_id` |
|---|---|---|
| Asegurado (`insured`) | `asegurado@example.com` | — |
| Asesor (`advisor`) | `asesor@example.com` | — |
| Operaciones (`operations`) | `operaciones@example.com` | — |
| Socio (`partner`) | `socio@example.com` | `socio-a` (el socio por defecto de los usage plans) |

Los correos son ficticios (dominio reservado `example.com`). Se declaran en `local.test_users` de `main.tf`; el alta, la baja, el
cambio de contraseña y la verificación están en el runbook
[«Usuarios de prueba permanentes»](../../../../docs/runbooks/README.md#5-usuarios-de-prueba-permanentes-bootstrapidentity).

**Contraseña.** Los cuatro comparten una contraseña permanente que entra **solo** por la variable de entorno
`TF_VAR_seed_user_password` (12 o más caracteres, con minúsculas, mayúsculas, números y símbolos; la variable no tiene valor por
defecto, así que un `plan` sin ella falla). Nunca va en un `.tfvars`, en el código ni en un mensaje. Como cualquier argumento de un
recurso, queda en el **estado cifrado** del bucket de T4 y en el archivo de un `plan -out`: por eso el plan guardado se escribe
**fuera del repositorio** y se borra en cuanto se aplica (el `.gitignore` no ignora `*.tfplan`). Es aceptable para usuarios de
prueba ficticios.

Salidas (ninguna es secreta): `region`, `user_pool_id`, `user_pool_arn`, `issuer`, `jwks_url`, `client_ids` (`web`, `mobile`),
`group_names` y `test_users` (rol → correo).

## Aplicación única

Requiere Terraform 1.11 o superior, credenciales de la cuenta `368083581678` (`aws sts get-caller-identity`) y la contraseña en el
entorno. Se lanza desde la raíz del repositorio:

```bash
export AWS_PROFILE=solventa
export TF_VAR_seed_user_password=…            # nunca en un archivo
unset TF_LOG                                   # el log detallado podría mostrar la contraseña

terraform -chdir=infra/terraform/bootstrap/identity init      # configura el backend S3 desde el primer init
terraform -chdir=infra/terraform/bootstrap/identity plan -out=<carpeta fuera del repo>/identity.tfplan
#   esperado: 15 por crear, solo module.cognito.aws_cognito_* (1 pool, 4 grupos, 2 clientes, 4 usuarios, 4 pertenencias)
terraform -chdir=infra/terraform/bootstrap/identity apply <carpeta fuera del repo>/identity.tfplan
rm <carpeta fuera del repo>/identity.tfplan

terraform -chdir=infra/terraform/bootstrap/identity plan -detailed-exitcode   # código 0: «No changes.»
terraform -chdir=infra/terraform/bootstrap/identity output user_pool_id       # y region, client_ids, issuer, test_users
unset TF_VAR_seed_user_password
```

Las salidas se leen **por nombre** (`output <nombre>`), nunca con `output -json` ni `show -json`. Si `init` no configura el backend
S3, no se sigue con estado local.

## Quién la lee

`production-demo/platform` y `staging/platform` leen sus salidas del estado remoto y se las pasan al módulo
[`platform`](../../modules/platform/README.md#user-pool-propio-o-externo) en `existing_cognito`. Los nombres `user_pool_id`,
`user_pool_arn`, `issuer`, `jwks_url` y `client_ids` son el contrato entre las composiciones: no se renombran.

```hcl
data "terraform_remote_state" "identity" {
  backend = "s3"
  config = {
    bucket = "solventa-tfstate-368083581678"
    key    = "bootstrap/identity/terraform.tfstate"
    region = "us-east-2"
  }
}

module "platform" {
  # ...
  existing_cognito = {
    user_pool_id  = data.terraform_remote_state.identity.outputs.user_pool_id
    user_pool_arn = data.terraform_remote_state.identity.outputs.user_pool_arn
    issuer        = data.terraform_remote_state.identity.outputs.issuer
    jwks_url      = data.terraform_remote_state.identity.outputs.jwks_url
    client_ids    = data.terraform_remote_state.identity.outputs.client_ids
  }
}
```

Por eso las dos composiciones solo se pueden planear después de aplicar esta. El `destroy` de una ventana de `staging/platform` no
toca el pool: no pertenece a esa composición. Los valores que configuran el portal y la app están en
[`docs/api/README.md`](../../../../docs/api/README.md#valores-de-los-clientes-por-ambiente).

## Lo que no se hace

- **`destroy`:** nunca. El candado de AWS lo rechaza y el pool lo usan todos los ambientes.
- **Renombrar el pool** o cambiar en el módulo `cognito` algo que obligue a reemplazarlo (por ejemplo el esquema de atributos o
  `username_attributes`). Si un `plan` muestra el pool con `must be replaced`, no se aplica: con el candado activo el `apply` falla, y
  reemplazarlo exigiría un `apply` aparte que desactive el candado, migrar los usuarios y reconfigurar el portal y la app con los
  identificadores nuevos.

## Pruebas (locales, sin AWS)

```bash
terraform -chdir=infra/terraform/bootstrap/identity init -backend=false
terraform -chdir=infra/terraform/bootstrap/identity validate
terraform -chdir=infra/terraform/bootstrap/identity test
```

`tests/identity.tftest.hcl` usa un proveedor simulado y un valor de prueba como contraseña: no necesita credenciales ni crea nada.
Comprueba las etiquetas de recurso permanente, que se rechaza otra región, el nombre de los grupos y de los clientes, el emisor y
la URL de JWKS, un usuario de prueba por rol con `socio-a` solo en el del socio, correos ficticios y únicos, la salida `test_users` y
que se rechaza una contraseña corta. El alta solo administrativa, el plan Essentials y los clientes públicos los comprueban las
pruebas del módulo `cognito`. `.terraform.lock.hcl` es copia del de `cd-ecr` (mismo proveedor `aws`).

## Costos

Sin costo: la franja gratuita de Essentials cubre 10 000 usuarios activos al mes por cuenta, sin cargo fijo por pool, y el pool no
envía SMS ni correos (alta sin invitación). El estado en el bucket de T4 son pocos KB (menos de US$ 0,01 al mes).
