# Contrato de API — HU-W4.2-01 · Consultar el listado de consentimientos de Open Finance de los clientes

Servicio: **auditoría** (`audit`, `openapi.yaml` 0.6.0). Consumidor: portal web de cumplimiento (*Consentimientos Open Finance*: listado
O14 / O14b y detalle O15).

El analista del rol de cumplimiento (`operations`) ve **todos** los consentimientos en una tabla paginada, con cuatro indicadores, filtros
por estado, días de otorgamiento, entidad y cliente que se conservan al paginar, y abre el detalle completo de cada uno. El estado de cada
fila es el derivado **en el instante de la consulta**: cualquier revocación (desde la app del asegurado o desde el portal) se ve en la
siguiente consulta (CA-5).

> **Los ejemplos son respuestas capturadas** del servicio real en Docker (`CAPTURE_DIR=<carpeta> ./tests/e2e/hu-w4.2/list.sh`); los
> identificadores y las horas son los de esa corrida. Donde se repite una fila o el alcance se escribe `[ … ]`.

---

## 1. Cómo funciona

```
portal ──GET /v1/audit/consents?status=&granted_from=&granted_to=&entity=&q=&page=&page_size=──▶ audit
          una sola foto de la base: la página, el total, los indicadores (sobre todos) y las entidades del filtro
   ◀── 200 { items, page, page_size, total_items, total_pages, indicators, entities }
portal ──GET /v1/audit/consents/{consent_id}/revocation──▶ el detalle: el estado de HU-W4.2-02 con la vigencia, la finalidad y los canales
```

- **Rol:** `Authorization: Bearer <ID token de Cognito>`, solo **`operations`**; `advisor`, `insured` y `partner` reciben `403`; sin token,
  `401`. Todas las respuestas llevan `Cache-Control: no-store`.
- **Misma ruta que el aliado:** `/v1/audit/consents` también es el `POST` del aliado (API key, sin token). La autorización es **por
  método** (decisión D-1 a): el `GET` pide el token de cumplimiento y el `POST` sigue igual.
- **Minimización (decisión D-4 a):** cada fila trae el nombre y el documento **enmascarado** (`******4050`). La búsqueda acepta el
  documento completo y lo compara exacto en el servidor; el documento completo nunca viaja en la respuesta.
- **El orden** es por otorgamiento, el más reciente primero (desempate por id). Paginar no cambia el filtro: el portal guarda el filtro en
  su URL y pide `page` 1, 2, 3… con los mismos parámetros.

---

## 2. `GET /v1/audit/consents`

### Parámetros (todos opcionales; se combinan con «y»)

| Parámetro | Valores | Qué hace |
|---|---|---|
| `status` | `active`, `revoked`, `partially_revoked`, `expired` | Solo los consentimientos con ese estado ahora (decisión D-6 a: una opción por valor) |
| `granted_from` | `AAAA-MM-DD` | Primer día de otorgamiento, **día civil de Colombia** (UTC−5), inclusivo |
| `granted_to` | `AAAA-MM-DD` | Último día, inclusivo; anterior a `granted_from` es `400 out_of_range` |
| `entity` | código del aliado (`aliado-banco-1`) | Solo los otorgados por esa entidad; las opciones vienen en `entities` |
| `q` | hasta 100 caracteres | El id exacto (`cns-…`), el documento exacto (solo cuentan letras y dígitos: `1.020.304.050` sirve; **nunca** un prefijo) o una parte del nombre, sin mayúsculas ni tildes (`nunez` encuentra a «José Ñuñez») |
| `page` | ≥ 1 (1) | Página pedida; una después de la última responde `200` sin filas y con los totales reales |
| `page_size` | 1 a 50 (20) | El portal pide 6, como el frame `44:4954` |

### Respuesta `200`: `ComplianceConsentPage`

`GET /v1/audit/consents?page_size=6` sobre 13 consentimientos de hoy y uno vencido:

```json
{
  "items": [
    {
      "consent_id": "cns-481723ae7daea0886cec688432599fa0",
      "holder": { "full_name": "Ana María Pérez", "document_masked": "******0012" },
      "granted_at": "2026-10-31T04:00:01.652252Z",
      "expires_at": "2027-10-31T04:00:01.652252Z",
      "ended_at": null,
      "status": "active",
      "revocation": null,
      "grant_channel": "embedded",
      "scope": [
        { "code": "open_finance", "data_categories": ["credit_products", "balances", "payment_behavior"], "validity": "12_months" },
        { "code": "open_data", "data_categories": ["public_records"], "validity": "12_months" },
        { "code": "kyc", "data_categories": ["identity_verification"], "validity": "single_use" }
      ],
      "entities": [{ "code": "aliado-banco-1", "name": "Banco Aliado" }]
    }
    [ … 5 filas más … ]
  ],
  "page": 1,
  "page_size": 6,
  "total_items": 14,
  "total_pages": 3,
  "indicators": {
    "active": 13,
    "expired": 1,
    "revoked_last_30_days": { "total": 0, "mobile_app": 0, "compliance_portal": 0 },
    "revocations_out_of_deadline": 0,
    "as_of": "2026-10-31T04:00:04.14429Z"
  },
  "entities": [
    { "code": "aliado-banco-1", "name": "Banco Aliado" },
    { "code": "aliado-coop", "name": "aliado-coop" }
  ]
}
```

| Campo | Para qué en el portal |
|---|---|
| `holder.full_name`, `holder.document_masked`, `consent_id` | Columna «Cliente» (CA-1). El tipo de documento («CC») no existe en el modelo |
| `granted_at`, `expires_at` | «Otorgado» (fecha y hora) y «Vence» (12 meses después) |
| `status` | Insignia: `active`, `revoked`, `partially_revoked` o `expired`, derivado ahora |
| `revocation` | `null` si no fue revocado; si lo fue: `effective_at` (para «Revocado dd/mm»), `channel` (`mobile_app` o `compliance_portal`) y `notice_status` |
| `ended_at` | Cuándo dejó de estar vigente (revocado o vencido); `null` mientras lo esté |
| `scope`, `entities` | Columna «Alcance»: categorías de datos y entidades autorizadas |
| `total_items`, `total_pages` | Pie: «1 a 6 de 14 registros · página 1 de 3». `total_pages` es al menos 1 («página 1 de 1» sin resultados) |
| `indicators` | Las cuatro tarjetas (decisión D-5 a), **sobre todos los consentimientos, sin el filtro** (no cambian al filtrar, frame `877:9304`) |
| `entities` | Opciones del filtro «Entidad»: las que tienen nombre en `PARTNER_DISPLAY_NAMES` y las que tienen algún consentimiento, por nombre |

**Indicadores.** `active` y `expired`: consentimientos con ese estado ahora. `revoked_last_30_days`: revocados o parciales cuya revocación
tomó efecto en los últimos 30 días, en total y por canal («12 desde la app · 25 desde el portal»). `revocations_out_of_deadline`: toda
revocación en la que alguna entidad **no confirmó dentro de su primera ventana de 5 minutos** (RN-02), aunque después un reintento haya
tenido éxito; la cifra no baja. `as_of` es el instante del cálculo.

Después de una revocación desde la app y otra desde el portal, la siguiente consulta ya trae (L5 de `list.sh`):

```json
"indicators": {
  "active": 11,
  "expired": 1,
  "revoked_last_30_days": { "total": 2, "mobile_app": 1, "compliance_portal": 1 },
  "revocations_out_of_deadline": 0,
  "as_of": "2026-10-31T04:00:38.30397Z"
}
```

y la fila revocada: `"status": "revoked"`, `"ended_at": "2026-10-31T04:00:38.038275Z"`,
`"revocation": { "effective_at": "2026-10-31T04:00:38.038275Z", "channel": "compliance_portal", "notice_status": "completed" }`.

### Sin resultados (CA-3)

`GET /v1/audit/consents?status=revoked&entity=aliado-coop` antes de cualquier revocación: `200`, no un error.

