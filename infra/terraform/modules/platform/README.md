# Módulo `platform` — plataforma completa de un ambiente (T4)

Compone los módulos vecinos en la plataforma persistente de un ambiente. Lo llaman
[`staging/platform`](../../environments/staging/platform/README.md) y
[`production-demo/platform`](../../environments/production-demo/platform/README.md), que solo fijan los valores de cada
ambiente.

```
cliente ──HTTPS──▶ API Gateway REST (stage = ambiente)
   │  rutas de socio: API key + usage plan por socio (D-7: 100 req/s, ráfaga 200)
   │  rutas humanas: authorizer de Cognito (valida el JWT)
   └──VPC Link V2──▶ ALB interno (health check EXP-D03, deregistration delay 30 s)
        └──por ruta──▶ ECS Fargate: origination · claims · partners · audit · notifications
                          ├─▶ RDS PostgreSQL (una base y un usuario por servicio)
                          ├─▶ ElastiCache Redis
                          ├─▶ SNS (un tópico por dominio) ─▶ SQS por consumidor + DLQ
                          ├─▶ S3 (documents, evidence)
                          └─Cloud Map─▶ stubs openfinance · opendata · kyc · email · payment-gateway
```

| Pieza | Módulo | Configuración |
|---|---|---|
| Red | [`network`](../network/README.md) | VPC en dos AZ; `nat_mode` (`none` / `single` / `per_az`) decide dónde corren las tareas |
| ECR | [`ecr`](../ecr/README.md) vía [`bootstrap/cd-ecr`](../../bootstrap/cd-ecr/README.md) | Repositorios permanentes y compartidos; aquí solo se leen (`data "aws_ecr_repository"`) |
| Borde | [`apigateway`](../apigateway/README.md), [`cognito`](../cognito/README.md), [`alb`](../alb/README.md) | Usage plans y API keys por socio (D-7); user pool con authorizer; ALB interno en subredes privadas, solo alcanzable desde el VPC link |
| Cómputo | [`ecs`](../ecs/README.md) | Un servicio ECS por microservicio y por stub; autoescalado por CPU al 60 % (ADR-02); tarea `db-init` de un solo uso |
| Datos | [`rds`](../rds/README.md), [`elasticache`](../elasticache/README.md) | PostgreSQL y Redis en subredes privadas; Multi-AZ por variable |
| Eventos | [`messaging`](../messaging/README.md) | Tópicos `origination` y `claims`; colas `origination`, `audit`, `notifications` con DLQ ([catálogo](../../../../docs/events/README.md)) |
| Objetos | [`s3`](../s3/README.md) | `documents` (documento contractual, HU-W1.3-03) y `evidence` (evidencia de siniestros) |
| Secretos | [`secrets`](../secrets/README.md) | Un secreto por servicio con su `DATABASE_URL` |

## Una base por servicio, un usuario por servicio

Una sola instancia RDS (costo) con aislamiento lógico:

- El módulo `rds` genera una contraseña por servicio; `secrets` guarda en `<nombre>/<servicio>/database` un JSON con `username`
  (`<servicio>_app`), `password`, `dbname`, `host`, `port` y `DATABASE_URL` (`sslmode=require`).
- Cada tarea recibe **solo su** `DATABASE_URL` (campo `secrets` de ECS, clave `DATABASE_URL`). El rol de ejecución puede leer
  únicamente los secretos referenciados.
- La clave del administrador la gestiona RDS en Secrets Manager (nunca llega al estado) y solo la lee la tarea `db-init`, que crea
  roles y bases y le quita el `CONNECT` a `PUBLIC`. Se lanza con la salida `db_init_command`.

Hoy ningún servicio usa PostgreSQL todavía (adaptadores en memoria); la variable ya les llega para cuando lo hagan.

## Rutas

| Servicio | Prefijos en el ALB | Estado |
|---|---|---|
| `origination` | `/v1/risk-profiles*`, `/v1/offers*`, `/v1/acceptances*`, `/v1/assisted-quotes*`, `/v1/customers*`, `/v1/policies*`, `/v1/documents*` | Implementados |
| `claims`, `partners`, `audit`, `notifications` | `/v1/claims*`, `/v1/partners*`, `/v1/audit*`, `/v1/notifications*` | Reservados (solo `GET /health`); un target group necesita al menos una regla |

