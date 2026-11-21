# Contrato de API — HU-W2.3-03 · Atención de solicitudes de asistencia entrantes

Servicio: **siniestros** (`claims`). Consumidor: portal web del analista de operaciones (pantallas «Asistencias en sitio» y
«Detalle de la asistencia»).

> **Fuente única para el front.** Contrato formal: `services/claims/api/openapi.yaml` (versión 0.18.0). Los ejemplos son
> **respuestas capturadas del binario en Docker** (`CAPTURE_DIR=… ./tests/e2e/hu-w2.3-03/run.sh`), con la ubicación del mockup
> (4,7712 N · 74,0452 O, Autopista Norte, Bogotá), los usuarios y pólizas de prueba de Moto y del stub del core, y los
> prestadores de la semilla. Las horas son RFC 3339 en UTC y pueden traer fracciones de segundo; el portal las muestra en hora
> de Colombia (y en UTC donde el mockup lo pide).

---

## 1. Resumen del flujo

```
App del asegurado (HU-M2.3-01)        claims                              Portal del analista (rol operations)
──────────────────────────────        ──────────────────────────────      ─────────────────────────────────────────────
POST /v1/claims/assistance-requests ▶ assistance_request (status new) ◀── GET /v1/claims/assistance-inbox          [CA-1]
                                                                           (al entrar, cada 30 s y con «Actualizar»)
                                                                      ◀── POST …/assistance-inbox/{id}/attend      new → in_call
                                                                      ◀── GET  …/assistance-inbox/{id}             detalle + sugerencia [CA-2]
                                       sin GPS:                       ◀── POST …/{id}/voice-location               dirección + pin
                                                                      ◀── POST …/{id}/need                         necesidad → especialidad
                                       assessors.Suggest (HU-W2.3-01): disponibles hoy, con la especialidad,
                                       5 → 10 → 20 km, el más cercano, con distancia y llegada estimada
                                                                      ◀── POST …/{id}/dispatch                     in_call → dispatched [CA-3]
                                       correo al prestador (reintentos)
                                                                      ◀── POST …/{id}/close                        → closed, con motivo [CA-4]
                                       cada decisión: analista + hora (history)                                     [CA-5]
GET /v1/claims/assistance-requests/{id}  el asegurado ve el estado nuevo (sin datos del prestador)
GET /v1/claims/assistance-policy         necesidades por línea, radios, llegada estimada, motivos de cierre
```

| Criterio | Cómo |
|---|---|
| CA-1 En la bandeja en ≤ 60 s, con póliza, fecha y hora, estado de ubicación y mapa si hay coordenadas | La solicitud aparece en `GET /v1/claims/assistance-inbox` en cuanto se registra; el portal consulta cada 30 s. Cada fila trae `policy`, `requested_at` y `location` (con `latitude`/`longitude`/`accuracy_m` solo si `status: sent`) |
| CA-2 Al abrir, la información y la sugerencia con el mecanismo de HU-W2.3-01 | `GET …/{id}`: `suggestion` con candidatos, distancia, `eta_minutes` y `suggested: true` |
| CA-3 Despacho: el prestador recibe el caso con la ubicación; estado Despachada con fecha, hora y prestador | `POST …/dispatch`: `status: dispatched`, `dispatched_at`, `dispatch.provider`, `dispatch.location_shared`, `dispatch.notice` |
| CA-4 Cierre por otro medio con motivo obligatorio, sin despachar | `POST …/close` con `reason` (y `note` con `other`): `status: closed`, `closure`, `dispatch: null` |
| CA-5 Analista, decisiones y fechas | `history[]` (acción, `by`, `at`, `detail`), `handled_by`, `need.recorded_by`, `dispatch.dispatched_by`, `closure.closed_by` |

---

## 2. Endpoints

### Cabeceras comunes

- `Authorization: Bearer <ID token de Cognito>`. Sin token, `401`. **Todas las rutas son solo del rol `operations`**; asegurado,
  asesor y socio reciben `403`.
