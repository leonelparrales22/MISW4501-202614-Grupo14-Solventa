# Contrato de API — HU-W2.2-01 · Cálculo automático de prioridad del siniestro (triage)

Servicio: **siniestros** (`claims`). Consumidores: portal web del analista de operaciones (cola y evaluación del siniestro) y
portal web del asegurado (detalle de su siniestro).

> **Fuente única para el front.** Generado a partir del código real del servicio `claims` (`internal/adapters/http/triage.go`,
> `internal/triage/{service,worker}.go`, `internal/domain/{triage,triage_policy,web_filing}.go`, `migrations/0005_claim_triage.sql`)
> y de respuestas **capturadas del binario en ejecución** el 2026-10-30 (`claims` en Docker con su PostgreSQL y el barrido del
> triage cada 2 s, el stub del core de pólizas detrás de Toxiproxy y Cognito local de Moto con verificación JWT RS256 real de
> `pkg/security`; el entorno de `tests/e2e/hu-w2.2`). Los identificadores, montos y fechas de los ejemplos son los de esa
> ejecución. Contrato formal: `services/claims/api/openapi.yaml` (versión 0.6.0).

---

## 1. Resumen del flujo

```
claims (dentro del servicio, sin intervención manual)
──────────────────────────────────────────────────────────────────────────────────────────────────────────
radicación (web o móvil) ─▶ caso en evaluación ─▶ barrido cada 10 s ─▶ nivel Triage 1/2/3 registrado    [CA-1, CA-2]
cambia el monto, el tipo o la cobertura ─────────▶ barrido ──────────▶ nivel recalculado con su causa   [CA-3]

Portal del analista (rol operations)                claims
───────────────────────────────────────            ───────────────────────────────────────────────────
GET  <página de casos de la cola> ───────────────▶ (listado de casos de HU-W2.2-03)
GET  /v1/claims/triage?claim_id=…&claim_id=… ────▶ nivel, severidad, banda y objetivo de cada fila      [CA-4, CA-5]
GET  /v1/claims/triage-policy ───────────────────▶ objetivo de cada nivel para los indicadores          [CA-5]
GET  /v1/claims/{claim_id}/triage ───────────────▶ prioridad del detalle e historial «Triage N asignado» [CA-4]

Portal del asegurado (rol insured)
GET  /v1/claims/{claim_id}/triage ───────────────▶ nivel y tiempo estimado de resolución                [CA-4]
POST /v1/claims/web-filings (+ estimated_amount_minor) ─▶ el monto estimado que declara al radicar
```

| Criterio | Cómo |
|---|---|
| CA-1 Al entrar a evaluación, prioridad automática en ≤ 60 s, con fecha y hora | El servicio la calcula solo; `state: pending` hasta entonces y `assessed_at` después |
| CA-2 Nivel por severidad (tipo y cobertura) y rango de monto | `current.severity`, `current.amount_band` y la tabla de §3 |
| CA-3 Recálculo automático al cambiar información relevante, con causa y fecha y hora | `history[]` con `causes[]` y `assessed_at` |
| CA-4 Nivel con severidad y rango en el listado y el detalle; el cliente ve nivel y tiempo estimado | Lote para la cola, `GET …/{id}/triage` para el detalle (vista `operations` o `insured`) |
| CA-5 Objetivo de tiempo por nivel en el listado | `target` en cada respuesta y `GET /v1/claims/triage-policy` |

---

## 2. Endpoints

### Cabeceras comunes

- `Authorization: Bearer <ID token de Cognito>`. Sin token, `401`. El rol que acepta cada ruta está en su sección; con otro rol,
  `403`.
- Todas las respuestas llevan `Cache-Control: no-store`.
- Error: `{"error": "<código estable>", "message": "<texto en inglés, no mostrar>"}`. El front muestra su propio texto según `error`.

### 2.1 Estados del triage de un caso

