# Contrato de API — HU-W1.2-01 · Consentimiento explícito para consultar datos financieros

> **Fuente única para el front (Angular).** Generado a partir del código real de los servicios `audit`
> (`internal/adapters/http/consents.go`, `internal/consents`, `internal/domain/consent.go`, `internal/adapters/postgres`,
> `migrations/0001_consent_record.sql`) y `origination` (`internal/profiling/consent.go`, `internal/adapters/audit/verifier.go`,
> `cmd/origination/consent.go`) y de respuestas **capturadas del binario en ejecución** el 2026-10-18 (los dos servicios en Docker,
> PostgreSQL de `audit`, Redis, los stubs de Open Finance, Open Data y KYC detrás de Toxiproxy y Cognito local de Moto con verificación
> JWT RS256 real de `pkg/security`). Los identificadores y las fechas de los ejemplos son los de esa ejecución. Contratos formales:
> `services/audit/api/openapi.yaml` (versión 0.2.0) y `services/origination/api/openapi.yaml` (versión 0.9.0).

---

## 1. Resumen del flujo de punta a punta

```
Front embebido del aliado             audit                                      origination
────────────────────────────          ───────────────────────────────────        ─────────────────────────────────
GET  /v1/audit/consent-terms/current ─▶ términos vigentes (E1: datos, fuentes, finalidad)
POST /v1/audit/consents ──────────────▶ registro inmutable (titular, alcance, fecha y hora, canal)
                                                   ▲
POST /v1/risk-profiles/resolve ───────────────────────────────────────────────▶ ¿hay consentimiento? ──▶ GET /v1/audit/consent-verifications
                                                                                 ├─ no  ─▶ 422 consent_required (no se consulta nada)
                                                                                 ├─ audit no responde ─▶ 503 request_aborted (tampoco)
                                                                                 └─ sí  ─▶ fuentes en paralelo, perfil, oferta
```

| Criterio | Cubierto | Cómo |
|---|---|---|
| CA-1 Antes de continuar se muestran los datos, las fuentes y la finalidad | Sí | `GET /v1/audit/consent-terms/current` (versionado). El `POST` envía el `terms_version` que se mostró y recibe `409 terms_outdated` si ya no es el vigente |
| CA-2 Registro con titular, alcance, fecha y hora y canal, consultable después | Sí | `POST /v1/audit/consents` guarda titular, **copia** del alcance, `granted_at` (reloj del servidor), canal, aliado y solicitud. Consulta: `GET /v1/audit/consents/{consent_id}` (roles `operations` y `advisor`) y `GET /v1/audit/consent-verifications` |
| CA-3 Sin consentimiento no se consulta ninguna fuente | Sí | `origination` pregunta a `audit` antes de tocar la caché o las fuentes: sin consentimiento `422 consent_required`; si `audit` no responde, `503 request_aborted` (nunca se asume) |
| CA-4 Registro inmutable | Sí | Disparadores de PostgreSQL rechazan `UPDATE`, `DELETE` y `TRUNCATE`; el servicio no tiene operación para modificar un registro |