- Todas las respuestas llevan `Cache-Control: no-store`.
- Error: `{"error": "<código estable>", "message": "<texto en inglés, no mostrar>"}`. El front muestra su propio texto según `error`.
- Cuerpos JSON: un campo desconocido o un cuerpo que no es un objeto → `400 invalid_request`. Un campo con valor inválido →
  `422 validation_failed` con **todos** los campos inválidos a la vez en `fields[]` (`required`, `invalid_format`,
  `out_of_range`).
- Un `request_id` que no es un UUID v4 responde `404 assistance_request_not_found`.
- **Todas las escrituras responden `200` con la solicitud completa** (`AssistanceCase`, la misma forma de §2.3): el portal
  repinta la pantalla con la respuesta.

### 2.1 Estados de una solicitud

| `status` | Mockup | Cómo se llega | Qué se puede hacer |
|---|---|---|---|
| `new` | *Nueva* | La registra la app (HU-M2.3-01) | Atender, Cerrar |
| `in_call` | *En llamada* | «Atender» (`POST …/attend`) | Continuar (otro analista la toma), necesidad, ubicación por voz, Despachar, Cerrar |
| `dispatched` | *Despachada* | «Despachar» (`POST …/dispatch`) | Ver (final) |
| `closed` | *Cerrada* | «Cerrar por otro medio» (`POST …/close`) | Ver (final) |

Abrir el detalle **no** cambia el estado. De dos analistas que despachan o cierran la misma solicitud gana uno; el otro recibe
`409 assistance_not_open` y debe volver a leer el detalle.

### 2.2 `GET /v1/claims/assistance-inbox` — bandeja

Parámetros: `status` (repetible: `?status=new&status=in_call`; sin él, todos) y `limit` (1–100, por defecto 50). Orden: la más
reciente primero. `summary` son los indicadores del mockup y **no** dependen de los filtros; «hoy» es el día de Colombia.

```json
{
  "summary": {
    "new_count": 2,
    "in_call_count": 0,
    "dispatched_today": 0,
    "mean_dispatch_seconds_today": null,
    "dispatch_target_seconds": 300
  },
  "items": [
    {
      "request_id": "bc8adc67-47aa-47ed-bd0f-5a1466582d32",
      "request_number": "AST-2026-0002",
      "status": "new",
      "requester_name": "Carolina Pérez",
      "policy": {
        "policy_id": "AU-2026-868766",
        "line": "auto",
        "product": "Autos",
        "insured_object": "IZJ-766"
      },
      "location": {
        "status": "unavailable",
        "latitude": null,
        "longitude": null,
        "accuracy_m": null,
        "captured_at": null,
        "unavailable_reason": "no_fix",
        "place": null,
        "voice": null
      },
      "requested_at": "2026-11-21T16:16:11.023796Z",
      "handled_by": null,
      "dispatched_at": null,
      "closed_at": null
    },
    {
      "request_id": "184a52ab-8b1a-4715-ac8e-d124e121fe33",
      "request_number": "AST-2026-0001",
      "status": "new",
      "requester_name": "Carolina Pérez",
      "policy": {
        "policy_id": "AU-2026-868766",
        "line": "auto",
        "product": "Autos",
        "insured_object": "IZJ-766"
      },
      "location": {
        "status": "sent",
        "latitude": 4.7712,
        "longitude": -74.0452,
        "accuracy_m": 8,
        "captured_at": "2026-11-21T16:16:04Z",
        "unavailable_reason": null,
        "place": "Autopista Norte km 12, Bogotá",
        "voice": null
      },
      "requested_at": "2026-11-21T16:16:10.985834Z",
      "handled_by": null,
      "dispatched_at": null,
      "closed_at": null
    }
  ]
}
```

- **Mapa:** solo con `location.status: sent` (o con `location.voice`, que trae el pin del analista).
- **«Sin GPS · ubicación por voz»:** `location.status: unavailable`; el motivo está en `unavailable_reason`
  (`permission_denied`, `location_disabled`, `no_fix`) y la dirección dictada, si ya se registró, en `location.voice.address`.
- **Teléfono del asegurado:** el backend **no** lo tiene (el token no lo trae). La columna del mockup muestra solo el nombre;
  `requester_name` es `null` si el token no traía nombre.
