# Módulo `platform` — plataforma completa de un ambiente (T4)

Compone los módulos vecinos en la plataforma persistente de un ambiente. Lo llaman
[`staging/platform`](../../environments/staging/platform/README.md) y
[`production-demo/platform`](../../environments/production-demo/platform/README.md), que solo fijan los valores de cada
ambiente.

```
cliente ──HTTPS──▶ API Gateway REST (stage = ambiente; todos los tipos de contenido como binarios, D-4)
   │  rutas de socio: API key + usage plan por socio (D-7: 100 req/s, ráfaga 200)
   │  rutas humanas: authorizer de Cognito (valida el JWT)
   │  ruta pública: GET /v1/documents/{token} (enlace firmado del correo; 10 req/s, ráfaga 20, D-5)
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
| Secretos | [`secrets`](../secrets/README.md) | Un secreto por servicio con su `DATABASE_URL`, y `origination/document-link` con la clave de los enlaces del documento contractual |

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
| `origination` | `/v1/risk-profiles*`, `/v1/offers*`, `/v1/acceptances*`, `/v1/assisted-quotes*`, `/v1/customers*`, `/v1/policies*`, `/v1/documents*`, `/v1/review-requests*`, `/v1/issuance-exceptions*` (9 prefijos, 2 reglas del ALB) | Implementados |
| `claims`, `partners`, `audit`, `notifications` | `/v1/claims*`, `/v1/partners*`, `/v1/audit*`, `/v1/notifications*` | Reservados (solo `GET /health`); un target group necesita al menos una regla |

En API Gateway las rutas de socio son las de cotización (`/v1/risk-profiles/...`, API key) y las humanas por defecto son `offers`,
`acceptances` (`GET` y `POST`: medio de pago y reanudación del cobro), `assisted-quotes`, `customers`, `policies`, `review-requests`
(bandeja del asesor, HU-W1.1-02: `GET` y `POST`) e `issuance-exceptions` (excepciones de emisión de operaciones, HA-D02: `GET`), con el
authorizer de Cognito (variable `human_routes`).

`GET /v1/documents/{token}` es el enlace firmado del correo de emisión (HU-W1.3-03): se abre **sin sesión y sin API key** porque el
token firmado y con vencimiento lo protege (variable `public_routes`, HA-D02). Como ningún usage plan lo cubre, el stage lo limita a
10 req/s con ráfaga de 20 (D-5). Para que el PDF llegue íntegro a cualquier cliente (navegador, `curl`, la descarga autenticada del
front con `responseType: 'blob'`), la API declara `*/*` como tipo binario (D-4): las integraciones `HTTP_PROXY` pasan los bytes sin
tocarlos (también el JSON) y el *preflight* `MOCK` convierte su cuerpo a texto para seguir respondiendo `200`.

## Imágenes

`backend-cd` publica `solventa/<imagen>:<sha de 12>` y solo reconstruye las imágenes que cambiaron. Por eso:

- `image_tag`: etiqueta para todas; `image_tag_overrides`: etiqueta por imagen.
- `image_tag = null`: cada servicio usa la imagen más reciente de su repositorio, **fijada por digest** (un *push* posterior no
  cambia una tarea en ejecución; el siguiente `apply` sí la actualiza).

## Variables de entorno de `origination`

| Variable | Valor | Para qué |
|---|---|---|
| `PORT` | `8080` | Puerto del contenedor |
| `REDIS_ADDR` | Endpoint de ElastiCache | Caché del perfil de riesgo |
| `OPEN_FINANCE_URL`, `OPEN_DATA_URL`, `KYC_URL` | Stubs por Cloud Map (9000, 9010, 9020) | Perfilamiento en paralelo (HA-L01) |
| `EMAIL_URL` | Stub `email` por Cloud Map (9030) | Correo de emisión y de la bandeja; con él se enciende el documento contractual (HU-W1.3-03) |
| `PAYMENT_GATEWAY_URL` | Stub `payment-gateway` por Cloud Map (9040) | Enciende el cobro de la prima (HA-D02); exige `DATABASE_URL`, `EMAIL_URL` y `AUTH_ISSUER` |
| `AUTH_ISSUER`, `AUTH_JWKS_URL`, `AUTH_CLIENT_IDS` | User pool del ambiente | Rutas humanas (HA-S01) |
| `DOCUMENTS_BUCKET` | Bucket `documents` del ambiente | Repositorio del documento contractual en S3 (HA-D02, fase AWS); el servicio comprueba el bucket al arrancar |
| `DOCUMENTS_PUBLIC_BASE_URL` | `invoke_url` del stage de API Gateway | Base del enlace firmado del correo: `<invoke_url>/v1/documents/<token>` |
| `AWS_REGION` | Región del proveedor (`us-east-2`) | Región del SDK de AWS (S3) |
| `DATABASE_URL` (secreto) | `<nombre>/origination/database` | Base propia: cobro de la prima (HA-D02) y ofertas personalizadas (HU-W1.2-03) |
| `DOCUMENT_LINK_SECRET` (secreto) | `<nombre>/origination/document-link`, 48 caracteres aleatorios (`random_password`) | Firma de los enlaces del documento; nunca va como variable en claro |

`origination_environment` agrega variables para una ventana de prueba (por ejemplo `RETRY_WINDOW_SECONDS` y `CHARGE_RETRY_MAX_MS`,
D-6 de HA-D02); su validación impide reemplazar las de la tabla (`PORT`, `REDIS_ADDR`, `AWS_REGION`, `DATABASE_URL`, cualquier
`*_URL`, `AUTH_*`, `DOCUMENTS_*`, `DOCUMENT_LINK_*`). `DOCUMENTS_DIR` nunca llega en AWS: con `EMAIL_URL`, originación exige
exactamente uno de `DOCUMENTS_BUCKET` o `DOCUMENTS_DIR`. Como las contraseñas de las bases, la clave de los enlaces queda también en
el estado remoto cifrado del bucket de T4.

## Pendientes conocidos

- **Redis sin TLS:** el cliente de `origination` no admite TLS ni contraseña; ElastiCache se cifra en reposo y el security group
  solo admite las tareas.
- **Variables de mensajería:** los permisos IAM ya existen (publicar y consumir), pero no se inyectan nombres de tópicos ni colas
  porque ningún servicio los lee aún (los eventos `ChargeCompleted`/`PolicyIssued` con *outbox* son de HU-W1.3-02). El bucket
  `documents` sí se inyecta en `origination` desde HA-D02 (`DOCUMENTS_BUCKET`).
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
| `origination_environment` | `{}` | Variables extra de `origination` para una ventana de prueba; no puede reemplazar las de la plataforma |
| `log_retention_days` | `7` | Retención de logs |
| `rds_instance_class` / `rds_multi_az` / `rds_backup_retention_days` | `db.t4g.micro` / `false` / `1` | PostgreSQL |
| `redis_node_type` / `redis_multi_az` | `cache.t4g.micro` / `false` | Redis |
| `partners` | `socio-a`, `socio-b` (100 req/s, ráfaga 200) | Usage plans (D-7) |
| `human_routes` | rutas de `origination` | Rutas detrás de Cognito |
| `public_routes` | `documents` (`GET`, 10 req/s, ráfaga 20) | Rutas sin sesión ni API key (enlace firmado del documento, HA-D02) |
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
