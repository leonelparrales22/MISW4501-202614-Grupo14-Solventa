# Módulo `cognito` — user pool de los canales humanos (HA-S01)

User pool de Amazon Cognito para las personas que usan el portal web y la app móvil: asegurado, asesor comercial, analista
de operaciones y usuario del portal del socio. Las integraciones máquina a máquina de los socios **no** pasan por aquí:
usan API keys y usage plans del API Gateway (CA-5).

| Elemento | Configuración | Por qué |
|---|---|---|
| Alta de usuarios | `allow_admin_create_user_only = true`: sin autorregistro | Solo un administrador aprovisiona usuarios (CA-3) |
| Inicio de sesión | Con el correo (`username_attributes = ["email"]`, sin distinguir mayúsculas) | — |
| Grupos | `insured`, `advisor`, `operations`, `partner`, un grupo por usuario | Son los roles de `pkg/security` (claim `cognito:groups`, decisión D-3) |
| Titularidad del socio | Atributo `custom:partner_id`: los clientes lo pueden **leer** (llega al ID token) pero **no escribir** | Solo lo escribe quien aprovisiona; nadie cambia su propia titularidad (D-3b) |
| Clientes `web` y `mobile` | Sin secreto, `ALLOW_USER_PASSWORD_AUTH` + `ALLOW_REFRESH_TOKEN_AUTH`, revocación activa, errores que no revelan si el usuario existe | Clientes públicos (D-5); `RevokeToken` al cerrar sesión (CA-4) |
| Atributos escribibles por el usuario | Solo `name` | La lista explícita evita que Cognito deje escribir todos los atributos (incluido `custom:partner_id`) |
| Vigencias | ID y access token: 5 min · refresco web: 12 h · refresco móvil: 30 días | D-6. Un token revocado sigue siendo válido hasta su `exp`: ventana residual ≤ 5 min (`docs/api/README.md`) |
| Plan y protección | Essentials (gratis hasta 10 000 MAU); `deletion_protection` activa salvo en las ventanas de staging | Arquitectura (proveedor de identidad) |
| Usuarios semilla | Opcionales (`seed_users`), con contraseña **permanente** y sin invitación | Solo para ventanas de staging (D-13) |

## Uso

```hcl
module "cognito" {
  source              = "../../../modules/cognito"
  name                = "solventa-staging-ha-s01"
  deletion_protection = false # ventana efímera: destroy debe poder borrarlo

  seed_users = {
    asesor = { email = "asesor@example.com", group = "advisor" }
    socio  = { email = "socio@example.com", group = "partner", partner_id = "socio-a" }
  }
  seed_user_password = var.seed_user_password # solo desde TF_VAR_seed_user_password
}
```

| Variable | Defecto | Descripción |
|---|---|---|
| `name` | — | Nombre del user pool y prefijo de los clientes |
| `deletion_protection` | `true` | `false` solo en ventanas efímeras de staging |
| `user_pool_tier` | `ESSENTIALS` | Plan del user pool |
| `password_minimum_length` | `12` | Longitud mínima (exige minúsculas, mayúsculas, números y símbolos) |
| `token_validity_minutes` | `5` | Vigencia del ID y del access token (mínimo de Cognito) |
| `web_refresh_token_validity_hours` | `12` | Vigencia del token de renovación web |
| `mobile_refresh_token_validity_days` | `30` | Vigencia del token de renovación móvil |
| `explicit_auth_flows` | `ALLOW_USER_PASSWORD_AUTH`, `ALLOW_REFRESH_TOKEN_AUTH` | Flujos de los clientes (nunca `ALLOW_ADMIN_USER_PASSWORD_AUTH`) |
| `seed_users` | `{}` | Usuarios de prueba: `email`, `group` y `partner_id` (obligatorio solo para `partner`) |
| `seed_user_password` | `null` (sensible) | Contraseña permanente de los usuarios semilla. **Nunca** en el código ni en un `.tfvars` versionado |
| `tags` | `{}` | Etiquetas adicionales |

Salidas: `user_pool_id`, `user_pool_arn` (proveedor del authorizer del API Gateway), `issuer` y `jwks_url` (`AUTH_ISSUER` y
`AUTH_JWKS_URL` de los servicios), `client_ids` (`web`, `mobile`; juntos forman `AUTH_CLIENT_IDS`) y `group_names`.

El alta de usuarios fuera de staging sigue el runbook «Aprovisionar usuarios internos y del portal del socio»
(`docs/runbooks/README.md`): en el Sprint 1 es administrativa (D-2); el alta desde el sistema llega con HU-W3.1-01 y la de
asegurados con la verificación KYC (HU-M1.1-01).

## Pruebas (locales, sin AWS)

```bash
terraform -chdir=infra/terraform/modules/cognito init -backend=false
terraform -chdir=infra/terraform/modules/cognito validate
terraform -chdir=infra/terraform/modules/cognito test
```

`tests/cognito.tftest.hcl` usa un proveedor simulado (`mock_provider "aws"`): no necesita credenciales ni crea nada. Afirma
el alta solo administrativa, los cuatro grupos, los clientes públicos con revocación, que ningún cliente escribe
`custom:partner_id` ni el correo, las vigencias y las validaciones de los usuarios semilla. El comportamiento real
(`SignUp` rechazado, `UpdateUserAttributes` sobre `custom:partner_id` rechazado, revocación) se comprueba en la ventana de
staging de HA-S01 (`tests/security/ha-s01`).
