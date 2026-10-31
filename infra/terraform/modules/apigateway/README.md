# Módulo `apigateway` — API Gateway REST con caché y rate limiting por socio (HA-L01)

API REST regional delante del servicio `origination` para el recorrido de cotización embebida.

| Ruta | Método | Caché | Para qué |
|---|---|---|---|
| `/v1/risk-profiles/resolve` | `POST` | no (la clave de caché no puede incluir el cuerpo) | Recorrido de cotización |
| `/v1/risk-profiles/{customer_id}?consent_active=` | `GET` | sí, TTL `cache_ttl_seconds` (60 s, decisión D-6) | Misma respuesta, servida sin recalcular (CA-4) |

- **Aislamiento por socio (CA-4):** la clave de caché del `GET` es `customer_id` (ruta) + `consent_active` (query) + el
  encabezado `x-api-key`. Dos socios que piden el mismo cliente nunca comparten la entrada.
- **Rate limiting (CA-5):** cada socio de `partners` tiene su API key y su *usage plan* con `rate_limit` / `burst_limit`
  (decisión D-7: 100 req/s, ráfaga 200). Todas las rutas de cotización exigen API key; un socio por encima de su límite recibe `429` sin
  afectar a los demás.
- **Integración:** `HTTP_PROXY` con timeout de 5 s (ADR-02 / EXP-D03). Con `vpc_link_v2_id` e `integration_target_arn` es una
  integración privada a un ALB interno por **VPC Link V2** (soportado en REST desde el proveedor `hashicorp/aws` 6.x, atributo
  `integration_target`); `backend_base_url` fija el encabezado `Host` y la ruta. Sin VPC link, la integración es pública
  (`INTERNET`), útil solo para pruebas.
- La caché del stage se cobra por hora mientras exista (0,5 GB ≈ US$ 0,02/h): el módulo es para ventanas efímeras de staging
  y se destruye en la misma sesión (regla R12 del equipo de Nicolás, `infra/COSTOS.md` del harness).

## Uso

```hcl
module "apigateway" {
  source                 = "../../../modules/apigateway"
  name                   = "solventa-staging-ha-l01"
  backend_base_url       = "http://${aws_lb.internal.dns_name}"
  vpc_link_v2_id         = aws_apigatewayv2_vpc_link.this.id
  integration_target_arn = aws_lb.internal.arn
}
```

| Variable | Defecto | Descripción |
|---|---|---|
| `name` | — | Nombre del API |
| `stage_name` | `staging` | Stage |
| `backend_base_url` | — | URL base del servicio vista por la integración |
| `vpc_link_v2_id` / `integration_target_arn` | `null` | VPC Link V2 y ARN del ALB interno |
| `integration_timeout_ms` | `5000` | Timeout de integración (ADR-02) |
| `cache_cluster_size` | `"0.5"` | Tamaño de la caché del stage (GB) |
| `cache_ttl_seconds` | `60` | TTL del `GET` cacheado (≤ 300 s por la revocación de consentimiento, HU-W1.2-01 CA-5) |
| `partners` | `socio-a`, `socio-b` (100 req/s, ráfaga 200) | Socios de prueba con su API key y usage plan |
| `tags` | `{}` | Etiquetas adicionales |
| `cognito_user_pool_arns` | `[]` | User pools de los canales humanos (HA-S01); vacío = API solo de socios |
| `human_routes` | `{}` | Rutas humanas bajo `/v1` con sus métodos (HA-S01); `root_methods` opcional fija los métodos de la raíz (T5) |
| `cors_allow_origin` | `"*"` | Origen permitido en el *preflight* y en las respuestas 4XX del gateway (HA-S01) |
| `public_routes` | `{}` | Rutas de solo lectura sin sesión ni API key, con límite opcional por método (HA-D02) |
| `binary_media_types` | `[]` | Tipos de contenido que la API trata como binarios (HA-D02) |
| `payment_tokenization` | `null` | Ruta pública `POST /pasarela/tokens` de la pasarela simulada, con `backend_path` (`/tokens`), `rate_limit` (5) y `burst_limit` (10) (T5) |
| `registration_route` | `null` | Ruta pública `POST /v1/registrations` del registro del asegurado, con `backend_path` (`/v1/registrations`), `rate_limit` (2) y `burst_limit` (5) (HU-M1.1-01) |
| `partner_routes` | `{}` | Rutas de socio en rutas exactas bajo `/v1` (1 a 3 segmentos), con API key y sin authorizer (T5) |
| `human_subroutes` | `{}` | Subrutas humanas bajo una ruta de socio: la llave es la de una `partner_routes` (p. ej. `audit/consents`) y solo se crea su `{proxy+}`, con Cognito y *preflight* (HU-W4.2-02). `root_methods` (opcional, `[]` por defecto, HU-W4.2-01) agrega métodos humanos en la ruta exacta de esa ruta de socio, junto a sus métodos con API key |

