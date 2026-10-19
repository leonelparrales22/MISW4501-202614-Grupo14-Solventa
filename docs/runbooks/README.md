# Runbooks

Procedimientos operativos. **Estado:** se agregan a medida que existan los ambientes.

| Runbook | Estado |
|---|---|
| Levantar el ambiente local (PostgreSQL, Redis, LocalStack, Toxiproxy) | Pendiente (tarea T2 del plan de sprints) |
| Ejecutar pruebas de carga y resiliencia | Pendiente |
| Failover y rollback en AWS | Pendiente |
| [Publicar imágenes en ECR (CD)](../../infra/terraform/bootstrap/cd-ecr/README.md) | Disponible: primera aplicación y funcionamiento de `backend-cd`. El despliegue en ECS y su rollback siguen pendientes |
| Rotación de credenciales de socios | Pendiente |
| [Aprovisionar usuarios internos y del portal del socio](#aprovisionar-usuarios-internos-y-del-portal-del-socio) (grupo de Cognito y `custom:partner_id`) | Disponible (staging y [usuarios de prueba permanentes](#5-usuarios-de-prueba-permanentes-bootstrapidentity) del pool `solventa-users`, T4). En el Sprint 1 el alta es administrativa con Terraform (módulo `infra/terraform/modules/cognito`, HA-S01); el alta desde el sistema llega con HU-W3.1-01. Los asegurados se aprovisionan tras la verificación KYC (HU-M1.1-01) |

Plan: [Plan de trabajo por sprints](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Plan-de-trabajo-por-sprints).

## Aprovisionar usuarios internos y del portal del socio

Es la «función administrativa» de HA-S01 (CA-3): los usuarios internos (asesor comercial, analista de operaciones) y los del
portal del socio **los crea un administrador**, con su grupo asignado. El user pool lo define el módulo
[`cognito`](../../infra/terraform/modules/cognito/README.md): alta solo administrativa, un grupo por rol (`insured`, `advisor`,
`operations`, `partner`) y el atributo `custom:partner_id`, que los clientes pueden leer pero no escribir.

### 1. Alta en staging (usuarios semilla de Terraform)

En las composiciones de staging (por ejemplo [`staging/ha-s01`](../../infra/terraform/environments/staging/ha-s01/README.md)) los
usuarios se declaran en la variable `seed_users` del módulo:

```hcl
seed_users = {
  advisor = { email = "asesor@example.com", group = "advisor" }
  partner = { email = "socio@example.com", group = "partner", partner_id = "socio-a" }
}
```

1. Cada usuario lleva **su correo** (es el nombre de usuario), **un solo grupo** y `partner_id` **solo** si el grupo es `partner`
   (las validaciones del módulo rechazan un `partner_id` en otro grupo o un socio sin él).
2. La contraseña permanente (sin invitación ni correo, decisión D-13) va **solo** en la variable de entorno
   `TF_VAR_seed_user_password`: nunca en un `.tfvars`, en el código ni en un mensaje. Debe cumplir la política del pool (12 o más
   caracteres, con mayúsculas, minúsculas, números y símbolos).
3. `terraform plan` y `terraform apply` de la composición, con la autorización y la estimación de costo que exija la ventana.
4. Verificación de **solo lectura** de que el usuario quedó en exactamente un grupo, el suyo:

   ```bash
   POOL="$(terraform -chdir=infra/terraform/environments/staging/ha-s01 output -raw user_pool_id)"
   aws cognito-idp admin-list-groups-for-user --region us-east-2 --user-pool-id "$POOL" --username asesor@example.com \
     --query 'Groups[].GroupName'
   ```

   Para un socio, `custom:partner_id` se ve en el ID token (claims del inicio de sesión) o con
   `aws cognito-idp admin-get-user --region us-east-2 --user-pool-id "$POOL" --username socio@example.com` (también de lectura).

### 2. Baja

Quitar el usuario de `seed_users` y volver a correr `terraform apply`: Terraform borra su pertenencia al grupo y el usuario. Sus
sesiones vigentes caducan con sus tokens (5 minutos como máximo; ver «Ventana residual de revocación» en
[`docs/api/README.md`](../api/README.md)).

### 3. Lo que un usuario no puede hacer (y se verifica en staging)

- **Autorregistrarse:** `SignUp` con cualquier cliente responde `NotAuthorizedException` (`allow_admin_create_user_only`).
- **Cambiar su grupo:** los grupos solo se asignan con operaciones administrativas; ningún cliente los modifica.
- **Escribir `custom:partner_id`:** `UpdateUserAttributes` con su propio token lo rechaza (el atributo no está entre los
  escribibles de los clientes).

La ventana de verificación de HA-S01 lo comprueba con [`tests/security/ha-s01/verify-access.sh`](../../tests/security/ha-s01/README.md).

### 4. Fuera de staging

- Los usuarios semilla de las composiciones con pool propio, como `staging/ha-s01`, son **solo para sus ventanas** (D-13).
- El pool permanente `solventa-users` de [`bootstrap/identity`](../../infra/terraform/bootstrap/identity/README.md), que usan
  `staging/platform` y `production-demo/platform`, tiene **cuatro usuarios de prueba permanentes**, uno por rol (sección 5). Son
  ficticios (`@example.com`) y sirven para las pruebas y las demostraciones de todos los ambientes.
- Fuera de esos usuarios de prueba, el alta la hace el administrador del user pool del equipo (Carlos, a cargo de la
  infraestructura) con las operaciones administrativas de Cognito, con las mismas reglas: un solo grupo por usuario y
  `custom:partner_id` solo para usuarios del portal del socio. El alta desde el sistema llega con HU-W3.1-01.
- Los **asegurados** no se aprovisionan a mano: llegan tras la verificación KYC (HU-M1.1-01).
- Las integraciones máquina a máquina de los socios **no** usan este pool: se autentican con API keys y usage plans del API
  Gateway (CA-5).

### 5. Usuarios de prueba permanentes (`bootstrap/identity`)

El pool permanente `solventa-users` (`us-east-2`) tiene un usuario de prueba por rol, declarados en `local.test_users` de
[`bootstrap/identity/main.tf`](../../infra/terraform/bootstrap/identity/main.tf):

| Rol (grupo) | Usuario |
|---|---|
| Asegurado (`insured`) | `asegurado@example.com` |
| Asesor (`advisor`) | `asesor@example.com` |
| Operaciones (`operations`) | `operaciones@example.com` |
| Socio (`partner`, `custom:partner_id = socio-a`) | `socio@example.com` |

Los cuatro comparten una contraseña permanente (sin invitación ni correo). La tiene Nicolás en su variable de usuario de Windows
`SOLVENTA_IDENTITY_SEED_PASSWORD` y la comparte en privado con quien la necesite; nunca va en un archivo, en el código ni en un
mensaje del grupo. Terraform la recibe solo por `TF_VAR_seed_user_password` y los guiones de prueba por su propia variable de
entorno (p. ej. `SEED_USER_PASSWORD` del humo de HA-D02).

**Alta o baja de un usuario de prueba.** Editar `local.test_users` (un solo grupo por usuario y `partner_id` solo para `partner`),
abrir el PR y, una vez integrado, `plan` y `apply` de `bootstrap/identity` con la autorización que corresponda. El `plan` debe
mostrar solo el usuario y su pertenencia al grupo (`aws_cognito_user.seed` y `aws_cognito_user_in_group.seed`), nunca el pool.

**Cambio de contraseña.** Crear la contraseña nueva (12 o más caracteres, con minúsculas, mayúsculas, números y símbolos), pasarla
en `TF_VAR_seed_user_password` y aplicar `bootstrap/identity`: Terraform cambia la de los cuatro usuarios. Avisar en privado a
quienes la usen.

**Verificación de lectura** (perfil de la cuenta `368083581678`, solo lectura):

```bash
POOL="$(terraform -chdir=infra/terraform/bootstrap/identity output -raw user_pool_id)"
aws cognito-idp describe-user-pool --region us-east-2 --user-pool-id "$POOL" \
  --query "UserPool.{Name:Name,Protection:DeletionProtection,Tier:UserPoolTier,AdminOnly:AdminCreateUserConfig.AllowAdminCreateUserOnly}"
aws cognito-idp list-groups --region us-east-2 --user-pool-id "$POOL" --query 'Groups[].GroupName'
aws cognito-idp list-user-pool-clients --region us-east-2 --user-pool-id "$POOL" --query 'UserPoolClients[].ClientName'
for u in asegurado@example.com asesor@example.com operaciones@example.com socio@example.com; do
  aws cognito-idp admin-list-groups-for-user --region us-east-2 --user-pool-id "$POOL" --username "$u" --query 'Groups[].GroupName'
  aws cognito-idp admin-get-user --region us-east-2 --user-pool-id "$POOL" --username "$u" --query 'UserStatus'
done
```

Esperado: `solventa-users`, `ACTIVE`, `ESSENTIALS`, `true`; los cuatro grupos; los clientes `solventa-users-web` y
`solventa-users-mobile`; cada usuario en exactamente su grupo y `CONFIRMED` (el socio con `custom:partner_id = socio-a`, visible en
la salida completa de `admin-get-user`).

**Inicio de sesión real** (lo corre quien tiene la contraseña, en Windows PowerShell; no imprime la contraseña ni los tokens, solo
el grupo de cada usuario). `<cliente web>` es el valor `web` de `terraform output client_ids`:

```powershell
$clienteWeb = '<cliente web>'
$clave = [Environment]::GetEnvironmentVariable('SOLVENTA_IDENTITY_SEED_PASSWORD', 'User')
foreach ($u in 'asegurado@example.com', 'asesor@example.com', 'operaciones@example.com', 'socio@example.com') {
  $cuerpo = @{ AuthFlow = 'USER_PASSWORD_AUTH'; ClientId = $clienteWeb; AuthParameters = @{ USERNAME = $u; PASSWORD = $clave } } | ConvertTo-Json -Compress
  try {
    $resp = Invoke-WebRequest -UseBasicParsing -Method Post -Uri 'https://cognito-idp.us-east-2.amazonaws.com/' `
      -Headers @{ 'X-Amz-Target' = 'AWSCognitoIdentityProviderService.InitiateAuth' } `
      -ContentType 'application/x-amz-json-1.1' -Body $cuerpo
    $r = $resp.Content | ConvertFrom-Json
    if (-not $r.AuthenticationResult) { '{0}: no entró (Cognito pide {1})' -f $u, $r.ChallengeName; continue }
    $p = $r.AuthenticationResult.IdToken.Split('.')[1].Replace('-', '+').Replace('_', '/')
    $p = $p.PadRight($p.Length + (4 - $p.Length % 4) % 4, '=')
    $claims = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($p)) | ConvertFrom-Json
    '{0}: entró · grupo {1}' -f $u, ($claims.'cognito:groups' -join ',')
  } catch {
    '{0}: no entró ({1})' -f $u, $_.Exception.Message
  }
}
Remove-Variable clave, cuerpo, resp, r, p, claims -ErrorAction SilentlyContinue
```

Esperado: cuatro líneas «entró» con los grupos `insured`, `advisor`, `operations` y `partner`. El flujo es `USER_PASSWORD_AUTH`, el
que admiten los clientes (públicos, sin secreto, así que no lleva `SECRET_HASH`).
