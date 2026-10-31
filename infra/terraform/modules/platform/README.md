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
   │  ruta pública: POST /pasarela/tokens (tokenización de la tarjeta en E4; 5 req/s, ráfaga 10, T5)
   │  ruta pública: POST /v1/registrations (registro del asegurado; 2 req/s, ráfaga 5, HU-M1.1-01)
   └──VPC Link V2──▶ ALB interno (health check EXP-D03, deregistration delay 30 s)
        ├──/tokens──▶ stub payment-gateway (solo la tokenización, T5)
        └──por ruta──▶ ECS Fargate: origination · claims · partners · audit · notifications
                          ├─▶ RDS PostgreSQL (una base y un usuario por servicio)
                          ├─▶ ElastiCache Redis
                          ├─▶ SNS (un tópico por dominio) ─▶ SQS por consumidor + DLQ
                          ├─▶ S3 (documents, evidence)
                          ├─Cloud Map─▶ audit (origination verifica el consentimiento, HU-W1.2-01, T5)
                          └─Cloud Map─▶ stubs openfinance · opendata · kyc · email · payment-gateway · policy-core
```

| Pieza | Módulo | Configuración |
|---|---|---|
| Red | [`network`](../network/README.md) | VPC en dos AZ; `nat_mode` (`none` / `single` / `per_az`) decide dónde corren las tareas |
| ECR | [`ecr`](../ecr/README.md) vía [`bootstrap/cd-ecr`](../../bootstrap/cd-ecr/README.md) | Repositorios permanentes y compartidos; aquí solo se leen (`data "aws_ecr_repository"`) |
| Borde | [`apigateway`](../apigateway/README.md), [`cognito`](../cognito/README.md), [`alb`](../alb/README.md) | Usage plans y API keys por socio (D-7); user pool propio o externo (`existing_cognito`) con authorizer; ALB interno en subredes privadas, solo alcanzable desde el VPC link |
| Cómputo | [`ecs`](../ecs/README.md) | Un servicio ECS por microservicio y por stub; autoescalado por CPU al 60 % (ADR-02); tarea `db-init` de un solo uso |
| Datos | [`rds`](../rds/README.md), [`elasticache`](../elasticache/README.md) | PostgreSQL y Redis en subredes privadas; Multi-AZ por variable |
| Eventos | [`messaging`](../messaging/README.md) | Tópicos `origination` y `claims`; colas `origination`, `audit`, `notifications` con DLQ ([catálogo](../../../../docs/events/README.md)) |
| Objetos | [`s3`](../s3/README.md) | `documents` (documento contractual, HU-W1.3-03) y `evidence` (evidencia de siniestros) |
| Secretos | [`secrets`](../secrets/README.md) | Un secreto por servicio con su `DATABASE_URL`, y `origination/document-link` con la clave de los enlaces del documento contractual |

## User pool propio o externo

El user pool de los canales humanos puede venir de fuera de la plataforma o crearlo el módulo:

- **Externo** (`existing_cognito` con `user_pool_id`, `user_pool_arn`, `issuer`, `jwks_url` y `client_ids`): la plataforma no crea
  ningún pool y usa el recibido en el authorizer del API Gateway, en `AUTH_ISSUER`, `AUTH_JWKS_URL` y `AUTH_CLIENT_IDS` de los
  servicios y en la salida `cognito`. Las dos composiciones del repositorio pasan el **pool permanente** de
  [`bootstrap/identity`](../../bootstrap/identity/README.md), que leen por `terraform_remote_state`: el portal y la app conservan
  el mismo pool, los mismos clientes y los mismos usuarios de prueba en todas las ventanas, y un `destroy` del ambiente no lo toca.
- **Propio** (`existing_cognito = null`, el valor por defecto): la plataforma crea su pool con el módulo [`cognito`](../cognito/README.md)
  (nombre = `name`, candado según `deletion_protection`) y, si se piden, sus usuarios semilla (`seed_users`, `seed_user_password`).

La variable se valida: el ARN debe ser de un user pool de Cognito, el emisor debe terminar en el ID del pool, la URL de JWKS debe ser
la de ese emisor y debe haber al menos un cliente. Con un pool externo, `seed_users` y `seed_user_password` se rechazan: los usuarios de
prueba se declaran donde se administra ese pool (`bootstrap/identity`). Esa validación compara dos variables y por eso el módulo exige
**Terraform 1.9 o superior** (las composiciones ya piden 1.11). El módulo `cognito` se crea con `count`; un bloque `moved` traslada
la dirección sin índice a `module.cognito[0]`, así que un estado creado antes de `existing_cognito` conserva su pool con el valor por
defecto.

## Una base por servicio, un usuario por servicio

Una sola instancia RDS (costo) con aislamiento lógico:

- El módulo `rds` genera una contraseña por servicio; `secrets` guarda en `<nombre>/<servicio>/database` un JSON con `username`
  (`<servicio>_app`), `password`, `dbname`, `host`, `port` y `DATABASE_URL` (`sslmode=require`).
- Cada tarea recibe **solo su** `DATABASE_URL` (campo `secrets` de ECS, clave `DATABASE_URL`). El rol de ejecución puede leer
  únicamente los secretos referenciados.
- La clave del administrador la gestiona RDS en Secrets Manager (nunca llega al estado) y solo la lee la tarea `db-init`, que crea
  roles y bases y le quita el `CONNECT` a `PUBLIC`. Se lanza con la salida `db_init_command`.

Ya usan su base el cobro de la prima y las ofertas personalizadas de `origination` (HA-D02, HU-W1.2-03) y el consentimiento de
`audit` (HU-W1.2-01); los dos abren su base y aplican sus migraciones **al arrancar**, así que necesitan que `db-init` haya creado
su rol antes (ver «Primer `apply` en dos tiempos»). Los demás servicios reciben la variable para cuando la usen.

## Primer `apply` en dos tiempos

Los roles `<servicio>_app` los crea `db-init` **después** del `apply`, pero `origination` (con `PAYMENT_GATEWAY_URL`) y `audit` abren
PostgreSQL al arrancar: si arrancan antes, caen en bucle y el *circuit breaker* de despliegue de ECS puede dar el despliegue por
fallido. Por eso un ambiente nuevo se levanta así:

1. `apply` con `services_enabled = false` (todo menos las tareas);
2. `db-init` con el comando literal de `terraform output -raw db_init_command` (sin `eval`), y esperar a que la tarea termine con
   código 0;
3. `apply` con `services_enabled = true` (solo cambian los `desired_count` y el autoescalado).

## Una tarea de originación (C5)

`origination` corre con **una sola tarea** (`desired_count = max_count = 1`, sin autoescalado) en `staging` y en `production-demo`
mientras las aceptaciones, la bandeja del asesor y la emisión contractual sigan en memoria (C5 de la bitácora, ligado a
HU-W1.2-03): con dos tareas, una aceptación registrada en una no existiría en la otra y la bandeja o el cobro la perderían. Es una
**desviación temporal de ADR-02** (3 tareas), decidida en HA-D02 (#54) e informada al equipo; se vuelve a 3 tareas y al autoescalado
cuando esos datos estén en PostgreSQL. Un reinicio de la tarea borra lo que está en memoria (el cobro no: está en PostgreSQL).

## Rutas

| Servicio | Prefijos en el ALB | Estado |
|---|---|---|
| `origination` | `/v1/risk-profiles*`, `/v1/offers*`, `/v1/acceptances*`, `/v1/assisted-quotes*`, `/v1/customers*`, `/v1/policies*`, `/v1/documents*`, `/v1/review-requests*`, `/v1/issuance-exceptions*` (9 prefijos, 2 reglas del ALB) | Implementados |
| `audit` | `/v1/audit*` (bancos, API key) y `/v1/consents*` (personas, Cognito; HU-M1.3-01, D-3 a: un prefijo por recurso) | Consentimiento de HU-W1.2-01, consulta del asegurado de HU-M1.3-01 y su revocación de HU-M1.3-02 |
| `claims` | `/v1/claims*` | Radicación móvil (HU-M2.2-01) y web con soportes, listado, detalle y comprobante (HU-W2.1-01) |
| `partners`, `notifications` | `/v1/partners*`, `/v1/notifications*` | Reservados (solo `GET /health`); un target group necesita al menos una regla |
| stub `policy-core` | — (solo Cloud Map, puerto 9060) | Core de pólizas que consulta `claims` dentro de la VPC (HU-W2.1-01); el navegador nunca lo llama, por eso no está en `local.lb_stubs` |
| stub `payment-gateway` | `/tokens` (prioridad 6) | Tokenización de la tarjeta desde el navegador (T5, `local.lb_stubs`); el resto del simulador no se alcanza por el ALB |

En API Gateway las rutas de socio son las de cotización (`/v1/risk-profiles/...`, API key) y, desde T5 (variable `partner_routes`), el
consentimiento de HU-W1.2-01 (`GET /v1/audit/consent-terms/current` y `POST /v1/audit/consents`) y la oferta personalizada de HU-W1.2-03
(`POST /v1/offers`), también con la API key del socio y nunca con el authorizer. La verificación del consentimiento
(`GET /v1/audit/consent-verifications`) es interna de la VPC y **no se publica** (la variable lo rechaza). Las humanas por defecto son
`offers` (solo sus subrutas: `GET /v1/offers/{offer_id}`, `…/renewal` y `…/acceptance`; la raíz es del socio, `root_methods = []`),
`acceptances` (`GET` y `POST`: medio de pago y reanudación del cobro), `assisted-quotes`, `customers`, `policies`, `review-requests`
(bandeja del asesor, HU-W1.1-02: `GET` y `POST`), `issuance-exceptions` (excepciones de emisión de operaciones, HA-D02: `GET`),
y `consents` (`GET /v1/consents` y `GET /v1/consents/{consent_id}`: el asegurado consulta sus consentimientos, HU-M1.3-01; desde
HU-M1.3-02 también `POST /v1/consents/{consent_id}/revocations` y `GET /v1/consents/{consent_id}/revocation-receipt`, con
`root_methods = ["GET"]`: la raíz `/v1/consents` sigue de solo lectura), con el authorizer de Cognito (variable `human_routes`).
La caché del gateway está apagada para todo método salvo el `GET` de perfiles, así que `GET /v1/consents` nunca se comparte entre
titulares.

`GET /v1/documents/{token}` es el enlace firmado del correo de emisión (HU-W1.3-03): se abre **sin sesión y sin API key** porque el
token firmado y con vencimiento lo protege (variable `public_routes`, HA-D02). Como ningún usage plan lo cubre, el stage lo limita a
10 req/s con ráfaga de 20 (D-5). Para que el PDF llegue íntegro a cualquier cliente (navegador, `curl`, la descarga autenticada del
front con `responseType: 'blob'`), la API declara `*/*` como tipo binario (D-4): las integraciones `HTTP_PROXY` pasan los bytes sin
tocarlos (también el JSON) y el *preflight* `MOCK` convierte su cuerpo a texto para seguir respondiendo `200`.

## Portal web (T5)

El portal web se sirve desde CloudFront (composiciones `environments/<ambiente>/web`, módulo
[`web-portal`](../web-portal/README.md)) con el API en el **mismo dominio**. Para eso la plataforma aporta:

| Pieza | Qué hace |
|---|---|
| Registro público del asegurado | `POST /v1/registrations` en el gateway (sin sesión ni API key, 2 req/s, ráfaga 5) → ALB → originación. Es pública porque la cuenta todavía no existe y quien llama no tiene token; el *user pool* solo permite crear cuentas desde el servidor, así que quien decide crearla es originación (HU-M1.1-01) |
| Tokenización pública | `POST /pasarela/tokens` en el gateway (sin sesión ni API key, 5 req/s, ráfaga 10) → ALB `/tokens` → stub `payment-gateway`, que sigue también en Cloud Map para el cobro de originación (D-4 de T5). Su *target group* es `<nombre>-payment-gateway` (29 caracteres en `solventa-demo`) y el puerto 9040 se abre solo al ALB |
| Socio `portal-embebido` | API key y usage plan propios (10 req/s, ráfaga 20). CloudFront agrega su key a las rutas de socio de las pantallas públicas; el navegador nunca la ve (D-3 de T5) |
| Salida `api_origin` | Dominio del REST API, stage (el *origin path* de CloudFront) e `invoke_url` |
| Salida `portal_partner_api_key` | Key del socio `portal_partner` (sensible); solo la lee la composición `web` para el encabezado de origen de CloudFront |
| `audit` en Cloud Map | `audit` se registra también en Cloud Map (`local.discoverable_services`) sin dejar el ALB, para que originación verifique el consentimiento dentro de la VPC (`AUDIT_URL`, D-21 a de T5); el módulo `ecs` abre el 8080 entre las tareas. Desde HU-M1.3-01 `audit` recibe `AUTH_*` (para `GET /v1/consents*`); con ellas monta también la evidencia de HU-W1.2-01, que el gateway sigue sin publicar (D-23 c, HU-W4.2-02) |
| Salida `audit_url` | El `AUDIT_URL` de la definición de tarea de originación (`http://audit.<nombre>.local:8080`); lo comprueban las pruebas de las composiciones |
| Variable `portal_base_url` | Origen del portal (`https://<distribución>.cloudfront.net`). Con ella, los dos enlaces de los correos de originación apuntan al portal (D-11 de T5). La composición del ambiente la toma del parámetro SSM `/solventa/<ambiente>/web/url` |
| CORS del *bucket* de evidencia | Con `portal_base_url`, el *bucket* `evidence` acepta `PUT` y `GET` desde ese origen (módulo `s3`, `cors_allowed_origins`): el portal sube los soportes de la radicación web directo al *bucket* con URL firmada (HU-W2.1-01). El *bucket* `documents` no recibe CORS; sin portal tampoco hay orígenes que permitir |

## Variables de entorno de `audit` (HU-M1.3-01 y HU-M1.3-02)

| Variable | Valor | Para qué |
|---|---|---|
| `PORT` | `8080` | Puerto del contenedor |
| `AWS_REGION` | Región del proveedor (`us-east-2`) | Región del SDK de AWS (SQS) |
| `AUTH_ISSUER`, `AUTH_JWKS_URL`, `AUTH_CLIENT_IDS` | El mismo user pool y los mismos clientes de originación | `GET /v1/consents*` del asegurado (y la evidencia de HU-W1.2-01, no publicada) |
| `PARTNER_DISPLAY_NAMES` | `jsonencode(var.partner_display_names)` | Nombre visible de la entidad de cada consentimiento (D-2 a) |
| `EVENTS_QUEUE_URL` | Cola `audit` de `modules/messaging` | Consumo de `CustomerLinked`: así `audit` reconoce al titular de los consentimientos |
| `OPEN_FINANCE_URL` | `http://openfinance.<nombre>.local:9000` (Cloud Map, la misma dirección que usa originación) | Aviso de cada revocación a las entidades (HU-M1.3-02, D-2 a): el trabajador de `audit` llama al stub con la clave de idempotencia del aviso. El grupo de seguridad de las tareas ya abre el 9000 entre tareas: sin tópicos, colas ni reglas nuevas |
| `REVOCATION_NOTICE_DEADLINE_SECONDS` | `300` | Plazo de RN-02 para que cada entidad confirme; al vencer sin confirmación, la revocación queda parcial |
| `DATABASE_URL` (secreto) | `<nombre>/audit/database` | Base propia *append-only* |

El rol de la tarea ya tenía `ConsumeItsQueue` sobre la cola `audit` (T4).

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
| `AUDIT_URL` | `audit` por Cloud Map: `http://audit.<nombre>.local:8080` (T5, D-21 a) | Verificación del consentimiento antes del perfilamiento (HU-W1.2-01) y de la oferta personalizada (HU-W1.2-03). Si `audit` no responde en `AUDIT_TIMEOUT_MS` (300 ms por defecto, no se cambia) originación falla cerrado; sin la variable no verifica ni genera ofertas personalizadas |
| `AUTH_ISSUER`, `AUTH_JWKS_URL`, `AUTH_CLIENT_IDS` | User pool del ambiente: el permanente de `bootstrap/identity` (`existing_cognito`) o el que crea el módulo | Rutas humanas (HA-S01) |
| `COGNITO_USER_POOL_ID` | El mismo user pool | Monta el registro del asegurado (HU-M1.1-01). El rol de la tarea de originación recibe `AdminCreateUser`, `AdminSetUserPassword` y `AdminAddUserToGroup` **solo sobre ese pool**: no puede leer, listar ni borrar usuarios, y ningún otro servicio lo toca |
| `DOCUMENTS_BUCKET` | Bucket `documents` del ambiente | Repositorio del documento contractual en S3 (HA-D02, fase AWS); el servicio comprueba el bucket al arrancar |
| `DOCUMENTS_PUBLIC_BASE_URL` | `portal_base_url` si existe (T5, D-11); si no, `invoke_url` del stage de API Gateway | Base del enlace firmado del correo: `<base>/v1/documents/<token>`; con el portal, el PDF se abre por CloudFront |
| `OFFER_LINK_BASE_URL` | `portal_base_url`, solo si existe (T5, D-11) | Base de los enlaces de los correos de oferta hacia las pantallas del portal; sin portal, el servicio usa su valor por defecto (`http://localhost:4200`) |
| `AWS_REGION` | Región del proveedor (`us-east-2`) | Región del SDK de AWS (S3, SNS) |
| `EVENTS_TOPIC_ARN` | Tópico `origination` de `modules/messaging` | El relevo del *outbox* publica ahí `CustomerLinked` (HU-M1.3-01); el rol ya tenía `PublishItsEvents` sobre ese tópico (T4) |
| `DATABASE_URL` (secreto) | `<nombre>/origination/database` | Base propia: cobro de la prima (HA-D02) y ofertas personalizadas (HU-W1.2-03) |
| `DOCUMENT_LINK_SECRET` (secreto) | `<nombre>/origination/document-link`, 48 caracteres aleatorios (`random_password`) | Firma de los enlaces del documento; nunca va como variable en claro |

`origination_environment` agrega variables para una ventana de prueba (por ejemplo `RETRY_WINDOW_SECONDS` y `CHARGE_RETRY_MAX_MS`,
D-6 de HA-D02); su validación impide reemplazar las de la tabla (`PORT`, `REDIS_ADDR`, `AWS_REGION`, `DATABASE_URL`, cualquier
`*_URL`, `AUTH_*`, `DOCUMENTS_*`, `DOCUMENT_LINK_*`, `EVENTS_*`). `DOCUMENTS_DIR` nunca llega en AWS: con `EMAIL_URL`, originación exige
exactamente uno de `DOCUMENTS_BUCKET` o `DOCUMENTS_DIR`. Como las contraseñas de las bases, la clave de los enlaces queda también en
el estado remoto cifrado del bucket de T4.

## Variables de entorno de `claims`

| Variable | Valor | Para qué |
|---|---|---|
| `PORT` | `8080` | Puerto del contenedor |
| `AUTH_ISSUER`, `AUTH_JWKS_URL`, `AUTH_CLIENT_IDS` | User pool del ambiente | Sin ellas no se monta ninguna ruta humana (HU-M2.2-01) |
| `EVIDENCE_BUCKET` | Bucket `evidence` del ambiente | Evidencia del siniestro con URL firmada (HU-M2.1-01) |
| `AWS_REGION` | Región del proveedor (`us-east-2`) | Región del SDK de AWS |
| `POLICY_CORE_URL` | Stub `policy-core` por Cloud Map (9060) | Enciende la radicación web con soportes (HU-W2.1-01): opciones, subida de soportes y radicación. Sin ella `claims` no monta esas rutas y responde `404` |
| `DATABASE_URL` (secreto) | `<nombre>/claims/database` | Base propia de `claims` |

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
| `stub_desired_count` | `1` | Tareas de cada stub (`openfinance`, `opendata`, `kyc`, `email`, `payment-gateway`, `policy-core`) |
| `stub_desired_count_overrides` | `{}` | Tareas por stub que reemplazan a `stub_desired_count`; una ventana de staging pone `payment-gateway = 0` para tumbar la pasarela y `{}` para restaurarla (HA-D02, D-6): solo cambia ese servicio ECS |
| `origination_environment` | `{}` | Variables extra de `origination` para una ventana de prueba; no puede reemplazar las de la plataforma |
| `log_retention_days` | `7` | Retención de logs |
| `rds_instance_class` / `rds_multi_az` / `rds_backup_retention_days` | `db.t4g.micro` / `false` / `1` | PostgreSQL |
| `redis_node_type` / `redis_multi_az` | `cache.t4g.micro` / `false` | Redis |
| `partners` | `socio-a`, `socio-b` (100 req/s, ráfaga 200) y `portal-embebido` (10 req/s, ráfaga 20, T5) | Usage plans (D-7; el socio de demostración del portal, D-3 de T5) |
| `portal_partner` | `"portal-embebido"` | Socio cuya key recibe CloudFront (salida `portal_partner_api_key`, T5) |
| `portal_base_url` | `null` | Origen `https://` del portal web; apunta los enlaces de los correos al portal (T5, D-11) |
| `human_routes` | rutas de `origination`, `claims` (`GET` y `POST`, HU-W2.1-01) y `consents` (`GET` y `POST` con `root_methods = ["GET"]`, HU-M1.3-01 y HU-M1.3-02) | Rutas detrás de Cognito; `offers` con `root_methods = []` (la raíz es del socio, T5) |
| `partner_display_names` | `{ "aliado-banco-1" = "Banco Aliado" }` (el aliado de E1) | Catálogo de nombres visibles de las entidades para `audit` (`PARTNER_DISPLAY_NAMES`, HU-M1.3-01, D-2 a); claves `^[A-Za-z0-9_-]+$` y nombres no vacíos |
| `partner_routes` | `offers` (`POST`), `audit/consent-terms/current` (`GET`), `audit/consents` (`POST`) | Rutas de socio en rutas exactas, con API key (T5); `audit/consent-verifications` está prohibida |
| `public_routes` | `documents` (`GET`, 10 req/s, ráfaga 20) | Rutas sin sesión ni API key (enlace firmado del documento, HA-D02) |
| `existing_cognito` | `null` | User pool externo (`user_pool_id`, `user_pool_arn`, `issuer`, `jwks_url`, `client_ids`); `null` crea uno propio. Ver «User pool propio o externo» |
| `seed_users` / `seed_user_password` | `{}` / `null` | Usuarios de prueba del pool que crea la plataforma, solo con `existing_cognito = null` (contraseña solo por `TF_VAR_seed_user_password`); las composiciones del repositorio usan los usuarios permanentes de `bootstrap/identity` |
| `cors_allow_origin` | `"*"` | Origen de las rutas humanas |
| `cache_ttl_seconds` | `60` | TTL del `GET` cacheado (D-6) |
| `deletion_protection` | `true` | RDS, ALB y el user pool cuando lo crea la plataforma |
| `ephemeral` | `false` | Sin snapshot final, buckets con `force_destroy`, secretos sin ventana de recuperación |

Salidas: `invoke_url`, `api_key_ids`, `api_key_values` (sensible), `usage_plan_ids`, `api_origin` (T5), `portal_partner_api_key`
(sensible, T5), `cognito`, `audit_url` (T5), `alb_dns_name`, `ecs_cluster`, `log_group`, `service_sizing`, `desired_counts`, `database_users`, `images`,
`rds_address`, `redis_endpoint`, `database_secret_arns`, `topic_arns`, `queue_urls`, `dlq_urls`, `buckets`, `nat_gateway_ids` y
`db_init_command`.

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform test
```

`tests/platform.tftest.hcl` comprueba de punta a punta los usage plans de D-7, un secreto y un usuario por servicio, el script de
`db-init`, los tres modos de NAT, la resolución de imágenes y el apagado de los servicios. También los dos modos del user pool: sin
`existing_cognito` la plataforma crea el suyo; con un pool externo no crea ninguno y lo lleva al authorizer, a `AUTH_*` y a la salida
`cognito`; y la variable rechaza usuarios semilla junto a un pool externo y un ARN que no sea de Cognito.
Desde T5 comprueba también que el stub de la
pasarela queda detrás del ALB solo en `/tokens`, que `portal-embebido` tiene su propio usage plan, que `api_origin` corresponde al
stage y que, con `portal_base_url`, los enlaces de los correos apuntan al portal (sin él, quedan como antes). Y que `audit` es el
único servicio registrado en Cloud Map, que originación recibe su `AUDIT_URL` sin `AUDIT_TIMEOUT_MS`, y que
`origination_environment` no puede reemplazarlo. Las rutas de socio: el consentimiento y `POST /v1/offers` se publican con API key, la
raíz de `/v1/offers` queda sin métodos humanos y sus subrutas con Cognito, y `audit/consent-verifications` no se publica ni puede
volverse ruta de socio. Desde HU-M1.3-01: `consents` es ruta humana (desde HU-M1.3-02, `GET` y `POST` con la raíz solo `GET`), el ALB lleva `/v1/consents*` a `audit`, `audit`
recibe `AUTH_*` del mismo pool, `AWS_REGION`, el catálogo JSON y `EVENTS_QUEUE_URL` de su cola, originación recibe `EVENTS_TOPIC_ARN`
de su tópico, el catálogo se puede reemplazar y rechaza códigos inválidos, y `origination_environment` no puede fijar `EVENTS_TOPIC_ARN`.
Desde HU-M1.3-02: `audit` recibe `OPEN_FINANCE_URL` (la misma dirección del stub que originación) y el plazo de 300 s.
