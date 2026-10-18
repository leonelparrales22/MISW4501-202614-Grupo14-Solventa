# API

Cada servicio mantiene su contrato en `services/<service>/api/openapi.yaml`; Schemathesis valida las peticiones y respuestas contra él en CI.

| Servicio | Contrato | Estado |
|---|---|---|
| origination | [openapi.yaml](../../services/origination/api/openapi.yaml) | 0.10.0 (HU-W1.2-03): rutas de la oferta personalizada. `POST /v1/offers` (canal del aliado, sin token de Cognito; API key en el API Gateway), `GET /v1/offers/{offer_id}` (`cognitoJwt`; `insured` —el primer asegurado que la abre la reclama—, `advisor` y `operations`; también sirve las ofertas `adj-*` y `demo-*` con una vista reducida) y `POST /v1/offers/{offer_id}/renewal` (`cognitoJwt`, solo `insured` dueño); la aceptación de una oferta reemplazada responde `422 offer_expired`; guía en [`docs/contratos/HU-W1.2-03-api.md`](../contratos/HU-W1.2-03-api.md). 0.9.0 (HU-W1.2-01: con `AUDIT_URL` el consentimiento de `/v1/risk-profiles/*` lo verifica `audit`, `consent_active` se ignora y `503 request_aborted` incluye el caso de `audit` caído; sin cambios de forma; guía en [`docs/contratos/HU-W1.2-01-api.md`](../contratos/HU-W1.2-01-api.md)). 0.8.0: `/health`, `POST /v1/risk-profiles/resolve` (HA-D01) y `GET /v1/risk-profiles/{customer_id}` (HA-L01); la respuesta agrega `partial` y `sources[]` (estado y latencia por fuente). Rutas humanas de HU-W1.3-01 (`cognitoJwt`): `POST`/`GET /v1/offers/{offer_id}/acceptance` y `GET /v1/acceptances/{acceptance_id}`; guía del front en [`docs/contratos/HU-W1.3-01-api.md`](../contratos/HU-W1.3-01-api.md). Rutas humanas de HU-W1.1-01 (`cognitoJwt`, rol `advisor`): `POST /v1/assisted-quotes`, `GET /v1/assisted-quotes/{quote_id}` y `GET /v1/customers?document_id=`; guía del front en [`docs/contratos/HU-W1.1-01-api.md`](../contratos/HU-W1.1-01-api.md). HU-W1.3-03: `GET /v1/policies/{policy_id}/issuance-status` y `GET /v1/policies/{policy_id}/contract-document` (`cognitoJwt`; dueño, `advisor` u `operations`) y `GET /v1/documents/{token}` (sin sesión: el enlace firmado y con vencimiento es la autorización); guía del front en [`docs/contratos/HU-W1.3-03-api.md`](../contratos/HU-W1.3-03-api.md). HA-D02 (`cognitoJwt`): `GET /v1/acceptances/{acceptance_id}/charge` (dueño, `advisor` u `operations`), `GET /v1/issuance-exceptions` y `POST /v1/acceptances/{acceptance_id}/charge/resume` (solo `operations`); guía en [`docs/contratos/HA-D02-api.md`](../contratos/HA-D02-api.md). HU-W1.1-02 (`cognitoJwt`, solo `advisor`): `GET /v1/review-requests`, `GET /v1/review-requests/{acceptance_id}` y `POST /v1/review-requests/{acceptance_id}/decision`; la aceptación agrega los estados `adjusted` y `rejected` y el objeto `review`; guía del front en [`docs/contratos/HU-W1.1-02-api.md`](../contratos/HU-W1.1-02-api.md) |
| claims | [openapi.yaml](../../services/claims/api/openapi.yaml) | Solo `/health` |
| partners | [openapi.yaml](../../services/partners/api/openapi.yaml) | Solo `/health` |
| audit | [openapi.yaml](../../services/audit/api/openapi.yaml) | 0.2.0 (HU-W1.2-01): `/health`; canal del aliado (sin token de Cognito; API key en el API Gateway): `GET /v1/audit/consent-terms/current` y `POST /v1/audit/consents`; interna (solo desde la red, llamada de `origination`): `GET /v1/audit/consent-verifications?customer_id=`; humana (`cognitoJwt`, roles `operations` y `advisor`): `GET /v1/audit/consents/{consent_id}`; guía en [`docs/contratos/HU-W1.2-01-api.md`](../contratos/HU-W1.2-01-api.md) |
| notifications | [openapi.yaml](../../services/notifications/api/openapi.yaml) | Solo `/health` |