| `state` | Qué significa | Qué hace el front |
|---|---|---|
| `pending` | El caso está en evaluación y todavía no tiene nivel (máximo 60 s después de radicarse) | «Calculando prioridad…» y volver a pedir en 5 s |
| `assessed` | Tiene nivel. Sigue visible aunque el caso salga de evaluación (pagado, rechazado, devuelto); ya no se recalcula | Mostrar el nivel |
| `not_applicable` | El caso nunca entró a evaluación | No mostrar prioridad |
| `not_found` | Solo en el lote: no existe un caso con ese id | Omitir la fila |

### 2.2 `GET /v1/claims/{claim_id}/triage` — prioridad de un caso

Roles: **`operations`** (vista completa) e **`insured` dueño del caso** (vista reducida). La respuesta trae `view` para
distinguirlas.

**Vista `operations`** (detalle «Evaluación del siniestro»: «Prioridad · Triage 2 · severidad media · monto bajo» y la línea
«Triage 2 asignado · automático» del historial):

```json
{
  "view": "operations",
  "claim_id": "c6dd0d52-73b3-495e-8fb2-291f8bf6214e",
  "state": "assessed",
  "current": {
    "level": 1,
    "severity": "medium",
    "amount_band": "high",
    "estimated_amount_minor": 4000000000,
    "currency": "COP",
    "incident_type": "water_damage",
    "coverage": "water_damage",
    "policy_version": "triage-policy-v1",
    "target": {
      "kind": "immediate",
      "business_days": null,
      "resolution_business_days": 2
    },
    "assessed_at": "2026-10-31T04:20:38.278329Z"
  },
  "history": [
    {
      "level": 1,
      "previous_level": 2,
      "severity": "medium",
      "amount_band": "high",
      "estimated_amount_minor": 4000000000,
      "incident_type": "water_damage",
      "causes": [
        "estimated_amount_changed"
      ],
      "actor": "system",
      "policy_version": "triage-policy-v1",
      "assessed_at": "2026-10-31T04:20:38.278329Z"
    },
    {
      "level": 2,
      "previous_level": null,
      "severity": "medium",
      "amount_band": "low",
      "estimated_amount_minor": 480000000,
      "incident_type": "water_damage",
      "causes": [
        "entered_evaluation"
      ],
      "actor": "system",
      "policy_version": "triage-policy-v1",
      "assessed_at": "2026-10-31T04:20:28.282377Z"
    }
  ]
}
```

- El ejemplo es un daño por agua radicado con $ 4.800.000 (Triage 2) al que luego se le cambió el monto a $ 40.000.000 con `psql`,
  como lo harán las historias del sprint 3: el recálculo quedó con su causa y el nivel anterior.
- `history` va del más reciente al más antiguo. El historial del front muestra cada fila como «Triage N asignado · automático»
  (primera fila) o «Prioridad recalculada: Triage A → Triage B · <causa>».
- `previous_level` igual a `level` significa que cambió la información pero no el nivel: también queda registrado.
- `estimated_amount_minor` está en **centavos**: `480000000` = $ 4.800.000 COP. `null` si el caso no tenía monto.

**Vista `insured`** (detalle «Mis siniestros», etapa *En evaluación*: «Prioridad Triage 2 · Tiempo estimado de resolución: 5
días hábiles»):

```json
{
  "view": "insured",
  "claim_id": "8cde7aff-a3ed-4e19-8b9a-ba25f33d6b6a",
  "state": "assessed",
  "level": 3,
  "estimated_resolution_business_days": 10,
  "assessed_at": "2026-10-31T04:20:36.276289Z"
}
```

Antes del primer cálculo (capturada segundos después de radicar un hurto):

```json
{
  "view": "insured",
  "claim_id": "c3ada1e6-6394-43c7-a678-ae7a86f41898",
  "state": "pending",
  "level": null,
  "estimated_resolution_business_days": null,
  "assessed_at": null
}
```

Sin severidad, monto ni historial.

| HTTP | `error` | Cuándo |
|---|---|---|
| `403` | `forbidden` | Rol `advisor` o `partner`, o un `insured` que pide un caso ajeno |
| `404` | `claim_not_found` | El id no tiene la forma de un id de caso o no existe |
| `503` | `claims_unavailable` | No se pudo leer; reintentar |

