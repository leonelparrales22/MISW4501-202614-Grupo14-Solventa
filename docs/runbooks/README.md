# Runbooks

Procedimientos operativos. **Estado:** se agregan a medida que existan los ambientes.

| Runbook | Estado |
|---|---|
| Levantar el ambiente local (PostgreSQL, Redis, LocalStack, Toxiproxy) | Pendiente (tarea T2 del plan de sprints) |
| Ejecutar pruebas de carga y resiliencia | Pendiente |
| Failover y rollback en AWS | Pendiente |
| Rotación de credenciales de socios | Pendiente |
| [Aprovisionar usuarios internos y del portal del socio](#aprovisionar-usuarios-internos-y-del-portal-del-socio) (grupo de Cognito y `custom:partner_id`) | Disponible (staging). En el Sprint 1 el alta es administrativa con Terraform (módulo `infra/terraform/modules/cognito`, HA-S01); el alta desde el sistema llega con HU-W3.1-01. Los asegurados se aprovisionan tras la verificación KYC (HU-M1.1-01) |

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

- Los usuarios semilla son **solo para las ventanas de staging** (D-13). Fuera de ellas, el alta la hace el administrador del user
  pool del equipo (Carlos, a cargo de la infraestructura) con las operaciones administrativas de Cognito, con las mismas reglas: un
  solo grupo por usuario y `custom:partner_id` solo para usuarios del portal del socio. El alta desde el sistema llega con
  HU-W3.1-01.
- Los **asegurados** no se aprovisionan a mano: llegan tras la verificación KYC (HU-M1.1-01).
- Las integraciones máquina a máquina de los socios **no** usan este pool: se autentican con API keys y usage plans del API
  Gateway (CA-5).