Aquí se documentará además: reglas de versionado (la web, el móvil y los socios no deben romperse a la vez) y ejemplos de uso.

## Autenticación y autorización (HA-S01)

Hay **dos mecanismos separados** y una ruta usa uno solo de ellos:

| Llamante | Mecanismo | Dónde se valida | Respuesta si falla |
|---|---|---|---|
| Personas: asegurado, asesor comercial, analista de operaciones y usuario del portal del socio (web y móvil) | JWT del user pool de Amazon Cognito en `Authorization: Bearer <ID token>` | 1) Authorizer `COGNITO_USER_POOLS` del API Gateway (firma y vigencia); 2) el servicio lo vuelve a verificar con `pkg/security` y autoriza por rol y titularidad | 401 sin token, con token inválido o vencido · 403 con rol insuficiente o recurso ajeno |
| Integraciones máquina a máquina de los socios (p. ej. la cotización embebida) | API key (`x-api-key`) y usage plan del API Gateway | Solo el API Gateway; **nunca** pasan por Cognito ni por `pkg/security` | 403 del gateway sin API key válida |

`GET /health` no lleva ninguno de los dos.

### Token, claims y roles

- El cliente envía el **ID token** (`token_use = id`) en `Authorization: Bearer`. Por ahora no hay scopes.
- Claims que usan los servicios (`security.Claims`): `sub` (identificador estable; titularidad del asegurado), `cognito:username`, `email`, `cognito:groups` (rol), `custom:partner_id` (titularidad del socio), `token_use`, `aud` (cliente web o móvil), `iat` y `exp`.
- Roles = grupos de Cognito, **un grupo por usuario**, asignado por un administrador al aprovisionar: `insured` (asegurado), `advisor` (asesor comercial), `operations` (analista de operaciones) y `partner` (usuario del portal del socio). Un grupo que no sea uno de estos cuatro se ignora y no da acceso.
- Nadie puede autoasignarse un grupo ni cambiar `custom:partner_id`: el user pool solo admite alta administrativa y los clientes de la aplicación no pueden escribir ese atributo (módulo `infra/terraform/modules/cognito`).

### Verificación en el servicio (`pkg/security`)

- `security.NewJWKSVerifier` descarga las llaves públicas del user pool **una vez al arrancar** y las guarda en memoria; un `kid` desconocido provoca como mucho una recarga por minuto. Verificar una petición no agrega saltos de red.
- Acepta solo `RS256`, un `kid` conocido, el `iss` exacto del user pool, un `aud` de la lista de clientes permitidos, el `token_use` configurado y la ventana `iat`/`nbf`/`exp` (con tolerancia de reloj).
- `security.Authenticate` responde **401** con `WWW-Authenticate: Bearer` y no llama al handler; `security.RequireRole(...)` responde **403** y no llama al handler. Los casos de uso usan `security.RequireOwnerOrRole` (dueño del recurso o rol privilegiado) y `security.RequireSamePartner` (mismo `custom:partner_id`), y el adaptador HTTP traduce `security.ErrForbidden` con `security.WriteForbidden`, así que la operación de negocio no se ejecuta.
- Cuerpo de error (mismo esquema `Error` de los servicios): `{"error": "unauthorized" | "forbidden", "message": "..."}`.

Variables de entorno de los servicios que monten rutas humanas (ninguna es secreta):

| Variable | Obligatoria | Valor |
|---|---|---|
| `AUTH_ISSUER` | Sí | `https://cognito-idp.us-east-2.amazonaws.com/<userPoolId>` |
| `AUTH_CLIENT_IDS` | Sí | IDs de los clientes web y móvil, separados por coma |
| `AUTH_JWKS_URL` | No | Por defecto `AUTH_ISSUER` + `/.well-known/jwks.json` |
| `AUTH_TOKEN_USE` | No | `id` (por defecto) o `access` |
| `AUTH_LEEWAY_SECONDS` | No | Tolerancia de reloj; por defecto `5` |