### 2.3 `GET /v1/claims/triage?claim_id=…` — prioridad de una página de casos

Rol: **`operations`**. Para la «Cola de siniestros»: con los `claim_id` de la página que se está mostrando (1 a 50, sin repetir),
**una** llamada trae la prioridad de todas las filas, en el mismo orden.

```
GET /v1/claims/triage?claim_id=c6dd0d52-73b3-495e-8fb2-291f8bf6214e&claim_id=c762e410-b71c-4536-8951-f130504290fe&claim_id=00000000-0000-4000-8000-000000000000
```

```json
{
  "items": [
    {
      "claim_id": "c6dd0d52-73b3-495e-8fb2-291f8bf6214e",
      "state": "assessed",
      "level": 1,
      "severity": "medium",
      "amount_band": "high",
      "target": {
        "kind": "immediate",
        "business_days": null,
        "resolution_business_days": 2
      },
      "assessed_at": "2026-10-31T04:20:38.278329Z"
    },
    {
      "claim_id": "c762e410-b71c-4536-8951-f130504290fe",
      "state": "assessed",
      "level": 1,
      "severity": "high",
      "amount_band": "unknown",
      "target": {
        "kind": "immediate",
        "business_days": null,
        "resolution_business_days": 2
      },
      "assessed_at": "2026-10-31T04:20:30.281473Z"
    },
    {
      "claim_id": "00000000-0000-4000-8000-000000000000",
      "state": "not_found",
      "level": null,
      "severity": null,
      "amount_band": null,
      "target": null,
      "assessed_at": null
    }
  ]
}
```

- Un id que no existe vuelve con `state: not_found` y no hace fallar la llamada.
- El monto no viene aquí: lo trae el listado de casos.

| HTTP | `error` | Cuándo |
|---|---|---|
| `400` | `invalid_request` | Sin `claim_id`, más de 50, repetidos o alguno sin la forma de un id de caso |
| `403` | `forbidden` | Rol distinto de `operations` |
| `503` | `claims_unavailable` | No se pudo leer; reintentar |

### 2.4 `GET /v1/claims/triage-policy` — política vigente

Rol: **`operations`**. Para los indicadores por nivel de la cola («Triage 1 · Atender primero», «Triage 2 · Objetivo: 5 días
hábiles», «Triage 3 · Objetivo: 10 días hábiles») y para explicar en el encabezado cómo se calcula la prioridad.

```json
{
  "version": "triage-policy-v1",
  "currency": "COP",
  "amount_thresholds": {
    "low_below_minor": 500000000,
    "high_above_minor": 3000000000
  },
  "severity": [
    {
      "line": "auto",
      "incident_type": "collision",
      "severity": "medium"
    },
    {
      "line": "auto",
      "incident_type": "theft",
      "severity": "high"
    },
    {
      "line": "auto",
      "incident_type": "third_party_damage",
      "severity": "high"
    },
    {
      "line": "auto",
      "incident_type": "glass",
      "severity": "low"
    },
    {
      "line": "auto",
      "incident_type": "other",
      "severity": "medium"
    },
    {
      "line": "home",
      "incident_type": "water_damage",
      "severity": "medium"
    }
  ],
  "targets": [
    {
      "level": 1,
      "target": {
        "kind": "immediate",
        "business_days": null,
        "resolution_business_days": 2
      }
    },
    {
      "level": 2,
      "target": {
        "kind": "business_days",
        "business_days": 5,
        "resolution_business_days": 5
      }
    },
    {
      "level": 3,
      "target": {
        "kind": "business_days",
        "business_days": 10,
        "resolution_business_days": 10
      }
    }
  ]
}
```

El front **no** debe fijar estos valores en su código: cambian con la versión de la política.

### 2.5 `POST /v1/claims/web-filings` — monto estimado al radicar (cambio en HU-W2.1-01)

El formulario «Radicar un siniestro» gana el campo opcional **«Monto estimado»**. El front lo pide en pesos y lo envía en
centavos:

```json
{ "...": "los campos de HU-W2.1-01", "estimated_amount_minor": 480000000 }
```