En API Gateway las rutas de socio son las de cotización (`/v1/risk-profiles/...`, API key) y las humanas por defecto son `offers`,
`acceptances`, `assisted-quotes`, `customers` y `policies` (authorizer de Cognito; variable `human_routes`). `GET /v1/documents/{token}`
es un enlace firmado sin sesión y el módulo `apigateway` aún no publica rutas públicas: queda fuera del gateway hasta entonces.

## Imágenes

`backend-cd` publica `solventa/<imagen>:<sha de 12>` y solo reconstruye las imágenes que cambiaron. Por eso:

- `image_tag`: etiqueta para todas; `image_tag_overrides`: etiqueta por imagen.
- `image_tag = null`: cada servicio usa la imagen más reciente de su repositorio, **fijada por digest** (un *push* posterior no
  cambia una tarea en ejecución; el siguiente `apply` sí la actualiza).

## Variables de entorno de `origination`

`PORT`, `REDIS_ADDR` (endpoint de ElastiCache), `OPEN_FINANCE_URL` / `OPEN_DATA_URL` / `KYC_URL` (stubs por Cloud Map, puertos
9000/9010/9020), `AUTH_ISSUER`, `AUTH_JWKS_URL` y `AUTH_CLIENT_IDS` (user pool del ambiente) y `DATABASE_URL`. Sin `EMAIL_URL`, el
documento contractual (HU-W1.3-03) queda apagado: necesita el stub de correo y S3 en el servicio.

## Pendientes conocidos

- **Redis sin TLS:** el cliente de `origination` no admite TLS ni contraseña; ElastiCache se cifra en reposo y el security group
  solo admite las tareas.
- **Variables de mensajería y S3:** los permisos IAM ya existen (publicar, consumir, leer y escribir objetos), pero no se inyectan
  nombres de tópicos, colas ni buckets porque ningún servicio los lee aún; se agregan con HA-D02 / HU-W1.3-02.
- **Caché del stage:** el módulo `apigateway` la crea siempre (≈ US$ 0,02/h).

## Variables

| Variable | Defecto | Descripción |
|---|---|---|
| `name` | — | Prefijo (≤ 18 caracteres) |
| `environment` | — | `staging` o `production-demo` (también es el stage) |
| `vpc_cidr` | — | CIDR `/16` |
| `nat_mode` | `"none"` | `none`, `single`, `per_az` |
| `image_tag` / `image_tag_overrides` | `null` / `{}` | Ver «Imágenes»; las claves de `image_tag_overrides` son las 10 imágenes (5 servicios y 5 stubs) |
| `services_enabled` | `true` | `false` deja todo en 0 tareas |
| `service_sizing` | — | CPU, memoria, `desired_count` y `max_count` de los cinco servicios |
| `stub_desired_count` | `1` | Tareas de cada stub (`openfinance`, `opendata`, `kyc`, `email`, `payment-gateway`) |
| `stub_desired_count_overrides` | `{}` | Tareas por stub que reemplazan a `stub_desired_count`; una ventana de staging pone `payment-gateway = 0` para tumbar la pasarela y `{}` para restaurarla (HA-D02, D-6): solo cambia ese servicio ECS |
| `log_retention_days` | `7` | Retención de logs |
| `rds_instance_class` / `rds_multi_az` / `rds_backup_retention_days` | `db.t4g.micro` / `false` / `1` | PostgreSQL |
| `redis_node_type` / `redis_multi_az` | `cache.t4g.micro` / `false` | Redis |
| `partners` | `socio-a`, `socio-b` (100 req/s, ráfaga 200) | Usage plans (D-7) |
| `human_routes` | rutas de `origination` | Rutas detrás de Cognito |
| `cors_allow_origin` | `"*"` | Origen de las rutas humanas |
| `cache_ttl_seconds` | `60` | TTL del `GET` cacheado (D-6) |
| `deletion_protection` | `true` | RDS, ALB y user pool |
| `ephemeral` | `false` | Sin snapshot final, buckets con `force_destroy`, secretos sin ventana de recuperación |

Salidas: `invoke_url`, `api_key_ids`, `api_key_values` (sensible), `usage_plan_ids`, `cognito`, `alb_dns_name`, `ecs_cluster`,
`log_group`, `desired_counts`, `database_users`, `images`, `rds_address`, `redis_endpoint`, `database_secret_arns`, `topic_arns`,
`queue_urls`, `dlq_urls`, `buckets`, `nat_gateway_ids` y `db_init_command`.

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform test
```

`tests/platform.tftest.hcl` comprueba de punta a punta los usage plans de D-7, un secreto y un usuario por servicio, el script de
`db-init`, los tres modos de NAT, la resolución de imágenes y el apagado de los servicios.