Salidas: `rest_api_id`, `stage_name`, `invoke_url`, `usage_plan_ids`, `api_key_ids`, `api_key_values` (sensible; claves de
prueba para los guiones k6 de la ventana), `public_paths` (HA-D02), `cognito_authorizer_id` (HA-S01; `null` sin user pool) y
`payment_tokenization_path` (T5; `null` sin la tokenización pública), `registration_path` (HU-M1.1-01; `null` sin la ruta de registro) y `partner_paths` (T5; rutas de socio en rutas exactas).

## Canales humanos: authorizer de Cognito (HA-S01)

Con `cognito_user_pool_arns` (salida `user_pool_arn` del módulo [`cognito`](../cognito/README.md)) el módulo agrega:

| Elemento | Configuración | Criterio |
|---|---|---|
| Authorizer | `COGNITO_USER_POOLS` sobre `method.request.header.Authorization`: valida firma y vigencia del JWT antes de llegar al servicio; sin token, con token inválido o vencido responde `401` | CA-1 |
| Rutas humanas (`human_routes`) | Cada clave es un segmento bajo `/v1` (p. ej. `quotes` → `/v1/quotes` y `/v1/quotes/{proxy+}`), con `authorization = "COGNITO_USER_POOLS"` y `api_key_required = false`, integradas por `HTTP_PROXY` (mismo VPC link y timeout) a la misma ruta del servicio. La cabecera `Authorization` llega al servicio, que vuelve a verificar el JWT con `pkg/security` y autoriza por rol y titularidad (D-1) | CA-1, CA-2 |
| Rutas de socio | Las de cotización siguen con `authorization = "NONE"` y `api_key_required = true`; nunca usan el authorizer. `risk-profiles` no puede declararse como ruta humana | CA-5 |
| *Preflight* CORS | `OPTIONS` sin autorización ni API key, respondido por el gateway (`MOCK`) con los métodos de la ruta y las cabeceras `Authorization,Content-Type` | — |
| Respuestas del gateway | `UNAUTHORIZED`, `ACCESS_DENIED`, `EXPIRED_TOKEN` y `DEFAULT_4XX` con cabeceras CORS y el cuerpo por defecto (`{"message": …}`), para que el navegador pueda leer el `401` y el cliente web lo trate como sesión expirada. `ACCESS_DENIED` responde **401** en lugar del 403 por defecto: con solo el authorizer de Cognito, AWS lo usa cuando un token del pool llega con la firma o el contenido alterados (verificado en la ventana de staging de HA-S01), y CA-1 pide 401 para todo token inválido. La falta de API key (`INVALID_API_KEY`) sigue en 403. Si se agrega una política de recursos u otro authorizer, revisar este mapeo | — |

Una ruta humana sin `cognito_user_pool_arns` hace fallar el `plan` (*precondition*): nunca se publica una ruta humana sin
authorizer. Las respuestas `2XX`/`5XX` de las rutas `HTTP_PROXY` llevan las cabeceras CORS que ponga el servicio. Sin
`cognito_user_pool_arns` no se crea nada de esta sección y el *hash* de redespliegue no cambia.

```hcl
module "apigateway" {
  source                 = "../../../modules/apigateway"
  name                   = "solventa-staging"
  backend_base_url       = "http://${aws_lb.internal.dns_name}"
  vpc_link_v2_id         = aws_apigatewayv2_vpc_link.this.id
  integration_target_arn = aws_lb.internal.arn
  cognito_user_pool_arns = [module.cognito.user_pool_arn]
  human_routes           = { quotes = { methods = ["GET", "POST"] } } # ejemplo: la primera ruta humana llega con HU-W1.1-01
  cors_allow_origin      = "https://<origen del portal>"
}
```

## Rutas públicas (enlace firmado de HU-W1.3-03)