**Revocación: fuera de alcance.** La tabla queda preparada (una revocación futura será un registro nuevo, nunca un `UPDATE`). Ni la
HU publicada (issue #64, cuatro criterios) ni esta entrega incluyen el «CA-5» (revocación ≤ 5 min) que cita el Terraform.

---

## 2. Endpoints de `audit`

Todas las respuestas llevan `Cache-Control: no-store` y los errores tienen la forma `{"error","message"}` (más `fields` en
`validation_failed`).

| Método y ruta | Quién | Éxito | Errores |
|---|---|---|---|
| `GET /v1/audit/consent-terms/current` | Canal del aliado | `200` | — |
| `POST /v1/audit/consents` | Canal del aliado | `201` nuevo · `200` ya existía | `400 invalid_request` · `validation_failed`, `409 terms_outdated`, `503 service_unavailable` |
| `GET /v1/audit/consent-verifications?customer_id=` | **Interna** (`origination`) | `200` | `400 invalid_request`, `503 service_unavailable` |
| `GET /v1/audit/consents/{consent_id}` | Cognito `operations` o `advisor` | `200` | `401`, `403`, `404 consent_not_found`, `503` |

El canal del aliado se autentica con la API key en el API Gateway (sin token de Cognito). La verificación es una llamada entre
servicios: **no debe publicarse a los aliados**.

### 2.1 `GET /v1/audit/consent-terms/current` — términos vigentes (pantalla E1)

El front pinta estos datos tal cual (los textos los traduce cada cliente) y guarda `version` para enviarla al aceptar.

```json
{
  "version": "v1",
  "purpose": "risk_profiling_mortgage_life_offer",
  "controller": "Solventa S.A.",
  "policy_ref": "personal-data-policy-v1",
  "sources": [
    {"code": "open_finance", "data_categories": ["credit_products", "balances", "payment_behavior"], "validity": "12_months"},
    {"code": "open_data", "data_categories": ["public_records"], "validity": "12_months"},
    {"code": "kyc", "data_categories": ["identity_verification"], "validity": "single_use"}
  ]
}
```

`validity`: `12_months` o `single_use` (un solo uso; mientras no se registre su uso, dura lo que el consentimiento).

### 2.2 `POST /v1/audit/consents` — registrar el consentimiento

| Campo | Tipo | Obligatorio | Reglas |
|---|---|---|---|
| `customer_id` | string | Sí | El id del cliente en el aliado (el que luego se usa en `/v1/risk-profiles`). 1–128 caracteres `A-Z a-z 0-9 _ -` |
| `document_id` | string | Sí | 5–20 letras o dígitos |
| `full_name` | string | Sí | 3–120 caracteres, con al menos una letra |
| `terms_version` | string | Sí | La versión mostrada (`version` del §2.1) |
| `accepted_sources` | string[] | Sí | **Todas** las fuentes de los términos, sin repetir ni faltar (todo o nada) |
| `accepted_data_policy` | boolean | Sí | Debe ser `true` |
| `channel` | string | Sí | `embedded` (único canal aceptado hoy) |
| `partner_id` | string | Sí | Aliado; mismo formato que `customer_id` |
| `application_id` | string | Sí | Solicitud de crédito; mismo formato que `customer_id` |

El `user_agent` se toma de la cabecera `User-Agent` y la fecha y hora (`granted_at`) las pone el servidor. **Idempotente** por
`(customer_id, application_id, terms_version)`: repetir el `POST` devuelve `200` con el mismo consentimiento y no registra otro. Un
`customer_id` con otra `application_id` registra un consentimiento nuevo.

Respuesta `201` real (`200` idéntica al repetir). El recibo **no repite el documento del titular**:

```json
{
  "consent_id": "cns-7c3e85c71dddd603c8e90def9f4ce9cd",
  "customer_id": "cliente-0001",
  "terms_version": "v1",
  "purpose": "risk_profiling_mortgage_life_offer",
  "channel": "embedded",
  "scope": [
    {"code": "open_finance", "data_categories": ["credit_products", "balances", "payment_behavior"], "validity": "12_months"},
    {"code": "open_data", "data_categories": ["public_records"], "validity": "12_months"},
    {"code": "kyc", "data_categories": ["identity_verification"], "validity": "single_use"}
  ],
  "granted_at": "2026-10-18T02:48:14.062927Z",
  "expires_at": "2027-10-18T02:48:14.062927Z"
}
```

`expires_at` = `granted_at` + 12 meses. El `consent_id` (`cns-` + 128 bits aleatorios) no se puede adivinar.

Errores reales:

```json
// 409 — la versión mostrada ya no es la vigente: mostrar de nuevo los términos
{"error": "terms_outdated", "message": "the terms version shown is no longer the current one; show the current terms again"}

// 400 — todos los campos inválidos a la vez (enviado: customer_id "cliente 0001", document_id "12", full_name "Al",
//       solo una fuente y accepted_data_policy false)
{
  "error": "validation_failed",
  "message": "one or more fields are invalid",
  "fields": [
    {"field": "customer_id", "code": "invalid_format"},
    {"field": "document_id", "code": "invalid_format"},
    {"field": "full_name", "code": "invalid_format"},
    {"field": "accepted_sources", "code": "incomplete"},
    {"field": "accepted_data_policy", "code": "must_be_true"}
  ]
}
```

Códigos de campo: `required`, `invalid_format`, `out_of_range`, `incomplete`, `must_be_true`. Con campos inválidos **y** versión
desactualizada se responde primero `400`.

### 2.3 `GET /v1/audit/consent-verifications?customer_id=` — verificación (interna)

Sin consentimiento **no es un error**: `active=false`. Un consentimiento vencido también responde `active=false`, con su evidencia.

```json
// Antes de consentir
{"active": false, "consent_id": null, "granted_at": null, "expires_at": null, "sources": []}

// Después
{
  "active": true,
  "consent_id": "cns-7c3e85c71dddd603c8e90def9f4ce9cd",
  "granted_at": "2026-10-18T02:48:14.062927Z",
  "expires_at": "2027-10-18T02:48:14.062927Z",
  "sources": ["open_finance", "open_data", "kyc"]
}
```

### 2.4 `GET /v1/audit/consents/{consent_id}` — evidencia (`operations`, `advisor`)

Cabecera `Authorization: Bearer <ID token de Cognito>`. La evidencia sí incluye al titular:

```json
{
  "consent_id": "cns-7c3e85c71dddd603c8e90def9f4ce9cd",
  "customer_id": "cliente-0001",
  "holder": {"document_id": "1020304050", "full_name": "Ana María Pérez"},
  "terms_version": "v1",
  "scope": [
    {"code": "open_finance", "data_categories": ["credit_products", "balances", "payment_behavior"], "validity": "12_months"},
    {"code": "open_data", "data_categories": ["public_records"], "validity": "12_months"},
    {"code": "kyc", "data_categories": ["identity_verification"], "validity": "single_use"}
  ],
  "purpose": "risk_profiling_mortgage_life_offer",
  "channel": "embedded",
  "partner_id": "aliado-banco-1",
  "application_id": "credito-2026-0001",
  "user_agent": "Mozilla/5.0 (Macintosh) Solventa-Front/1.0",
  "granted_at": "2026-10-18T02:48:14.062927Z",
  "expires_at": "2027-10-18T02:48:14.062927Z"
}
```

Errores reales: sin token `401 {"error":"unauthorized","message":"a bearer token is required"}`; rol `insured` (u otro distinto de
`operations`/`advisor`) `403 {"error":"forbidden","message":"the caller is not allowed to perform this operation"}`; id desconocido
`404 {"error":"consent_not_found","message":"there is no consent with that id"}`.

---

## 3. Cambio en `origination` (0.9.0)

`POST /v1/risk-profiles/resolve` y `GET /v1/risk-profiles/{customer_id}` **no cambian de forma**. Con `AUDIT_URL` configurada:

- `consent_active` sigue siendo obligatorio en el contrato, pero **su valor se ignora**: decide `audit`. Un `consent_active=true`
  sin consentimiento registrado responde `422`; un `false` con consentimiento registrado responde `200`.
- Sin consentimiento activo: `422 {"error":"consent_required","message":"an active consent is required to profile the customer"}`.
  No se consulta la caché ni ninguna fuente.
- `audit` no responde en 300 ms (`AUDIT_TIMEOUT_MS`), responde con error o su circuito está abierto:
  `503 {"error":"request_aborted","message":"the request was aborted before an offer could be produced"}`. El front lo trata como un
  error reintentable («no pudimos verificar tu consentimiento»). No hay un código propio para este caso (queda pendiente, §5).

Respuestas reales de `origination` con el consentimiento del §2.2 registrado: con `{"customer_id":"cliente-0001","consent_active":false}`
→ `200` con `"mode":"normal"`, `"source":"external_sources"` y el perfil; con `audit` detenido y `consent_active=true` → el `503`
anterior.

---

## 4. Estados y orden del recorrido embebido

```
términos vistos ─▶ aceptación (POST /consents 201) ─▶ perfil (POST /risk-profiles/resolve 200) ─▶ oferta
                      │ 409 terms_outdated: volver a mostrar los términos
                      │ 400 validation_failed: marcar los campos
                      └ repetir el POST es seguro (200, mismo consent_id)
```

El front debe registrar el consentimiento **antes** de pedir el perfil. Si el cliente vuelve más tarde dentro de los 12 meses, el
consentimiento sigue activo y no hace falta volver a pedirlo.

---

## 5. Qué está pendiente

| Pieza | Estado hoy | Pendiente de |
|---|---|---|
| Revocación del consentimiento | No existe (el modelo la admite como registro nuevo) | HU futura |
| Uso único del KYC | Dura lo que el consentimiento; su uso no se registra | HU futura |
| Particionado por fecha de `consent_record` | No existe | Cuando el volumen lo pida |
| Rutas en el API Gateway (AWS) y variables de ECS (`AUDIT_URL`, `DATABASE_URL`, `AUTH_*` de `audit`) | No registradas | Tarea de Terraform (`chore(infra)`) |
| Código de error propio para «consentimiento no verificable» | Es `503 request_aborted` | Dueños de `handler.go` |
| Cotización asistida (`POST /v1/assisted-quotes`) | Sigue con el `consent_active` afirmado por el asesor | HU-W1.1-01 |

## 6. Cómo correrlo en local

```
./tests/e2e/hu-w1.2/run.sh        # los dos servicios reales, PostgreSQL, Redis, stubs con Toxiproxy y Moto
```

Para correr los servicios a mano: `audit` con `DATABASE_URL` (y `AUTH_*` para la evidencia) y `origination` con `AUDIT_URL`
(ver los README de cada servicio).