- **Acción de la fila:** `new` → «Atender»; `in_call` → «Continuar»; `dispatched`/`closed` → «Ver».
- `mean_dispatch_seconds_today` es `null` si hoy no hubo despachos.

Errores: `400 invalid_request` (estado desconocido o `limit` fuera de 1–100), `503 assistance_unavailable`.

### 2.3 `GET /v1/claims/assistance-inbox/{request_id}` — detalle

La solicitud de §2.2 más `need`, `suggestion`, `dispatch`, `closure` e `history`. Ejemplo capturado: AST-2026-0001 en
llamada, con la necesidad «grúa» registrada (es la respuesta de §2.5; el `GET` responde lo mismo). Los candidatos son **todos** los
prestadores con la especialidad, del más cercano al más lejano, cada uno con su `eligibility`.

```json
{
  "request_id": "184a52ab-8b1a-4715-ac8e-d124e121fe33",
  "request_number": "AST-2026-0001",
  "status": "in_call",
  "requester_name": "Carolina Pérez",
  "policy": {
    "policy_id": "AU-2026-868766",
    "line": "auto",
    "product": "Autos",
    "insured_object": "IZJ-766"
  },
  "location": {
    "status": "sent",
    "latitude": 4.7712,
    "longitude": -74.0452,
    "accuracy_m": 8,
    "captured_at": "2026-11-21T16:16:04Z",
    "unavailable_reason": null,
    "place": "Autopista Norte km 12, Bogotá",
    "voice": null
  },
  "requested_at": "2026-11-21T16:16:10.985834Z",
  "handled_by": {
    "email": "operaciones.local@example.com",
    "name": "Andrés Ruiz"
  },
  "dispatched_at": null,
  "closed_at": null,
  "need": {
    "code": "towing",
    "specialty": "towing",
    "recorded_by": {
      "email": "operaciones.local@example.com",
      "name": "Andrés Ruiz"
    },
    "recorded_at": "2026-11-21T16:16:11.309446Z"
  },
  "suggestion": {
    "state": "found",
    "specialty": "towing",
    "location_source": "app",
    "from_radius_km": 5,
    "radius_km": 5,
    "reason": null,
    "policy_version": "assignment-policy-v1",
    "attempts": [
      {
        "radius_km": 5,
        "eligible_count": 2,
        "attempted_at": "2026-11-21T16:16:11.311729385Z"
      }
    ],
    "candidates": [
      {
        "provider_id": "prv-gruas-autopista-norte",
        "name": "Grúas Autopista Norte",
        "specialties": [
          "towing"
        ],
        "distance_km": 0.9,
        "availability": "available",
        "unavailable_until": null,
        "eligibility": "eligible",
        "suggested": true,
        "eta_minutes": 12,
        "dispatched_today": 0
      },
      {
        "provider_id": "prv-auxilio-vial-norte",
        "name": "Auxilio Vial Norte",
        "specialties": [
          "roadside_assistance",
          "towing"
        ],
        "distance_km": 2.2,
        "availability": "available",
        "unavailable_until": null,
        "eligibility": "eligible",
        "suggested": false,
        "eta_minutes": 15,
        "dispatched_today": 0
      },
      {
        "provider_id": "prv-gruas-calle-80",
        "name": "Grúas Calle 80",
        "specialties": [
          "towing"
        ],
        "distance_km": 9.9,
        "availability": "available",
        "unavailable_until": null,
        "eligibility": "out_of_radius",
        "suggested": false,
        "eta_minutes": 30,
        "dispatched_today": 0
      },
      {
        "provider_id": "prv-gruas-del-valle",
        "name": "Grúas del Valle",
        "specialties": [
          "towing",
          "roadside_assistance"
        ],
        "distance_km": 297.6,
        "availability": "available",
        "unavailable_until": null,
        "eligibility": "out_of_radius",
        "suggested": false,
        "eta_minutes": 606,
        "dispatched_today": 0
      }
    ]
  },
  "dispatch": null,
  "closure": null,
  "history": [
    {
      "action": "attended",
      "by": {
        "email": "operaciones.local@example.com",
        "name": "Andrés Ruiz"
      },
      "at": "2026-11-21T16:16:11.220159Z",
      "detail": null
    },
    {
      "action": "need_recorded",
      "by": {
        "email": "operaciones.local@example.com",
        "name": "Andrés Ruiz"
      },
      "at": "2026-11-21T16:16:11.309446Z",
      "detail": {
        "need": "towing"
      }
    }
  ]
}
```

