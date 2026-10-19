# Módulo `elasticache` — Redis (T4)

Grupo de replicación Redis 7.1 en subredes privadas, cifrado en reposo y alcanzable solo desde las tareas. `multi_az` sigue el
mismo criterio que RDS y el NAT: `false` por defecto (un nodo), `true` agrega una réplica en la otra zona con *failover*
automático.

**Sin cifrado en tránsito:** el cliente de `origination` (go-redis solo con `Addr`) habla TCP plano y sin contraseña. Activar TLS y
un token AUTH requiere primero que el servicio los soporte.

| Variable | Defecto | Descripción |
|---|---|---|
| `name` | — | Identificador (≤ 40 caracteres) |
| `vpc_id` / `subnet_ids` | — | Subredes privadas |
| `allowed_security_group_ids` | — | Mapa clave → security group que llega al 6379 |
| `engine_version` / `parameter_group_name` | `"7.1"` / `default.redis7` | — |
| `node_type` | `cache.t4g.micro` | — |
| `multi_az` | `false` | Réplica con *failover* |
| `snapshot_retention_days` | `0` | Es caché; se reconstruye desde las fuentes |

Salidas: `primary_endpoint` (`host:puerto`, el `REDIS_ADDR` de los servicios) y `security_group_id`.

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform test
```

Usan un proveedor simulado: no necesitan credenciales y no crean nada.
