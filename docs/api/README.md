# API

Cada servicio mantiene su contrato en `services/<service>/api/openapi.yaml`; Schemathesis valida las peticiones y respuestas contra él en CI.

| Servicio | Contrato | Estado |
|---|---|---|
| origination | [openapi.yaml](../../services/origination/api/openapi.yaml) | 0.12.0 (HU-W1.5-02): la renovación en línea del asegurado (`cognitoJwt`, solo `insured`): `GET /v1/policies`, `GET` y `POST /v1/policies/{policy_id}/renewal` y `GET …/renewal/status`, con los códigos `renewal_not_available`, `renewal_closed`, `renewal_not_found`, `policy_not_found` y `renewal_unavailable`; el canal `renewal` en el monitor de emisiones. 0.11.0 (HU-W1.3-02): el detalle del rechazo del cobro. `Charge` agrega `decline_code` —el código del emisor tal como lo reporta la pasarela, `51` son fondos insuficientes—, `provider_reference` —el identificador que el cliente le cita a su banco— y `offer_expires_at`; los dos primeros se limpian al entregar otro medio de pago. `PaymentMethod` agrega `payer_type`, obligatorio con `pse`. **Viaja el código y no la frase:** la redacción de la pantalla E4c es del cliente, así sigue su idioma. Guía en [`docs/contratos/HU-W1.3-02-api.md`](../contratos/HU-W1.3-02-api.md). 0.10.1 (fix C5 de HU-W1.1-02 y HU-W1.3-01): **sin cambios de forma** (rutas, esquemas y códigos iguales); corrige las descripciones de persistencia: con `DATABASE_URL` las aceptaciones, la bandeja del asesor y sus decisiones y los clientes están en PostgreSQL y sobreviven a un reinicio. Nombra además el valor `payment_method_missing` de `IssuanceException.reason` y de `Charge.failure_reason` (HU-W1.3-02), agregado antes sin subir la versión. 0.10.0 (HU-W1.2-03): rutas de la oferta personalizada. `POST /v1/offers` (canal del aliado, sin token de Cognito; API key en el API Gateway), `GET /v1/offers/{offer_id}` (`cognitoJwt`; `insured` —el primer asegurado que la abre la reclama—, `advisor` y `operations`; también sirve las ofertas `adj-*` y `demo-*` con una vista reducida) y `POST /v1/offers/{offer_id}/renewal` (`cognitoJwt`, solo `insured` dueño); la aceptación de una oferta reemplazada responde `422 offer_expired`; guía en [`docs/contratos/HU-W1.2-03-api.md`](../contratos/HU-W1.2-03-api.md). 0.9.0 (HU-W1.2-01: con `AUDIT_URL` el consentimiento de `/v1/risk-profiles/*` lo verifica `audit`, `consent_active` se ignora y `503 request_aborted` incluye el caso de `audit` caído; sin cambios de forma; guía en [`docs/contratos/HU-W1.2-01-api.md`](../contratos/HU-W1.2-01-api.md)). 0.8.0: `/health`, `POST /v1/risk-profiles/resolve` (HA-D01) y `GET /v1/risk-profiles/{customer_id}` (HA-L01); la respuesta agrega `partial` y `sources[]` (estado y latencia por fuente). Rutas humanas de HU-W1.3-01 (`cognitoJwt`): `POST`/`GET /v1/offers/{offer_id}/acceptance` y `GET /v1/acceptances/{acceptance_id}`; guía del front en [`docs/contratos/HU-W1.3-01-api.md`](../contratos/HU-W1.3-01-api.md). Rutas humanas de HU-W1.1-01 (`cognitoJwt`, rol `advisor`): `POST /v1/assisted-quotes`, `GET /v1/assisted-quotes/{quote_id}` y `GET /v1/customers?document_id=`; guía del front en [`docs/contratos/HU-W1.1-01-api.md`](../contratos/HU-W1.1-01-api.md). HU-W1.3-03: `GET /v1/policies/{policy_id}/issuance-status` y `GET /v1/policies/{policy_id}/contract-document` (`cognitoJwt`; dueño, `advisor` u `operations`) y `GET /v1/documents/{token}` (sin sesión: el enlace firmado y con vencimiento es la autorización); guía del front en [`docs/contratos/HU-W1.3-03-api.md`](../contratos/HU-W1.3-03-api.md). HA-D02 (`cognitoJwt`): `GET /v1/acceptances/{acceptance_id}/charge` (dueño, `advisor` u `operations`), `GET /v1/issuance-exceptions` y `POST /v1/acceptances/{acceptance_id}/charge/resume` (solo `operations`); guía en [`docs/contratos/HA-D02-api.md`](../contratos/HA-D02-api.md). HU-W1.1-02 (`cognitoJwt`, solo `advisor`): `GET /v1/review-requests`, `GET /v1/review-requests/{acceptance_id}` y `POST /v1/review-requests/{acceptance_id}/decision`; la aceptación agrega los estados `adjusted` y `rejected` y el objeto `review`; guía del front en [`docs/contratos/HU-W1.1-02-api.md`](../contratos/HU-W1.1-02-api.md) |
| claims | [openapi.yaml](../../services/claims/api/openapi.yaml) | 0.5.0 (HU-W2.1-01): los soportes de la radicación web ya no pasan por el servicio. `POST /v1/claims/supports` (JSON: nombre, tipo, tamaño, sha256) devuelve la URL firmada con la que el navegador sube el archivo directo al *bucket* (sin el límite de 10 MB del API Gateway) y `POST /v1/claims/supports/{support_id}/confirmation` comprueba tamaño, resumen y contenido; solo un soporte confirmado se puede radicar. `GET /v1/claims/{claim_id}` agrega `evidence` (la evidencia del móvil con lectura firmada). 0.4.0 (HU-M2.1-01): la evidencia del siniestro. `POST /v1/claims/evidence/upload-url` (`cognitoJwt`, solo `insured`) emite la URL firmada con la que el teléfono sube una foto o un video directamente al *bucket* de evidencia; el archivo no cruza el servicio, que es lo que permite un video de 30 segundos. `POST /v1/claims` gana el arreglo `evidence`, ahora **obligatorio** en el canal móvil: entre 4 y 6 fotografías y hasta un video, con la ubicación, el instante de captura y el resumen de cada una; una llave que no está en el repositorio responde `422` con el código de campo `not_stored`, y la respuesta trae `evidence_count`. Guía en [`docs/contratos/HU-M2.1-01-api.md`](../contratos/HU-M2.1-01-api.md). 0.3.0 (HU-W2.1-01): radicación del siniestro con soportes desde el portal web y lectura de los casos propios, todas humanas (`cognitoJwt`, solo `insured`): `GET /v1/claims/filing-options`, `POST /v1/claims/supports` (multipart, JPG/PNG/PDF ≤ 10 MiB; reemplazado en 0.5.0), `POST /v1/claims/web-filings` (`Idempotency-Key`), `GET /v1/claims`, `GET /v1/claims/{claim_id}` y `GET /v1/claims/{claim_id}/filing-receipt` (PDF); un caso ajeno responde `403`; guía en [`docs/contratos/HU-W2.1-01-api.md`](../contratos/HU-W2.1-01-api.md). 0.2.0 (HU-M2.2-01, la fila decía «Solo `/health`»): `POST /v1/claims` (`cognitoJwt`, solo `insured`), guía en [`docs/contratos/HU-M2.2-01-api.md`](../contratos/HU-M2.2-01-api.md) |
| partners | [openapi.yaml](../../services/partners/api/openapi.yaml) | Solo `/health` |
| audit | [openapi.yaml](../../services/audit/api/openapi.yaml) | 0.5.0 (HU-W4.2-02): el portal de cumplimiento revoca un consentimiento (`cognitoJwt`, solo `operations`): `GET /v1/audit/consents/{consent_id}/revocation` (estado para el modal, la alerta y el reintento: titular con documento enmascarado, alcance, entidades, `revocable`, registro con quién, motivo y referencia, e historial), `POST /v1/audit/consents/{consent_id}/revocations` (cabecera `Idempotency-Key` UUID obligatoria; cuerpo `reason`, `reference`, `role_confirmed`: `201`, `200` con la misma clave, `400 validation_failed` con todos los campos a la vez, `409 consent_not_active`, `409 consent_already_revoked`, `409 idempotency_key_reused`) y `POST /v1/audit/consents/{consent_id}/revocation-retries` (`202`; `409 consent_not_revoked` y `409 nothing_to_retry`); sin migración; guía en [`docs/contratos/HU-W4.2-02-api.md`](../contratos/HU-W4.2-02-api.md). 0.5.0 es también la primera versión que declara las rutas de decisiones de HU-W4.1-01 (`POST /v1/audit/decisions`, interna, y `GET /v1/audit/decisions/{offer_id}`, `operations`), agregadas antes sin subir la versión. 0.4.0 (HU-M1.3-02): el asegurado revoca un consentimiento (`POST /v1/consents/{consent_id}/revocations`, `cognitoJwt`, solo `insured`, cabecera `Idempotency-Key` UUID obligatoria: `201`, `200` con la misma clave, `409 consent_not_active`, `409 consent_already_revoked`, `409 idempotency_key_reused`) y descarga el comprobante (`GET /v1/consents/{consent_id}/revocation-receipt`, `application/pdf`); `HolderConsent` gana `revocable` y `revocation`, y el historial los eventos de la revocación y de cada entidad; guía en [`docs/contratos/HU-M1.3-02-api.md`](../contratos/HU-M1.3-02-api.md). 0.3.0 (HU-M1.3-01): rutas del asegurado sobre sus propios consentimientos (`cognitoJwt`, solo `insured`; el titular es el `sub` del token): `GET /v1/consents` y `GET /v1/consents/{consent_id}`, de solo lectura, con `403 holder_not_linked` mientras la cuenta no tenga su vínculo `CustomerLinked` y `404 not_found` para lo ajeno; guía en [`docs/contratos/HU-M1.3-01-api.md`](../contratos/HU-M1.3-01-api.md). 0.2.0 (HU-W1.2-01): `/health`; canal del aliado (sin token de Cognito; API key en el API Gateway): `GET /v1/audit/consent-terms/current` y `POST /v1/audit/consents`; interna (solo desde la red, llamada de `origination`): `GET /v1/audit/consent-verifications?customer_id=`; humana (`cognitoJwt`, roles `operations` y `advisor`): `GET /v1/audit/consents/{consent_id}`; guía en [`docs/contratos/HU-W1.2-01-api.md`](../contratos/HU-W1.2-01-api.md) |
| notifications | [openapi.yaml](../../services/notifications/api/openapi.yaml) | Solo `/health` |

