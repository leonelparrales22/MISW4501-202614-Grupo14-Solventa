# Módulo `rds` — PostgreSQL con una base por servicio (T4)

Una instancia PostgreSQL 16 privada y cifrada, compartida por los servicios con **una base y un rol de login por servicio**
(`<servicio>` / `<servicio>_app`).

- La clave del administrador (`solventa_admin`) la **gestiona RDS en Secrets Manager** (`manage_master_user_password`): nunca llega
  al estado de Terraform.
- Genera una contraseña de 32 caracteres por servicio (salida sensible `passwords`); quien llama la guarda con la `DATABASE_URL` en
  el secreto del servicio. Los roles y las bases los crea la tarea `db-init` del módulo [`platform`](../platform/README.md).
- **`multi_az`** es una variable, `false` por defecto (costo) y `true` para la demo final o una prueba de tolerancia a fallas.
  Cambiarla es una modificación en línea (RDS crea la réplica desde un snapshot) y **no pierde datos**; con `apply_immediately` se
  aplica en el momento.

| Variable | Defecto | Descripción |
|---|---|---|
| `name` | — | Identificador |
| `vpc_id` / `subnet_ids` | — | Subredes privadas en dos zonas |
| `allowed_security_group_ids` | — | Mapa clave → security group que llega al 5432 |
| `databases` | — | Una base y un rol por elemento |
| `engine_version` / `instance_class` | `"16"` / `db.t4g.micro` | — |
| `allocated_storage` / `max_allocated_storage` | `20` / `0` | GB, gp3 |
| `multi_az` / `apply_immediately` | `false` / `true` | — |
| `backup_retention_days` | `1` | 1 a 35 |
| `deletion_protection` / `skip_final_snapshot` | `true` / `false` | Snapshot final `<name>-final` |

Salidas: `address`, `port`, `master_user_secret_arn`, `security_group_id`, `databases` y `passwords` (sensible).

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform test
```

Usan un proveedor simulado: no necesitan credenciales y no crean nada.