- Opcional. Sin él, el caso no tiene monto y su banda es `unknown` (la prioridad sale solo de la severidad).
- Entero entre 1 y 10¹³. Fuera de rango → `422 validation_failed` con `{"field": "estimated_amount_minor", "code":
  "out_of_range"}`; un texto o un decimal → `invalid_format`. Se reporta junto con los demás campos inválidos.
- `GET /v1/claims/{claim_id}` lo devuelve en `estimated_amount_minor` («Datos reportados · Monto estimado»). Si no se declaró, el
  campo **no viene** en la respuesta (tampoco en un caso radicado desde el móvil): el front muestra «—».
- El monto forma parte de la radicación: la misma `Idempotency-Key` con otro monto responde `422 idempotency_key_reused`.

### 2.6 Errores (capturados)

```
403  {"error": "forbidden", "message": "the caller is not allowed to perform this operation"}     ← otro asegurado pide el triage del caso
404  {"error": "claim_not_found", "message": "there is no claim with that id"}
400  {"error": "invalid_request", "message": "every claim_id must be a claim id, without repeats"}   ← lote con claim_id=x
422  {"error": "validation_failed", "message": "one or more fields are missing or invalid", "fields": [{"field": "estimated_amount_minor", "code": "invalid_format"}]}   ← radicación con "estimated_amount_minor": "4.8 millones"
```

---

## 3. Cómo se calcula el nivel (`triage-policy-v1`, provisional)

| | Banda `low` (< $ 5.000.000) | Banda `medium` ($ 5.000.000 – $ 30.000.000) | Banda `high` (> $ 30.000.000) | Sin monto (`unknown`) |
|---|---|---|---|---|
| Severidad `high` (hurto, daño a terceros) | Triage 1 | Triage 1 | Triage 1 | Triage 1 |
| Severidad `medium` (colisión, otro, daño por agua) | Triage 2 | Triage 2 | Triage 1 | Triage 2 |
| Severidad `low` (cristales) | **Triage 3** | Triage 2 | Triage 1 | Triage 2 |

| Nivel | Objetivo de atención (cola) | Tiempo estimado de resolución (asegurado) |
|---|---|---|
| Triage 1 | Atención inmediata | 2 días hábiles |
| Triage 2 | 5 días hábiles | 5 días hábiles |
| Triage 3 | 10 días hábiles | 10 días hábiles |

**Pendiente de validación con negocio** (severidad, umbrales y plazos). Si cambia, cambia `version` y los casos en evaluación se
recalculan solos con la causa `policy_updated`.

### Causas del recálculo (`causes[]`)

| Causa | Cuándo |
|---|---|
| `entered_evaluation` | Primer cálculo, al entrar a evaluación |
| `estimated_amount_changed` | Cambió el monto estimado |
| `incident_type_changed` | Cambió el tipo de siniestro |
| `coverage_changed` | Cambió la cobertura aplicable |
| `provider_report_received` | Llegó el informe del prestador (se activará con HU-W2.3-02) |
| `policy_updated` | Cambió la versión de la política de triage |

---

## 4. Tratamiento de datos

- La prioridad la calcula siempre el sistema (`actor: system`); nadie la asigna a mano.
- Cada cálculo queda en un historial que no se modifica ni se borra.
- El asegurado ve solo el nivel y el tiempo estimado de su propio caso, nunca la severidad, el monto con el que se calculó ni el
  historial.
- El triage no consulta servicios externos: usa lo que quedó guardado en el caso.

---

## 5. Lo que queda para otras historias

- **Orden de la cola por prioridad y tiempo de radicación:** HU-W2.2-02.
- **Listado de casos en evaluación del analista:** HU-W2.2-03. Este contrato le agrega la prioridad por lote (§2.3).
- **Recálculos por la aprobación del asesor, la devolución y el informe del prestador** (HU-W2.1-03, HU-W2.1-04, HU-W2.3-02,
  sprint 3): cuando esas historias cambien el caso, el nivel se recalcula solo; este contrato no cambia.
- **Monto estimado en la app móvil:** el móvil radica sin monto hasta que una historia lo agregue.