**`suggestion.state`**

| Estado | Qué muestra el portal |
|---|---|
| `need_missing` | Pide elegir la necesidad (§2.5). Sin búsqueda: `attempts` y `candidates` vacíos |
| `location_missing` | Solicitud sin GPS y sin ubicación por voz: formulario de dirección + pin (§2.6) |
| `found` | Candidatos; *Despachar* habilitado **solo** para `eligibility: eligible`; el sugerido destacado |
| `exhausted` | Nadie elegible en 20 km (`reason: no_eligible_provider_within_max_radius`, tres intentos). Volver a leer el detalle busca otra vez; o cerrar con `no_provider_available` |
| `not_applicable` | Solicitud despachada o cerrada: no se busca |

- La sugerencia se **calcula en cada lectura** (no se guarda). Las distancias son en línea recta, redondeadas a 0,1 km.
- `eta_minutes` = 10 + ⌈distancia / 30 km/h × 60⌉ (provisional; §4). Es una estimación, no una ruta.
- `dispatched_today` es informativo: **despachar no deja al prestador ocupado**.
- `history` va del más antiguo al más reciente; `detail` según la acción: `need_recorded` → `need`, `dispatched` →
  `provider_id`, `closed` → `reason`, `taken_over` → `from` (correo del analista anterior).

### 2.4 `POST /v1/claims/assistance-inbox/{request_id}/attend` — atender

Sin cuerpo. `new` → `in_call` y `handled_by` = quien atiende («Atender»). Sobre una `in_call` de **otro** analista, la toma
(acción `taken_over`, «Continuar»); del mismo analista no cambia nada. Responde `200 AssistanceCase`.

Errores: `409 assistance_not_open` (ya despachada o cerrada).

### 2.5 `POST /v1/claims/assistance-inbox/{request_id}/need` — necesidad

```json
{"need": "towing"}
```

Las necesidades de cada línea están en la política (§2.9). Se puede cambiar mientras la solicitud esté `in_call`; vale la
última. La respuesta trae la sugerencia de la especialidad nueva.

Errores: `409 assistance_not_attended` (está `new`: atiéndela primero), `409 assistance_not_open`, `422 need_not_for_line` (la
necesidad no es de la línea de la póliza; Viaje no tiene ninguna), `422 validation_failed` (`need` falta o no es del catálogo).

### 2.6 `POST /v1/claims/assistance-inbox/{request_id}/voice-location` — ubicación por voz

Solo para solicitudes **sin** coordenadas del teléfono. El portal resuelve la dirección en el mapa (pin); el backend no
geocodifica.

```json
{"address": "Vía Cali–Palmira km 4", "latitude": 3.4921, "longitude": -76.4203}
```

`address` 5–200 caracteres; `latitude` −90…90; `longitude` −180…180; las tres obligatorias. Se puede corregir mientras esté
`in_call` (vale la última). La respuesta trae `location.voice` y la sugerencia desde ese punto (`location_source: voice`).

Respuesta capturada (`200`, AST-2026-0002 sin GPS, que Diana tomó de Andrés; se omiten los campos iguales a §2.3):

