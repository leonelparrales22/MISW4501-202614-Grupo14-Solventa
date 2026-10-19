# Módulo `ecs` — clúster ECS Fargate (T4)

Clúster, grupo de logs (`/ecs/<name>`), espacio de nombres de Cloud Map, un security group compartido por las tareas y:

- **`services`**: servicios de larga duración, detrás del ALB (`load_balanced` + `target_group_arn`) y/o registrados en Cloud Map
  (`discovery_name`). Autoescalado por *target tracking* de CPU al 60 % (ADR-02) entre `desired_count` y `max_count`;
  *circuit breaker* de despliegue con *rollback*.
- **`tasks`**: definiciones de tarea sin servicio (p. ej. `db-init`), que se lanzan con `aws ecs run-task`.

`secrets` asocia una variable de entorno a un ARN de Secrets Manager (opcionalmente con `:<clave json>::`). El rol de ejecución
solo puede leer los secretos referenciados; cada servicio con `task_role_statements` recibe su propio rol de tarea.
`load_balanced` es una bandera explícita porque el ARN del target group no se conoce en el plan.

Un `apply` restablece `desired_count`; el autoescalado lo vuelve a mover dentro de `[desired_count, max_count]`.

| Variable | Defecto | Descripción |
|---|---|---|
| `name` | — | Clúster y prefijo |
| `vpc_id` / `subnet_ids` / `assign_public_ip` | — | Red de las tareas (salidas `app_*` de `network`) |
| `alb_security_group_id` | `null` | Origen permitido hacia los puertos de los servicios `load_balanced` |
| `discovery_namespace` | `"solventa.local"` | Espacio de nombres de Cloud Map |
| `services` / `tasks` | `{}` | Ver arriba |
| `cpu_target_percent` | `60` | Objetivo de CPU (ADR-02) |
| `health_check_grace_period_seconds` | `30` | Gracia de una tarea que arranca |
| `log_retention_days` / `container_insights` | `7` / `false` | — |

Salidas: `cluster_name`, `cluster_arn`, `tasks_security_group_id`, `log_group_name`, `service_names`, `desired_counts`,
`task_definition_families`, `execution_role_arn` y `task_role_arns`.

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform test
```

Usan un proveedor simulado: no necesitan credenciales y no crean nada.