```json
{
  "items": [],
  "page": 1,
  "page_size": 20,
  "total_items": 0,
  "total_pages": 1,
  "indicators": { "active": 13, "expired": 1, "revoked_last_30_days": { "total": 0, "mobile_app": 0, "compliance_portal": 0 },
                  "revocations_out_of_deadline": 0, "as_of": "2026-10-31T04:00:30.584619Z" },
  "entities": [{ "code": "aliado-banco-1", "name": "Banco Aliado" }, { "code": "aliado-coop", "name": "aliado-coop" }]
}
```

### Parámetros inválidos

Todos a la vez, con el mismo formato de los demás `400` de `audit`:

```json
{
  "error": "validation_failed",
  "message": "one or more fields are invalid",
  "fields": [
    { "field": "status", "code": "invalid_format" },
    { "field": "granted_to", "code": "out_of_range" },
    { "field": "page", "code": "out_of_range" },
    { "field": "page_size", "code": "out_of_range" }
  ]
}
```

El portal valida el rango de fechas antes de consultar (lo avisa en el campo «Hasta»), así que en uso normal no recibe este `400`.

---

## 3. El detalle: `GET /v1/audit/consents/{consent_id}/revocation` (campos nuevos)

La ruta y todos los campos de HU-W4.2-02 ([contrato](HU-W4.2-02-api.md)) siguen iguales; 0.6.0 **solo agrega** lo que el detalle O15 necesita
para CA-4 (decisión D-3 a):

```json
{
  "consent_id": "cns-c34685fb39cfa00b4846bf20aebcb5b3",
  "status": "active",
  "granted_at": "2026-10-31T03:59:54.777993Z",
  "expires_at": "2027-10-31T03:59:54.777993Z",
  "ended_at": null,
  "purpose": "risk_profiling_mortgage_life_offer",
  "grant_channel": "embedded",
  "terms_version": "v1",
  [ … holder, revocable, scope, entities, revocation, history … ],
  "history": [
    { "status": "granted", "occurred_at": "2026-10-31T03:59:54.777993Z", "entity_code": null, "actor_type": null,
      "channel": "embedded", "reason": null, "reference": null }
  ]
}
```

| Campo nuevo | Para qué |
|---|---|
| `granted_at`, `expires_at` | Encabezado («otorgado el … · vence el …») y «Vigencia» (otorgado → vence) |
| `ended_at` | Fin de la vigencia por revocación o vencimiento; `null` mientras siga vigente |
| `purpose` | «Finalidad»: un código que el portal traduce (`risk_profiling_mortgage_life_offer`) |
| `grant_channel` | «Canal de otorgamiento» (`embedded`, el flujo del aliado) |
| `terms_version` | «Evidencia»: registro inmutable con la versión de los términos |
| `history[].channel` | El canal de cada cambio: `embedded` en el otorgamiento, `mobile_app` o `compliance_portal` en la revocación; `null` en los eventos de una entidad y en el vencimiento |

---

## 4. Errores

| Código | `error` | Condición |
|---|---|---|
| `400` | `validation_failed` | Un parámetro inválido; `fields` los nombra todos |
| `401` | `unauthorized` | Sin token, token inválido o vencido (el API Gateway o el servicio) |
| `403` | `forbidden` | El rol no es `operations`; no se consulta nada |
| `503` | `service_unavailable` | No se pudo leer la base; reintentar es seguro (el listado solo lee) |

---

## 5. Lo que queda fuera

- **«Exportar»** (frame `44:4954`): ningún criterio lo pide; la exportación análoga (HU-W4.1-02) es *Could* excluida del alcance.
- Contacto (correo y teléfono), *hash* de la evidencia, «Descargar evidencia», NIT de la entidad, «Última consulta» por entidad y los hitos
  «Consultado para cálculo de oferta» y «Confirmado en la app con biometría»: `audit` no tiene esos datos.
- El tipo de documento («CC»): el modelo no lo guarda.
- La revocación, el modal y el reintento: HU-W4.2-02 (sin cambios).