```json
{
  "request_number": "AST-2026-0002",
  "status": "in_call",
  "location": {
    "status": "unavailable",
    "latitude": null,
    "longitude": null,
    "accuracy_m": null,
    "captured_at": null,
    "unavailable_reason": "no_fix",
    "place": null,
    "voice": {
      "address": "Vía Cali–Palmira km 4",
      "latitude": 3.4921,
      "longitude": -76.4203,
      "recorded_by": {
        "email": "operaciones.turno2.local@example.com",
        "name": "Diana Torres"
      },
      "recorded_at": "2026-11-21T16:16:11.513872Z"
    }
  },
  "handled_by": {
    "email": "operaciones.turno2.local@example.com",
    "name": "Diana Torres"
  },
  "suggestion": {
    "state": "found",
    "specialty": "towing",
    "location_source": "voice",
    "from_radius_km": 5,
    "radius_km": 5,
    "reason": null,
    "policy_version": "assignment-policy-v1",
    "attempts": [
      {
        "radius_km": 5,
        "eligible_count": 1,
        "attempted_at": "2026-11-21T16:16:11.516142552Z"
      }
    ],
    "candidates": [
      {
        "provider_id": "prv-gruas-del-valle",
        "name": "Grúas del Valle",
        "specialties": [
          "towing",
          "roadside_assistance"
        ],
        "distance_km": 1.9,
        "availability": "available",
        "unavailable_until": null,
        "eligibility": "eligible",
        "suggested": true,
        "eta_minutes": 14,
        "dispatched_today": 0
      },
      {
        "provider_id": "prv-gruas-calle-80",
        "name": "Grúas Calle 80",
        "specialties": [
          "towing"
        ],
        "distance_km": 294.8,
        "availability": "available",
        "unavailable_until": null,
        "eligibility": "out_of_radius",
        "suggested": false,
        "eta_minutes": 600,
        "dispatched_today": 0
      },
      {
        "provider_id": "prv-auxilio-vial-norte",
        "name": "Auxilio Vial Norte",
        "specialties": [
          "roadside_assistance",
          "towing"
        ],
        "distance_km": 298.3,
        "availability": "available",
        "unavailable_until": null,
        "eligibility": "out_of_radius",
        "suggested": false,
        "eta_minutes": 607,
        "dispatched_today": 0
      },
      {
        "provider_id": "prv-gruas-autopista-norte",
        "name": "Grúas Autopista Norte",
        "specialties": [
          "towing"
        ],
        "distance_km": 299.8,
        "availability": "available",
        "unavailable_until": null,
        "eligibility": "out_of_radius",
        "suggested": false,
        "eta_minutes": 610,
        "dispatched_today": 0
      }
    ]
  },
  "history": [
    {
      "action": "attended",
      "by": {
        "email": "operaciones.local@example.com",
        "name": "Andrés Ruiz"
      },
      "at": "2026-11-21T16:16:11.389133Z",
      "detail": null
    },
    {
      "action": "taken_over",
      "by": {
        "email": "operaciones.turno2.local@example.com",
        "name": "Diana Torres"
      },
      "at": "2026-11-21T16:16:11.416756Z",
      "detail": {
        "from": "operaciones.local@example.com"
      }
    },
    {
      "action": "need_recorded",
      "by": {
        "email": "operaciones.turno2.local@example.com",
        "name": "Diana Torres"
      },
      "at": "2026-11-21T16:16:11.454012Z",
      "detail": {
        "need": "towing"
      }
    },
    {
      "action": "voice_location_recorded",
      "by": {
        "email": "operaciones.turno2.local@example.com",
        "name": "Diana Torres"
      },
      "at": "2026-11-21T16:16:11.513872Z",
      "detail": null
    }
  ]
}
```

Errores: `409 location_from_app` (el teléfono compartió coordenadas), `409 assistance_not_attended`, `409 assistance_not_open`,
`422 validation_failed`.

### 2.7 `POST /v1/claims/assistance-inbox/{request_id}/dispatch` — despachar

```json
{"provider_id": "prv-gruas-autopista-norte"}
```

El sugerido o cualquier `eligible`. La búsqueda **se repite al despachar**: si el prestador dejó de ser elegible desde que se
leyó el detalle, `422 provider_not_eligible` y el portal vuelve a leer el detalle. Respuesta (`200`, se omiten los campos
iguales a §2.3):