El correo de emisión lleva un enlace al documento contractual, `GET /v1/documents/{token}`, que el asegurado abre desde el correo:
**no hay sesión** ni API key. Lo protege el token, firmado con HMAC y con vencimiento, que el servicio verifica (`403 invalid_link` si
está alterado o vencido). `public_routes` publica esas rutas (HA-D02):

| Elemento | Configuración |
|---|---|
| Recursos | Cada clave es un segmento bajo `/v1` (p. ej. `documents` → `/v1/documents` y `/v1/documents/{proxy+}`), integrado por `HTTP_PROXY` (mismo VPC link y timeout) a la misma ruta del servicio |
| Métodos | Solo `GET`, con `authorization = "NONE"` y `api_key_required = false` |
| Límite | `rate_limit` / `burst_limit` (req/s) por método en el stage (`aws_api_gateway_method_settings`), porque ningún usage plan cubre una ruta sin API key; sin caché |
| Reglas | Una clave no puede estar a la vez en `public_routes` y en `human_routes` (*precondition*); `risk-profiles` nunca es pública; solo `GET` |

```hcl
public_routes = { documents = { methods = ["GET"], rate_limit = 10, burst_limit = 20 } } # D-5 de HA-D02
```

## Contenido binario

Una API REST sin tipos binarios trata el cuerpo como texto y **corrompe un PDF**. `binary_media_types` declara los tipos que pasan
como binarios. La plataforma usa `["*/*"]` (D-4 de HA-D02): así el PDF llega íntegro aunque el cliente no pida
`application/pdf` (`curl`, la descarga del front con `responseType: 'blob'`, cualquier navegador), y con `HTTP_PROXY` el JSON también
pasa sin cambios. Con una lista no vacía, el *preflight* `MOCK` usa `content_handling = "CONVERT_TO_TEXT"` para que su plantilla
siga respondiendo `200`.

Con los valores por defecto (`public_routes = {}`, `binary_media_types = []`) no se crea nada de estas dos secciones, la API queda
igual y el *hash* de redespliegue no cambia: `staging/ha-l01` y `staging/ha-s01` no se ven afectados.

## Tokenización pública de la pasarela simulada (T5)

En el paso E4 el navegador manda la tarjeta **directo a la pasarela** y solo el token pasa por Solventa (PCI; contrato
[HU-W1.3-02](../../../../docs/contratos/HU-W1.3-02-api.md) §2 y §8). En AWS el *stub* `payment-gateway` solo se alcanzaba por Cloud
Map; `payment_tokenization` lo publica en el gateway:

| Elemento | Configuración |
|---|---|
| Recurso | `/pasarela/tokens`, colgado de la raíz y **fuera de `/v1`**: el prefijo lo fijan el contrato y el *proxy* local del cliente web (`proxy.conf.json`) |
| Método | Solo `POST`, con `authorization = "NONE"` y `api_key_required = false`. Ninguna otra ruta del simulador queda expuesta (`/charges` y `/debug/*` responden `403`) |
| Integración | `HTTP_PROXY` (mismo VPC link y timeout) a `<backend_base_url><backend_path>`; la plataforma pone el *stub* detrás del ALB solo en `/tokens` |
| Límite | `rate_limit` / `burst_limit` (req/s; 5 y 10 por defecto, D-4 de T5) en el stage, porque ningún usage plan cubre una ruta sin API key; sin caché |

```hcl
payment_tokenization = {} # POST /pasarela/tokens → <ALB>/tokens, 5 req/s, ráfaga 10
```

Con `payment_tokenization = null` (defecto) no se crea nada, la API queda igual y el *hash* de redespliegue no cambia.

## Registro público del asegurado (HU-M1.1-01)

El registro ocurre **antes de que exista la cuenta**, así que quien llama no tiene token que enviar. El *user pool* solo
permite crear cuentas desde el servidor (`allow_admin_create_user_only`), de modo que es originación —y no el cliente— quien
decide si se crea una cuenta: la ruta es pública, pero la decisión de crearla no.

| Elemento | Configuración |
|---|---|
| Recurso | `/v1/registrations`, colgado de `/v1` |
| Método | Solo `POST`, con `authorization = "NONE"` y `api_key_required = false`. Leer un registro exige sesión y va por las rutas humanas |
| Integración | `HTTP_PROXY` (mismo VPC link y *timeout*) a `<backend_base_url><backend_path>` |
| Límite | `rate_limit` / `burst_limit` (req/s; 2 y 5 por defecto) en el *stage*, porque ningún *usage plan* cubre una ruta sin API key; sin caché |

