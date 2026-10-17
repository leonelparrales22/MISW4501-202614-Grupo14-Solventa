# Módulo `alb` — ALB interno con health checks (T4)

ALB interno, alcanzable solo desde el VPC link del API Gateway, con un target group (tipo `ip`) por servicio y reglas por ruta.
Una ruta que ningún servicio declara recibe `404`. Parámetros de EXP-D03 (ADR-02):

| Parámetro | Valor |
|---|---|
| Health check | `GET /health` cada 5 s, timeout 3 s, *healthy* tras 2 éxitos, *unhealthy* tras 2 fallas, código `200` |
| Deregistration delay | **30 s**: una tarea que sale termina sus peticiones en curso |
| Idle timeout | 10 s |

Una regla del ALB admite 5 valores por condición: los `path_patterns` de cada servicio se parten en reglas de 5 con prioridad
`priority × 10 + índice`.

| Variable | Defecto | Descripción |
|---|---|---|
| `name` | — | Nombre (≤ 18 caracteres, para que `<name>-notifications` quepa en 32) |
| `vpc_id` / `vpc_cidr_block` / `subnet_ids` | — | Red (dos zonas) |
| `ingress_security_group_ids` | — | Mapa clave → security group que puede llegar al listener (la clave fija mantiene `for_each` conocido en el plan) |
| `targets` | — | Por servicio: `port`, `path_patterns`, `priority` |
| `listener_port` / `idle_timeout` / `deregistration_delay` | `80` / `10` / `30` | — |
| `health_check` | EXP-D03 | Objeto con los parámetros del health check |
| `deletion_protection` | `false` | — |

Salidas: `arn`, `dns_name`, `security_group_id`, `listener_arn` y `target_group_arns`.

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform test
```

Usan un proveedor simulado: no necesitan credenciales y no crean nada.