```json
{
  "request_number": "AST-2026-0001",
  "status": "dispatched",
  "dispatched_at": "2026-11-21T16:16:11.606015Z",
  "suggestion": {
    "state": "not_applicable",
    "specialty": null,
    "location_source": null,
    "from_radius_km": null,
    "radius_km": null,
    "reason": null,
    "policy_version": "assignment-policy-v1",
    "attempts": [],
    "candidates": []
  },
  "dispatch": {
    "provider": {
      "id": "prv-gruas-autopista-norte",
      "name": "Grúas Autopista Norte",
      "contact_email": "despachos@gruasautopistanorte.example.com",
      "contact_phone": "+576015550301"
    },
    "need": "towing",
    "specialty": "towing",
    "distance_km": 0.9,
    "eta_minutes": 12,
    "location_shared": {
      "source": "app",
      "latitude": 4.7712,
      "longitude": -74.0452,
      "accuracy_m": 8,
      "address": null,
      "place": "Autopista Norte km 12, Bogotá"
    },
    "suggested_provider_id": "prv-gruas-autopista-norte",
    "dispatched_by": {
      "email": "operaciones.local@example.com",
      "name": "Andrés Ruiz"
    },
    "dispatched_at": "2026-11-21T16:16:11.606015Z",
    "policy_versions": {
      "assistance": "assistance-policy-v1",
      "assignment": "assignment-policy-v1"
    },
    "notice": {
      "status": "pending",
      "attempts": 0,
      "sent_at": null
    }
  },
  "closure": null,
  "history": [
    {
      "action": "attended",
      "by": {
        "email": "operaciones.local@example.com",
        "name": "Andrés Ruiz"
      },
      "at": "2026-11-21T16:16:11.220159Z",
      "detail": null
    },
    {
      "action": "need_recorded",
      "by": {
        "email": "operaciones.local@example.com",
        "name": "Andrés Ruiz"
      },
      "at": "2026-11-21T16:16:11.309446Z",
      "detail": {
        "need": "towing"
      }
    },
    {
      "action": "dispatched",
      "by": {
        "email": "operaciones.local@example.com",
        "name": "Andrés Ruiz"
      },
      "at": "2026-11-21T16:16:11.606015Z",
      "detail": {
        "provider_id": "prv-gruas-autopista-norte"
      }
    }
  ]
}
```

- `notice.status: pending` → el correo aún no sale (se reintenta solo); `sent` → entregado. El portal puede volver a leer el
  detalle para verlo cambiar.
- `dispatch.suggested_provider_id` es el que sugería el sistema al despachar; puede ser distinto de `provider.id`.

Errores: `409 assistance_not_attended`, `409 assistance_not_open` (otro analista ya la despachó o cerró), `422 need_missing`,
`422 location_missing`, `422 provider_not_eligible` (desconocido, no disponible, sin la especialidad o fuera del radio),
`422 validation_failed` (`provider_id` falta o no tiene forma de id).

### 2.8 `POST /v1/claims/assistance-inbox/{request_id}/close` — cerrar por otro medio

```json
{"reason": "resolved_by_phone"}
```

```json
{"reason": "other", "note": "El asegurado consiguió quien le pasara corriente"}
```

| `reason` | Texto sugerido |
|---|---|
| `resolved_by_phone` | Resuelta en la llamada |
| `duplicate` | Solicitud repetida |
| `cancelled_by_insured` | Cancelada por el asegurado |
| `not_covered` | No cubierta por la póliza |
| `no_provider_available` | Sin prestador disponible |
| `other` | Otro (con nota obligatoria) |

`note`: 3–500 caracteres, **obligatoria con `other`** y opcional con el resto. Desde `new` o `in_call`. No despacha a nadie ni
envía correo. Responde `200` con `status: closed`, `closed_at` y `closure` (`reason`, `note`, `closed_by`, `closed_at`).

Errores: `409 assistance_not_open`, `422 validation_failed` (`reason` falta o es desconocido; `note` falta con `other` o fuera
de 3–500).

Respuesta capturada (`200`, AST-2026-0003 de Hogar, cerrada desde `new` sin atenderla: `handled_by` y `need` quedan en
`null`):