Montaje: ver `ExampleAuthenticate`, `ExampleRequireRole` y `ExampleRequireOwnerOrRole` en `pkg/security/example_test.go`.

### Catálogo de rutas humanas en `openapi.yaml`

Cada historia que agregue rutas las clasifica en el contrato de su servicio (HA-S01 no cambia ningún `openapi.yaml` porque todavía no hay rutas humanas):

```yaml
components:
  securitySchemes:
    cognitoJwt:
      type: http
      scheme: bearer
      bearerFormat: JWT
    partnerApiKey:
      type: apiKey
      in: header
      name: x-api-key
```

- Operación humana: `security: [{cognitoJwt: []}]`, con `Authenticate` + `RequireRole` montados en su grupo de rutas y una fila en su matriz 401/403 (plantilla: `pkg/security/catalog_test.go`).
- Operación de socio: `security: [{partnerApiKey: []}]`, sin `Authenticate`.
- `GET /health`: `security: []`.

### Sesión en los clientes (web y móvil)

- Inicio de sesión contra Cognito (`InitiateAuth`), fuera del camino de las peticiones de negocio. Vigencias: ID y access token de **5 minutos**; token de renovación de **12 horas** en la web y de **30 días** en el móvil.
- Renovación con `REFRESH_TOKEN_AUTH` antes de que venza el ID token. Cierre de sesión: `RevokeToken` y borrado de la sesión local.
- Web: tokens en memoria y token de renovación en `sessionStorage` (sobrevive a recargar la página, muere al cerrar la pestaña). Un 401 del gateway lleva a «sesión expirada»; un 403, a «acceso no autorizado».
- Móvil (**propuesta de contrato**, la confirma el responsable móvil con HU-M1.1-01/02): mismos tokens y cabecera; los tokens se guardan cifrados con una llave del Android Keystore que exige BiometricPrompt. Al desbloquear, si el ID token venció se renueva con el token de renovación. La biometría, los intentos y la inactividad son de HU-M1.1-02.
- **Ventana residual de revocación:** un token revocado sigue siendo válido para cualquier verificación de firma y vigencia (incluida la de `pkg/security`) hasta su `exp`. Con ID tokens de 5 minutos, la ventana es de **5 minutos como máximo**; el equipo debe aceptarla. **Medido en la ventana de staging de HA-S01 (2026-10-11):** tras `RevokeToken`, la renovación se rechaza enseguida y Cognito rechaza el access token revocado (`GetUser` → `NotAuthorizedException`), pero el authorizer del gateway siguió aceptando el ID token revocado (`200`) y lo rechazó (`401`) **310 s después de revocarlo, a `exp` + 19–20 s**.
- **Códigos del gateway con la autorización de Cognito (medidos en AWS):** sin token, con un token mal formado o de otro emisor y con un token vencido, `401`. Con un token del pool cuya firma o contenido fueron alterados, AWS responde `403` (`ACCESS_DENIED`) por defecto; el módulo `infra/terraform/modules/apigateway` lo mapea a **`401`** para que todo token inválido pida autenticarse de nuevo. La falta de API key en una ruta de socio sigue en `403`. El gateway **reenvía `Authorization`** a la integración, así que el servicio puede volver a verificar el JWT con `pkg/security`, que lee el rol (`cognito:groups`) y `custom:partner_id` del propio token.

### Pruebas en local

- `pkg/security`: emisor de prueba dentro de los `_test.go` (llave RSA generada en el test y JWKS servido con `httptest`), sin AWS.
- Pruebas de handlers y casos de uso: se inyecta el llamante con `security.WithClaims`; no hace falta un token real.
- Configuración de Cognito y del API Gateway: `terraform test` con proveedor simulado en `infra/terraform/modules/*/tests`.
- Ciclo manual con tokens reales sin AWS: Cognito emulado con Moto en Docker ([`tests/security/ha-s01/local`](../../tests/security/ha-s01/local/README.md)).
  Solo cambia la configuración (`AUTH_ISSUER` con el pool de Moto y `AUTH_JWKS_URL` obligatoria); la prueba
  `pkg/security/moto_integration_test.go` corre solo con `AUTH_MOTO_URL`.