Aquí se documentará además: reglas de versionado (la web, el móvil y los socios no deben romperse a la vez) y ejemplos de uso.

## Convención de rutas

**Un prefijo por recurso, sustantivo en plural** (decisión D-3 a de HU-M1.3-01, #84): `/v1/<recurso>` y `/v1/<recurso>/{id}`. Cuando
un mismo servicio atiende a públicos distintos, cada familia tiene su propio prefijo, así la autorización del API Gateway (Cognito o API
key) y la regla del ALB se deciden por el prefijo, sin mezclar personas y socios en una misma ruta.

Rutas de `audit`:

| Familia | Ruta | Autorización | Historia |
|---|---|---|---|
| Personas (Cognito) | `GET /v1/consents`, `GET /v1/consents/{consent_id}` | `cognitoJwt`, rol `insured`; el titular es el `sub` | HU-M1.3-01 |
| Personas (Cognito) | `POST /v1/consents/{consent_id}/revocations`, `GET /v1/consents/{consent_id}/revocation-receipt` | `cognitoJwt`, rol `insured`; el titular es el `sub` | HU-M1.3-02 |
| Personas (Cognito) | `GET /v1/audit/consents/{consent_id}` (evidencia) | `cognitoJwt`, roles `operations` y `advisor`; el gateway aún no la publica | HU-W1.2-01 (publicación: Fase G de HU-W4.2-02) |
| Personas (Cognito) · `operations` | `GET /v1/audit/consents/{consent_id}/revocation`, `POST …/revocations`, `POST …/revocation-retries` | `cognitoJwt`, solo `operations`; el gateway aún no las publica | HU-W4.2-02 (publicación: Fase G, solo `/v1/audit/consents/{proxy+}`) |
| Bancos y aliados (API key) | `GET /v1/audit/consent-terms/current`, `POST /v1/audit/consents` | API key del socio en el API Gateway, sin Cognito | HU-W1.2-01 |
| Interna (nunca se publica) | `GET /v1/audit/consent-verifications?customer_id=` | Ninguna: solo dentro de la VPC (Cloud Map), la pide originación | HU-W1.2-01 |

En el ALB, `audit` recibe `/v1/audit*` y `/v1/consents*` (`infra/terraform/modules/platform`).

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

### Valores de los clientes por ambiente

`production-demo` y `staging` usan el mismo user pool **permanente**, `solventa-users`, en la región `us-east-2`, creado por
[`infra/terraform/bootstrap/identity`](../../infra/terraform/bootstrap/identity/README.md) (T4). Sus identificadores no cambian entre
ventanas de staging, así que el portal web y la app móvil los configuran una sola vez. Ninguno es secreto.

| Valor | Salida de `bootstrap/identity` | Identificador (aplicado el 2026-10-18) | Quién lo usa |
|---|---|---|---|
| Región | `region` | `us-east-2` | Portal web y app móvil (llamadas a Cognito) |
| ID del user pool | `user_pool_id` | `us-east-2_EWwg8NL2C` | Portal web y app móvil |
| Cliente web (`solventa-users-web`) | `client_ids["web"]` | `v16a9q5ba8hhh5keo76fjd4e1` | Portal web |
| Cliente móvil (`solventa-users-mobile`) | `client_ids["mobile"]` | `6dri40bi3sk74cr0jb5djq43c0` | App móvil |
| Emisor | `issuer` | `https://cognito-idp.us-east-2.amazonaws.com/us-east-2_EWwg8NL2C` | `AUTH_ISSUER` de los servicios (lo pone `modules/platform`); los dos clientes forman `AUTH_CLIENT_IDS` |

Se pueden volver a leer con `terraform -chdir=infra/terraform/bootstrap/identity output <salida>` (por nombre; requiere
credenciales de la cuenta `368083581678`, porque el estado está en el bucket de T4). Los usuarios de prueba de ese pool, uno por
rol, están en el runbook
[«Usuarios de prueba permanentes»](../runbooks/README.md#5-usuarios-de-prueba-permanentes-bootstrapidentity).

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