```json
{
  "request_id": "9cef2741-03ec-4f85-a7df-8d75e9ecb6dc",
  "request_number": "AST-2026-0003",
  "status": "closed",
  "requester_name": "Carolina Pérez",
  "policy": {
    "policy_id": "HG-2026-266966",
    "line": "home",
    "product": "Hogar",
    "insured_object": "Calle 26 # 47-47, Bogotá"
  },
  "location": {
    "status": "unavailable",
    "latitude": null,
    "longitude": null,
    "accuracy_m": null,
    "captured_at": null,
    "unavailable_reason": "permission_denied",
    "place": null,
    "voice": null
  },
  "requested_at": "2026-11-21T16:16:20.562879Z",
  "handled_by": null,
  "dispatched_at": null,
  "closed_at": "2026-11-21T16:16:20.620241Z",
  "need": null,
  "suggestion": {
    "state": "not_applicable",
    "specialty": null,
    "location_source": null,
    "from_radius_km": null,
    "radius_km": null,
    "reason": null,
    "policy_version": "assignment-policy-v1",
    "attempts": [],
    "candidates": []
  },
  "dispatch": null,
  "closure": {
    "reason": "resolved_by_phone",
    "note": null,
    "closed_by": {
      "email": "operaciones.local@example.com",
      "name": "Andrés Ruiz"
    },
    "closed_at": "2026-11-21T16:16:20.620241Z"
  },
  "history": [
    {
      "action": "closed",
      "by": {
        "email": "operaciones.local@example.com",
        "name": "Andrés Ruiz"
      },
      "at": "2026-11-21T16:16:20.620241Z",
      "detail": {
        "reason": "resolved_by_phone"
      }
    }
  ]
}
```

### 2.9 `GET /v1/claims/assistance-policy` — política vigente

```json
{
  "version": "assistance-policy-v1",
  "needs": [
    {"line": "auto", "code": "towing", "specialty": "towing"},
    {"line": "auto", "code": "battery", "specialty": "roadside_assistance"},
    {"line": "auto", "code": "flat_tire", "specialty": "roadside_assistance"},
    {"line": "auto", "code": "fuel", "specialty": "roadside_assistance"},
    {"line": "auto", "code": "vehicle_lockout", "specialty": "roadside_assistance"},
    {"line": "home", "code": "plumbing_emergency", "specialty": "plumbing"}
  ],
  "radii_km": [5, 10, 20],
  "assignment_policy_version": "assignment-policy-v1",
  "eta": {"preparation_minutes": 10, "average_speed_kmh": 30},
  "close_reasons": ["resolved_by_phone", "duplicate", "cancelled_by_insured", "not_covered", "no_provider_available", "other"],
  "dispatch_target_seconds": 300
}
```

Textos sugeridos de las necesidades: `towing` Grúa · `battery` Batería · `flat_tire` Cambio de llanta · `fuel` Combustible ·
`vehicle_lockout` Llaves dentro del vehículo · `plumbing_emergency` Emergencia de plomería.

### 2.10 Errores

| HTTP | `error` | Cuándo |
|---|---|---|
| 400 | `invalid_request` | Cuerpo que no es objeto, campo desconocido, `status`/`limit` inválidos en la bandeja |
| 401 | — | Sin token o token inválido |
| 403 | `forbidden` | El token no es de `operations` |
| 404 | `assistance_request_not_found` | No existe o el id no es un UUID v4 |
| 409 | `assistance_not_open` | Ya despachada o cerrada |
| 409 | `assistance_not_attended` | Está `new`: atenderla primero |
| 409 | `location_from_app` | Ubicación por voz para una solicitud con coordenadas del teléfono |
| 422 | `need_not_for_line` | Necesidad que no es de la línea de la póliza |
| 422 | `need_missing` | Despachar sin necesidad |
| 422 | `location_missing` | Despachar sin coordenadas del teléfono ni ubicación por voz |
| 422 | `provider_not_eligible` | El prestador no es elegible ahora |
| 422 | `validation_failed` | Campos inválidos (`fields[]`) |
| 503 | `assistance_unavailable` | Reintentar |

Ejemplo (`422`, cerrar con `other` sin nota):

```json
{
  "error": "validation_failed",
  "message": "one or more fields are missing or invalid",
  "fields": [
    {
      "field": "note",
      "code": "required"
    }
  ]
}
```

---

## 3. Aviso al prestador (correo)