```hcl
registration_route = {} # POST /v1/registrations → <ALB>/v1/registrations, 2 req/s, ráfaga 5
```

El límite es **más bajo que el de la tokenización** porque alguien se registra una sola vez, mientras que una tokenización
ocurre en cada compra.

Es la **única ruta pública de escritura bajo `/v1`**. `public_routes` sigue siendo de solo lectura y su validación no cambia:
exponer una escritura pública exige declararla aquí, con su límite, y no se puede hacer por descuido.

Con `registration_route = null` (defecto) no se crea nada, la API queda igual y el *hash* de redespliegue no cambia.

## Rutas de socio en rutas exactas (T5)

Además de la cotización (`risk-profiles`, fija en el módulo), `partner_routes` publica rutas del **canal del aliado** en rutas exactas
bajo `/v1`: la llave es la ruta (1 a 3 segmentos en minúsculas, p. ej. `offers`, `audit/consents` o `audit/consent-terms/current`) y
cada método se publica como la cotización: `authorization = "NONE"`, `api_key_required = true` (aplica el usage plan de cada socio),
`HTTP_PROXY` a la misma ruta del servicio y **sin *preflight*** (el socio llama de servidor a servidor, o el portal por CloudFront con
el mismo origen). Solo se publica lo que se nombra: una ruta vecina, como `audit/consent-verifications`, no existe en el gateway (`403`).

`root_methods` (opcional en `human_routes`) separa los métodos de la raíz `/v1/<ruta>` de los de sus subrutas `{proxy+}`: `null` = los
mismos de `methods` (lo de siempre); `[]` deja la raíz sin métodos humanos ni *preflight*, para que la tome una ruta de socio. Así
conviven en `/v1/offers` el `POST` del aliado (API key) y `GET /v1/offers/{id}`, `…/renewal` y `…/acceptance` con Cognito:

```
/v1/offers                        POST       → API key del socio   partner_routes.offers
/v1/offers/{proxy+}               GET, POST  → Cognito             human_routes.offers (root_methods = [])
/v1/audit/consent-terms/current   GET        → API key del socio   partner_routes
/v1/audit/consents                POST       → API key del socio   partner_routes
```

| Regla | Cómo se aplica |
|---|---|
| Formato y métodos | Validación de la variable: 1 a 3 segmentos; métodos `GET`, `POST`, `PUT`, `PATCH`, `DELETE` sin repetir |
| `risk-profiles` | No puede ser ruta de socio (es fija en el módulo) |
| Ruta pública | Una ruta de socio no puede empezar por una llave de `public_routes` (*precondition*) |
| Bajo una ruta humana | Solo puede tomar su raíz (una ruta más profunda chocaría con `{proxy+}`), y sus métodos no pueden repetir los de `root_methods` (*preconditions*) |

Con `partner_routes = {}` y sin `root_methods` (los valores por defecto) no se crea nada nuevo, la API queda igual y el *hash* de
redespliegue no cambia: `staging/ha-l01` y `staging/ha-s01` no se ven afectados.

## Subrutas humanas bajo una ruta de socio (HU-W4.2-02)

El portal de cumplimiento lee y revoca un consentimiento por `/v1/audit/consents/{consent_id}/…`, debajo de la ruta de socio
`POST /v1/audit/consents`. Declarar `audit` como `human_routes` publicaría todo `/v1/audit/*`, incluida la verificación interna del
consentimiento (`GET /v1/audit/consent-verifications`) y el registro de decisiones (`POST /v1/audit/decisions`), que no llevan token.
Por eso `human_subroutes` crea **solo** el recurso `{proxy+}` de una ruta de socio:

```
/v1/audit/consents                POST       → API key del socio   partner_routes."audit/consents"
/v1/audit/consents                GET        → Cognito             human_subroutes."audit/consents".root_methods (HU-W4.2-01)
/v1/audit/consents/{proxy+}       GET, POST  → Cognito             human_subroutes."audit/consents"
/v1/audit/consent-verifications   —          → no existe (403)
/v1/audit/decisions               —          → no existe (403)
```

