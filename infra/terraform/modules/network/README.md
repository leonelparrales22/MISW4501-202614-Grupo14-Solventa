# Módulo `network` — VPC en dos zonas de disponibilidad (T4)

VPC con una subred pública y una privada en cada una de dos zonas. RDS, ElastiCache, el ALB interno y el VPC link van siempre en
las privadas; dónde corren las tareas Fargate lo decide `nat_mode`:

| `nat_mode` | NAT Gateways | Tareas | Costo aproximado | Uso |
|---|---|---|---|---|
| `none` (defecto) | 0 | Subredes públicas con IP pública (sus security groups no admiten tráfico de internet) | US$ 0 de NAT; ≈ US$ 3,6/mes por IPv4 pública | Lo de siempre |
| `single` | 1, en la primera zona | Subredes privadas | ≈ US$ 33/mes + datos | Si cae esa zona, la otra pierde salida |
| `per_az` | 1 por zona | Subredes privadas | ≈ US$ 66/mes + datos | Demo final (tolerancia a fallas) |

Cambiar de modo mueve las tareas de subred; ECS las reemplaza con un despliegue gradual. Las subredes privadas tienen siempre el
*endpoint* de puerta de enlace de S3 (gratis).

| Variable | Defecto | Descripción |
|---|---|---|
| `name` | — | Prefijo de los nombres |
| `cidr_block` | — | CIDR `/16`; públicas en los `/24` 0 y 1, privadas en los `/24` 10 y 11 |
| `nat_mode` | `"none"` | `none`, `single` o `per_az` |
| `tags` | `{}` | Etiquetas adicionales |

Salidas: `vpc_id`, `vpc_cidr_block`, `availability_zones`, `public_subnet_ids`, `private_subnet_ids`, `app_subnet_ids`,
`app_assign_public_ip` y `nat_gateway_ids`.

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform test
```

Usan un proveedor simulado: no necesitan credenciales y no crean nada.