Sale cuando se despacha, con la llave de idempotencia `asistencia-<request_id>` (un reintento no duplica el correo). Si el
proveedor de correo no responde, el despacho queda registrado igual y el aviso se reintenta (5 s, 30 s, 2 min y luego cada
10 min).

El correo que recibió Grúas Autopista Norte en el e2e (para AST-2026-0001):

```
Asunto: Asistencia AST-2026-0001 · Grúa

Solventa le asigna la asistencia AST-2026-0001.

Necesidad: Grúa
Línea: Autos · IZJ-766
Ubicación: 4.7712, -74.0452 (precisión 8 m, compartida desde el teléfono)
Dirección: Autopista Norte km 12, Bogotá
Mapa: https://www.google.com/maps/search/?api=1&query=4.7712,-74.0452
Solicitada: 2026-11-21 16:16 UTC
```

Sin GPS, la línea de ubicación dice «indicada por voz» (por ejemplo `3.4921, -76.4203 (indicada por voz)`) y la dirección es
la que registró el analista. **No** lleva nombre,
documento ni teléfono del asegurado.

---

## 4. Cómo se sugiere el prestador (`assistance-policy-v1`, provisional)

1. La **necesidad** da la especialidad (§2.9).
2. La búsqueda es la de HU-W2.3-01 (`assignment-policy-v1`): **disponible hoy** + con la especialidad + dentro del radio;
   radios 5 → 10 → 20 km, se detiene en el primero con elegibles; distancia en línea recta desde la ubicación del teléfono o el
   pin del analista.
3. **Sugerido:** el elegible más cercano; en empate, el de `provider_id` menor.
4. **Llegada estimada:** 10 min de alistamiento + la distancia a 30 km/h, redondeada hacia arriba.

**Pendiente de validación con negocio** (necesidades, especialidades, llegada estimada, motivos de cierre y prestadores de la
carga inicial). Si cambia, cambia `version`.

---

## 5. Tratamiento de datos

- La ubicación de una persona es un dato sensible: solo la ve `operations` y solo se comparte con el prestador despachado.
- Cada decisión (atender, tomar, necesidad, ubicación por voz, despacho, cierre) queda en un historial que no se modifica ni se
  borra, con quién y cuándo. El despacho guarda una copia del prestador y de la ubicación tal como se compartieron.
- Lo que envió la app no se reescribe: solo cambia `status`.

---

## 6. Lo que queda para otras historias

- **Seguimiento después del despacho** (en camino, en sitio, servicio terminado), cancelación o re-despacho.
- **Ocupar al prestador** con una asistencia; seguimiento de la intervención de un siniestro: HU-W2.3-02.
- **Teléfono del asegurado** en la bandeja y **llamar desde el portal**.
- **Avisos al asegurado** del despacho (el asegurado ve el estado en su consulta de HU-M2.3-01).
- **Geocodificar la dirección** (la hace el portal con el pin) y **distancia o tiempo por vía**.
- **Necesidad enviada por la app** y asistencia en sitio para pólizas de Viaje.

### Diferencias con el mockup «Detalle de la asistencia»

- **Panel «Asegurada»:** el backend entrega el nombre (`requester_name`) y la póliza (id, línea, producto y objeto asegurado). No
  entrega el documento, la marca, el modelo ni el año del vehículo, si la póliza incluye asistencia ni los siniestros abiertos.
  El portal oculta esas líneas.
- **«Llamada en curso»:** el cronómetro y la línea de atención son del portal; el backend no los conoce.
- **Prestadores:** sin calificación ni número de servicios. La lista trae **todos** los prestadores con la especialidad (también
  los `out_of_radius`); el portal puede mostrar solo los `eligible` o separarlos.
- **Pines del mapa:** los candidatos no traen coordenadas, igual que en HU-W2.3-01. El portal las lee del registro
  (`GET /v1/claims/providers/{provider_id}`, rol `operations`).
- **Llegada estimada:** los minutos del mockup son ilustrativos. El backend aplica la regla provisional de §4.
- **Estado:** mientras el analista atiende, la solicitud está `in_call` («En llamada»), no «Nueva».