**`root_methods` (HU-W4.2-01).** El listado del portal es `GET /v1/audit/consents`, la ruta **exacta** del `POST` del socio. En API
Gateway la autorización se configura por método, así que `root_methods = ["GET"]` crea en el mismo recurso de la ruta de socio un `GET`
con el autorizador de Cognito y sin API key (integración `HTTP_PROXY` a `/v1/audit/consents`) y un *preflight* `OPTIONS` sin
autorización; el `POST` sigue con su API key y sin autorizador. CloudFront lleva `/v1/audit/consents` al origen del socio con la API
key, que el gateway ignora en un método sin `api_key_required`, y deja pasar la cabecera `Authorization` del navegador.

La subruta usa los mismos recursos que las rutas humanas (autorizador de Cognito, `api_key_required = false`, `HTTP_PROXY` a
`/v1/<llave>/{proxy}`, *preflight* `OPTIONS` sin autorización). El rol lo decide el servicio, que vuelve a verificar el JWT.

| Regla | Cómo se aplica |
|---|---|
| Formato y métodos | Validación de la variable: 1 a 3 segmentos; métodos `GET`, `POST`, `PUT`, `PATCH`, `DELETE` sin repetir |
| Rutas internas | `audit/consent-verifications` y `audit/decisions` se rechazan en la variable, ni siquiera detrás del autorizador |
| Ruta de socio | La llave debe existir en `partner_routes` (*precondition*): la raíz sigue siendo del socio |
| Ruta humana | La llave no puede ser también una clave de `human_routes` (el `{proxy+}` se crearía dos veces) |
| Autorizador | Exige `cognito_user_pool_arns` (*precondition*): nunca se publica sin Cognito |
| `root_methods` | Métodos válidos sin repetir (validación de la variable); un método no puede estar a la vez en `root_methods` y en la ruta de socio de la misma llave (*precondition* del método de socio) |

Con `human_subroutes = {}` (el valor por defecto) no se crea nada y la API queda igual; sin `root_methods` tampoco cambia nada en la
ruta exacta.

## Validación local (sin AWS)

```bash
terraform -chdir=infra/terraform/modules/apigateway init -backend=false
terraform -chdir=infra/terraform/modules/apigateway validate
terraform -chdir=infra/terraform/modules/apigateway test
```

`tests/apigateway.tftest.hcl` (HA-S01) usa un proveedor simulado: afirma que sin user pool la API sigue siendo solo de socios,
que toda ruta humana usa el authorizer y ninguna exige API key, que las rutas de socio nunca usan el authorizer, el *preflight*
sin autorización y las respuestas 4XX con CORS. Desde HA-D02 afirma además que las rutas públicas no usan authorizer ni API key y
quedan limitadas en el stage, que son de solo lectura, que una ruta no puede ser pública y humana, que `risk-profiles` no puede ser
pública, que con tipos binarios el *preflight* convierte su cuerpo a texto y que los valores por defecto no cambian nada (mismo
*hash* de redespliegue). Desde T5 afirma que la tokenización pública es un único `POST` en `/pasarela/tokens`, sin sesión ni API key,
limitado en el stage, que sin ella no existe ninguna ruta `/pasarela` y que `backend_path` debe ser una ruta absoluta. Y que las rutas
de socio exigen API key sin authorizer ni *preflight*, construyen sus recursos por profundidad, toman la raíz de una ruta humana sin
tocar sus subrutas, que `root_methods` por defecto repite `methods`, y que se rechazan `risk-profiles`, una ruta pública, una ruta
profunda bajo una humana y un método repetido en la raíz.

Desde HU-W4.2-02 afirma además que `human_subroutes` crea solo el `{proxy+}` bajo la ruta de socio, con Cognito, sin API key y con
*preflight*, que la raíz de socio no cambia y que no existen `consent-verifications` ni `decisions`; y que se rechazan una llave que
no es ruta de socio, una ruta interna, una llave que también es ruta humana y una subruta sin user pool. Desde HU-W4.2-01 afirma que
`root_methods` crea el `GET` exacto de `/v1/audit/consents` con Cognito y sin API key (y su *preflight*) en el recurso de la ruta de
socio, que el `POST` del socio sigue con API key y sin autorizador, que el `{proxy+}` no cambia, que sin `root_methods` no aparece
ningún método nuevo y que se rechazan un método repetido entre el socio y `root_methods` y un `root_methods` con métodos repetidos o
desconocidos.
