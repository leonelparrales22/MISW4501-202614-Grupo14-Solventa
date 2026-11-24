# Contrato de API — HU-W2.3-01 · Asignación del prestador más cercano

Servicio: **siniestros** (`claims`). Consumidor: portal web del analista de operaciones (pantallas «Asignar prestador» y
«Prestador no encontrado · Pendiente de asignación», y la administración de prestadores).

> **Fuente única para el front.** Contrato formal: `services/claims/api/openapi.yaml` (versión 0.11.0). Los ejemplos de esta
> guía son **respuestas capturadas del binario en Docker** (entorno de `tests/e2e/hu-w2.3`, 2026-11-08) con los prestadores de
> la semilla y el incidente del mockup (4,6771 N · 74,0483 O, Bogotá). Los ids, radicados y horas son los de esa corrida. Las
> horas son RFC 3339 en UTC y pueden traer fracciones de segundo.

---

## 1. Resumen del flujo

```
Portal del analista (rol operations)                     claims
────────────────────────────────────────                ──────────────────────────────────────────────────────────
GET  /v1/claims/{claim_id}/assignment ─────────────────▶ estado, especialidad, ubicación, última búsqueda, asignación
  └ location_missing:
POST /v1/claims/{claim_id}/incident-location ──────────▶ dirección + coordenadas del pin                          [CA-1]
POST /v1/claims/{claim_id}/provider-searches ──────────▶ busca en 5 → 10 → 20 km, registra cada intento,
     («Buscar» / «Reintentar búsqueda»: sin cuerpo;        sugiere el disponible más cercano con la especialidad
      «Ampliar radio»: {"from_radius_km": 10})             o deja el caso pendiente de asignación con el motivo    [CA-2…CA-4, CA-6]
POST /v1/claims/{claim_id}/assignment ─────────────────▶ prestador, fecha y hora, distancia y ubicación compartida;
                                                           el prestador recibe la ubicación por correo              [CA-5]
GET  /v1/claims/assignments?claim_id=…&claim_id=… ─────▶ estado de la asignación de una página de casos
GET  /v1/claims/assignment-policy ─────────────────────▶ radios, especialidad por tipo y frecuencia del reintento

claims (sin intervención manual)
caso pendiente de asignación ─▶ cada 5 min busca otra vez ─▶ si aparece un elegible, el caso queda «proposed» (no asigna solo)

Administración de prestadores (rol operations)
GET  /v1/claims/providers[?specialty=&availability=] · POST /v1/claims/providers
GET  /v1/claims/providers/{provider_id} · POST …/profile · POST …/availability                                   [CA-2]
```

| Criterio | Cómo |
|---|---|
| CA-1 Coordenadas del incidente; si no hay, dirección alternativa antes de buscar | `location.source: app` (evidencia del teléfono) o `analyst` (§2.3); sin ninguna, `state: location_missing` |
| CA-2 Solo disponibles con la especialidad del tipo, del registro de prestadores | `specialty` del caso, `eligibility` de cada candidato y la administración de §2.7 |
| CA-3 Proponer el de menor distancia | `candidates[]` ordenados por `distance_km` y `suggested: true` |
| CA-4 Ampliar el radio y registrar cada intento | `last_search.attempts[]` |
| CA-5 Registrar la asignación y avisar al prestador | `assignment` (prestador, `assigned_at`, `distance_km`, `location_shared`) y `assignment.notice` |
| CA-6 Pendiente de asignación con el motivo, sin asignar a quien no cumpla | `state: pending_assignment`, `last_search.reason`; asignar a un no elegible → `422 provider_not_eligible` |

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
- **El estado del siniestro no cambia** con esta historia: un caso pendiente de asignación o asignado sigue `in_evaluation`.

### 2.1 Estados de la asignación de un caso

| `state` | Qué significa | Qué hace el front |
|---|---|---|
| `not_required` | El tipo de siniestro no requiere visita (hurto) | No ofrecer «Asignar prestador» |
| `not_applicable` | El caso no está en evaluación y no tiene asignación | No ofrecer «Asignar prestador» |
| `location_missing` | No hay coordenadas de la app ni ubicación registrada | Formulario «Dirección» + pin en el mapa (§2.3) |
| `ready` | Tiene ubicación y no se ha buscado (o la ubicación cambió después de la última búsqueda) | Botón «Buscar prestador» |
| `proposed` | La última búsqueda encontró elegibles | Tabla de candidatos, sugerido resaltado y «Asignar» en cada `eligible` |
| `pending_assignment` | La última búsqueda no encontró elegibles hasta el radio máximo | Aviso «No se encontró un prestador disponible» con `reason` y los intentos; «Reintentar búsqueda» |
| `assigned` | Prestador confirmado | Datos de la asignación; sin acciones |
| `not_found` | Solo en el lote: no existe un caso con ese id | Omitir la fila |

**Botón «Asignar» habilitado solo si `eligibility: eligible`.** Un prestador `unavailable` (no disponible, ocupado en otro
caso o «disponible mañana») o `out_of_radius` se muestra con la acción deshabilitada; para llegar a uno `out_of_radius` el
analista usa «Ampliar radio de búsqueda», que queda registrado como intento.

### 2.2 `GET /v1/claims/{claim_id}/assignment` — asignación de un caso

Choque radicado desde el teléfono, aprobado por el asesor y con la búsqueda hecha (`proposed`), pantalla «Asignar prestador»:

```json
{
  "claim_id": "d2fd673c-ee66-4763-9482-3cbee9a3fa79",
  "filing_number": "SIN-2026-00001",
  "claim_status": "in_evaluation",
  "incident_type": "collision",
  "line": "auto",
  "state": "proposed",
  "specialty": "vehicle_assessment",
  "policy_version": "assignment-policy-v1",
  "location": {
    "source": "app",
    "latitude": 4.6771,
    "longitude": -74.0483,
    "accuracy_m": 8,
    "address": null,
    "recorded_by": null,
    "recorded_at": "2026-11-07T14:38:30Z"
  },
  "last_search": {
    "seq": 1,
    "trigger": "analyst",
    "searched_by": {"email": "operaciones.local@example.com", "name": "Andrés Ruiz"},
    "specialty": "vehicle_assessment",
    "location": {"source": "app", "latitude": 4.6771, "longitude": -74.0483},
    "from_radius_km": 5,
    "outcome": "found",
    "radius_km": 5,
    "reason": null,
    "policy_version": "assignment-policy-v1",
    "searched_at": "2026-11-08T14:38:30.553159Z",
    "attempts": [
      {"radius_km": 5, "eligible_count": 1, "attempted_at": "2026-11-08T14:38:30.553159Z"}
    ],
    "candidates": [
      {
        "provider_id": "prv-peritos-autos-andinos",
        "name": "Peritos Autos Andinos",
        "specialties": ["vehicle_assessment", "bodywork"],
        "distance_km": 2.8,
        "availability": "available",
        "unavailable_until": null,
        "eligibility": "eligible",
        "suggested": true
      },
      {
        "provider_id": "prv-peritaje-express-sur",
        "name": "Peritaje Express Sur",
        "specialties": ["vehicle_assessment"],
        "distance_km": 13.1,
        "availability": "available",
        "unavailable_until": null,
        "eligibility": "out_of_radius",
        "suggested": false
      }
    ]
  },
  "assignment": null
}
```

- `location.source: app`: de la evidencia del teléfono marcada como confiable (precisión de 50 m o mejor), la de menor
  `accuracy_m` («Capturadas desde la app · precisión 8 m»). En la corrida, una foto con 3 m pero marcada no confiable no ganó.
  `recorded_at` es el instante de esa captura.
- **Solo el móvil trae coordenadas.** Un caso radicado por el portal (el daño por agua del mockup, por ejemplo) llega siempre
  con `state: location_missing` y `location: null`, y el analista registra la ubicación (§2.3).
- `candidates[]`: los prestadores **con la especialidad**, del más cercano al más lejano (máximo 20), elegibles o no.
  `distance_km` es en línea recta, redondeada a 0,1 km.
- El radio del mapa es `last_search.radius_km` («Radio de búsqueda 5 km»).

### 2.3 `POST /v1/claims/{claim_id}/incident-location` — dirección alternativa

Solo cuando el caso **no** tiene coordenadas de la app (`state: location_missing`), o para corregir la que registró el
analista mientras no haya asignación. El front convierte la dirección en el pin del mapa; el servicio no geocodifica.

```http
POST /v1/claims/6fdbce81-e4ee-4693-953d-7a3ebb0c01aa/incident-location
Content-Type: application/json

{"address": "Carrera 15 # 93-40, apto 402, Bogotá", "latitude": 4.6771, "longitude": -74.0483}
```

`201` con la asignación del caso (§2.2), ahora `state: ready` y:

```json
"location": {
  "source": "analyst",
  "latitude": 4.6771,
  "longitude": -74.0483,
  "accuracy_m": null,
  "address": "Carrera 15 # 93-40, apto 402, Bogotá",
  "recorded_by": {"email": "operaciones.local@example.com", "name": "Andrés Ruiz"},
  "recorded_at": "2026-11-08T14:38:30.898445Z"
}
```

| Campo | Regla |
|---|---|
| `address` | Obligatoria, 5 a 200 caracteres. El front puede precargar el «lugar» que el asegurado escribió al radicar |
| `latitude` | Obligatoria, −90 a 90 |
| `longitude` | Obligatoria, −180 a 180 |

Errores propios: `409 location_from_app` (el caso tiene coordenadas de la app), `409 assignment_exists`,
`409 claim_not_assignable`.

### 2.4 `POST /v1/claims/{claim_id}/provider-searches` — buscar prestador

| Botón | Cuerpo |
|---|---|
| «Buscar prestador» / «Reintentar búsqueda» | Sin cuerpo (empieza en el radio inicial, 5 km) |
| «Ampliar radio de búsqueda» | `{"from_radius_km": 10}` (el siguiente de `radii_km`; en 20 km no aplica) |

La búsqueda recorre los radios desde el pedido y **se detiene en el primero con elegibles**. Responde `201` con la asignación del
caso (§2.2): `state: proposed` si encontró, `state: pending_assignment` si no.

Daño por agua con los dos plomeros de 5 km no disponibles: la búsqueda se amplió sola a 10 km (CA-4). Servicios Hogar Norte
vuelve mañana, así que no es elegible hoy:

```json
"state": "proposed",
"last_search": {
  "seq": 2,
  "from_radius_km": 5,
  "outcome": "found",
  "radius_km": 10,
  "attempts": [
    {"radius_km": 5, "eligible_count": 0, "attempted_at": "2026-11-08T14:38:30.977737Z"},
    {"radius_km": 10, "eligible_count": 1, "attempted_at": "2026-11-08T14:38:30.977737Z"}
  ],
  "candidates": [
    {"provider_id": "prv-servicios-hogar-norte", "name": "Servicios Hogar Norte", "specialties": ["plumbing", "damp_proofing"],
     "distance_km": 2.3, "availability": "unavailable", "unavailable_until": "2026-11-09", "eligibility": "unavailable",
     "suggested": false},
    {"provider_id": "prv-reparaciones-chapinero", "name": "Reparaciones Chapinero", "specialties": ["plumbing"],
     "distance_km": 3.8, "availability": "unavailable", "unavailable_until": null, "eligibility": "unavailable",
     "suggested": false},
    {"provider_id": "prv-tecnicos-unidos", "name": "Técnicos Unidos", "specialties": ["plumbing", "damp_proofing", "painting"],
     "distance_km": 6.1, "availability": "available", "unavailable_until": null, "eligibility": "eligible", "suggested": true}
  ]
}
```

(Se omiten los campos que no cambian respecto de §2.2.)

Sin elegibles hasta el radio máximo (pantalla «Prestador no encontrado · Pendiente de asignación»), otro daño por agua cuando
Técnicos Unidos ya quedó asignado:

```json
"state": "pending_assignment",
"last_search": {
  "seq": 1,
  "trigger": "analyst",
  "searched_by": {"email": "operaciones.local@example.com", "name": "Andrés Ruiz"},
  "specialty": "plumbing",
  "location": {"source": "analyst", "latitude": 4.6575, "longitude": -74.056},
  "from_radius_km": 5,
  "outcome": "exhausted",
  "radius_km": 20,
  "reason": "no_eligible_provider_within_max_radius",
  "policy_version": "assignment-policy-v1",
  "searched_at": "2026-11-08T14:38:37.638301Z",
  "attempts": [
    {"radius_km": 5, "eligible_count": 0, "attempted_at": "2026-11-08T14:38:37.638301Z"},
    {"radius_km": 10, "eligible_count": 0, "attempted_at": "2026-11-08T14:38:37.638301Z"},
    {"radius_km": 20, "eligible_count": 0, "attempted_at": "2026-11-08T14:38:37.638301Z"}
  ],
  "candidates": [
    {"provider_id": "prv-reparaciones-chapinero", "name": "Reparaciones Chapinero", "specialties": ["plumbing"],
     "distance_km": 1.5, "availability": "unavailable", "unavailable_until": null, "eligibility": "unavailable",
     "suggested": false},
    {"provider_id": "prv-servicios-hogar-norte", "name": "Servicios Hogar Norte", "specialties": ["plumbing", "damp_proofing"],
     "distance_km": 4.6, "availability": "unavailable", "unavailable_until": "2026-11-09", "eligibility": "unavailable",
     "suggested": false},
    {"provider_id": "prv-tecnicos-unidos", "name": "Técnicos Unidos", "specialties": ["plumbing", "damp_proofing", "painting"],
     "distance_km": 7.8, "availability": "busy", "unavailable_until": null, "eligibility": "unavailable", "suggested": false}
  ]
}
```

- `availability: busy` → «No disponible · en otro caso»; `unavailable` con fecha → «No disponible · hasta 09/11»; sin fecha,
  «No disponible».
- El **registro de intentos** de la pantalla sale de `attempts[]` de cada búsqueda.
- **Reintento automático:** mientras el caso esté `pending_assignment`, el servicio repite la búsqueda cada
  `retry_interval_seconds` (300 s en AWS). Si encuentra un elegible, el caso pasa a `proposed` y el analista confirma; **nunca
  asigna solo**. El front puede volver a pedir §2.2 al abrir la pantalla. Así llegó el caso anterior cuando Servicios Hogar Norte
  volvió a `available`:

```json
"state": "proposed",
"last_search": {
  "seq": 2,
  "trigger": "automatic_retry",
  "searched_by": null,
  "outcome": "found",
  "radius_km": 5,
  "attempts": [
    {"radius_km": 5, "eligible_count": 1, "attempted_at": "2026-11-08T14:38:39.057896Z"}
  ]
}
```

Errores propios: `422 location_missing`, `409 assignment_exists`, `409 claim_not_assignable`, `422 validation_failed`
(`from_radius_km` que no es un radio de la política).

### 2.5 `POST /v1/claims/{claim_id}/assignment` — confirmar el prestador

```http
POST /v1/claims/6fdbce81-e4ee-4693-953d-7a3ebb0c01aa/assignment
Content-Type: application/json

{"provider_id": "prv-tecnicos-unidos", "search_seq": 2}
```

`search_seq` es el `last_search.seq` que el analista tiene en pantalla. Se puede elegir el sugerido u **otro `eligible`**.
`201` con la asignación del caso, `state: assigned` y:

```json
"assignment": {
  "provider": {
    "id": "prv-tecnicos-unidos",
    "name": "Técnicos Unidos",
    "contact_email": "operaciones@tecnicosunidos.example.com",
    "contact_phone": null
  },
  "assigned_by": {"email": "operaciones.local@example.com", "name": "Andrés Ruiz"},
  "assigned_at": "2026-11-08T14:38:31.026146Z",
  "distance_km": 6.1,
  "search_seq": 2,
  "location_shared": {
    "source": "analyst",
    "latitude": 4.6771,
    "longitude": -74.0483,
    "accuracy_m": null,
    "address": "Carrera 15 # 93-40, apto 402, Bogotá"
  },
  "policy_version": "assignment-policy-v1",
  "notice": {"status": "pending", "attempts": 0, "sent_at": null}
}
```

`claim_status` sigue `in_evaluation`. El aviso sale unos milisegundos después; al pedir §2.2 de nuevo llega
`"notice": {"status": "sent", "attempts": 1, "sent_at": "…"}`.

- El prestador queda `busy` (ocupado en este caso) y deja de ser elegible para otros.
- `notice` es el correo al prestador (§3): `pending` mientras sale y `sent` cuando el proveedor de correo lo aceptó. Si el correo
  falla, la asignación **no** se pierde: el servicio lo reintenta.
- Una asignación por caso; reasignar o cancelar no está en esta historia.

| Error | Cuándo | Qué hace el front |
|---|---|---|
| `409 provider_unavailable` | El prestador dejó de estar disponible después de la búsqueda | «El prestador ya no está disponible» y buscar de nuevo |
| `409 search_outdated` | `search_seq` no es la última búsqueda, o la ubicación cambió después | Volver a pedir §2.2 |
| `409 assignment_exists` | Otro analista ya asignó el caso | Volver a pedir §2.2 |
| `409 claim_not_assignable` | El caso salió de evaluación o su tipo no requiere visita | Volver a pedir §2.2 |
| `422 provider_not_eligible` | El prestador no está entre los `eligible` de esa búsqueda | No debería pasar si el botón solo se habilita en `eligible` |

### 2.6 `GET /v1/claims/assignments?claim_id=…` y `GET /v1/claims/assignment-policy`

Lote de 1 a 50 ids (repetibles, sin repetidos), en el orden pedido:

```json
{
  "items": [
    {"claim_id": "6fdbce81-e4ee-4693-953d-7a3ebb0c01aa", "state": "assigned", "specialty": "plumbing",
     "provider_name": "Técnicos Unidos", "distance_km": 6.1, "assigned_at": "2026-11-08T14:38:31.026146Z", "reason": null},
    {"claim_id": "a5c0cb62-fb8a-43af-bc49-0525102d2c7c", "state": "proposed", "specialty": "plumbing",
     "provider_name": null, "distance_km": null, "assigned_at": null, "reason": null},
    {"claim_id": "00000000-0000-4000-8000-000000000000", "state": "not_found", "specialty": null,
     "provider_name": null, "distance_km": null, "assigned_at": null, "reason": null}
  ]
}
```

Un caso `pending_assignment` trae `"reason": "no_eligible_provider_within_max_radius"`.

`400 invalid_request` con 0 ids, más de 50, repetidos o un id que no es de caso.

Política vigente:

```json
{
  "version": "assignment-policy-v1",
  "radii_km": [5, 10, 20],
  "initial_radius_km": 5,
  "max_radius_km": 20,
  "specialties": [
    {"line": "auto", "incident_type": "collision", "specialty": "vehicle_assessment"},
    {"line": "auto", "incident_type": "theft", "specialty": null},
    {"line": "auto", "incident_type": "third_party_damage", "specialty": "vehicle_assessment"},
    {"line": "auto", "incident_type": "glass", "specialty": "auto_glass"},
    {"line": "auto", "incident_type": "other", "specialty": "vehicle_assessment"},
    {"line": "home", "incident_type": "water_damage", "specialty": "plumbing"}
  ],
  "retry_interval_seconds": 10
}
```

`retry_interval_seconds` es 10 en la captura porque la e2e lo acorta (`ASSIGNMENT_RETRY_INTERVAL_SECONDS`); en AWS es 300.

### 2.7 Administración de prestadores (`/v1/claims/providers`)

| Ruta | Qué hace |
|---|---|
| `GET /v1/claims/providers?specialty=plumbing&availability=available` | Listado por nombre (máx. 200); filtros opcionales |
| `POST /v1/claims/providers` | Alta (`201`); queda `available`. El servicio asigna el `id` |
| `GET /v1/claims/providers/{provider_id}` | Un prestador (`404 provider_not_found`) |
| `POST /v1/claims/providers/{provider_id}/profile` | Reemplaza nombre, especialidades, ubicación y contacto |
| `POST /v1/claims/providers/{provider_id}/availability` | `available` o `unavailable` (con `unavailable_until` opcional) |

Cuerpo del alta y del perfil:

```json
{
  "name": "Servicios Hogar Norte",
  "specialties": ["plumbing", "damp_proofing"],
  "latitude": 4.694,
  "longitude": -74.0364,
  "address": "Calle 116 # 9-72, Bogotá",
  "contact_email": "despachos@hogarnorte.example.com",
  "contact_phone": "+576015550102"
}
```

| Campo | Regla |
|---|---|
| `name` | 3 a 120 caracteres |
| `specialties` | 1 a 6, sin repetidos: `vehicle_assessment` (peritaje vehicular), `auto_glass` (vidrios), `plumbing` (plomería y humedades), `damp_proofing` (humedades), `painting` (pintura), `bodywork` (latonería) |
| `latitude`, `longitude` | −90 a 90 y −180 a 180 |
| `address` | 5 a 200 caracteres |
| `contact_email` | Correo válido: es el canal por el que recibe las asignaciones |
| `contact_phone` | Opcional, formato E.164 (`+57…`); informativo |

Disponibilidad:

```json
{"availability": "unavailable", "unavailable_until": "2026-09-22"}
```

- `busy` no se escribe por aquí: lo pone la asignación. Poner `available` a un prestador `busy` lo **libera** (mientras la
  historia de seguimiento y cierre no lo haga sola).
- `unavailable_until` es informativa (la muestra la tabla de candidatos); el prestador no vuelve a `available` solo.

Respuesta (`Provider`), un prestador dado de alta y puesto `unavailable` hasta mañana:

```json
{
  "id": "prv-bce1d39b6372",
  "name": "Pinturas del Norte",
  "specialties": ["painting", "damp_proofing"],
  "latitude": 4.7021,
  "longitude": -74.0412,
  "address": "Calle 127 # 15-20, Bogotá",
  "contact_email": "citas@pinturasdelnorte.example.com",
  "contact_phone": "+576015550199",
  "availability": "unavailable",
  "unavailable_until": "2026-11-09",
  "busy_claim_id": null,
  "updated_by": "operaciones.local@example.com",
  "updated_at": "2026-11-08T14:38:39.888186Z"
}
```

Uno de la semilla después de asignarlo:

```json
{
  "id": "prv-tecnicos-unidos",
  "name": "Técnicos Unidos",
  "specialties": ["plumbing", "damp_proofing", "painting"],
  "latitude": 4.7246,
  "longitude": -74.0758,
  "address": "Avenida Suba # 115-58, Bogotá",
  "contact_email": "operaciones@tecnicosunidos.example.com",
  "contact_phone": null,
  "availability": "busy",
  "unavailable_until": null,
  "busy_claim_id": "6fdbce81-e4ee-4693-953d-7a3ebb0c01aa",
  "updated_by": "operaciones.local@example.com",
  "updated_at": "2026-11-08T14:38:31.026146Z"
}
```

`updated_by: reference-data` (con `updated_at: 2026-11-07T00:00:00Z`) marca a los prestadores de la carga inicial que nadie
ha tocado. La asignación también cuenta como cambio: guarda al analista que asignó y la hora.

### 2.8 Errores

```
401  {"error": "unauthorized", "message": "a bearer token is required"}
403  {"error": "forbidden", "message": "the caller is not allowed to perform this operation"}          ← asegurado o asesor
404  {"error": "claim_not_found", "message": "there is no claim with that id"}
404  {"error": "provider_not_found", "message": "there is no provider with that id"}
409  {"error": "location_from_app", "message": "the claim has trusted coordinates from the app"}
409  {"error": "assignment_exists", "message": "the claim already has its provider"}
409  {"error": "claim_not_assignable", "message": "the claim is not in evaluation or needs no on-site visit"}
409  {"error": "provider_unavailable", "message": "the provider is no longer available"}
409  {"error": "search_outdated", "message": "the search is not the last one of the claim"}
422  {"error": "location_missing", "message": "the claim has no location to search from"}
422  {"error": "provider_not_eligible", "message": "the provider is not eligible in that search"}
422  {"error": "validation_failed", "message": "one or more fields are missing or invalid", "fields": [{"field": "address", "code": "out_of_range"}, {"field": "latitude", "code": "out_of_range"}]}
400  {"error": "invalid_request", "message": "every claim_id must be a claim id, without repeats"}
```

Todos son respuestas capturadas. El `422` es una ubicación con dirección de 4 caracteres y latitud 91. Un alta de prestador con
`{"name": "", "specialties": ["towing"], "latitude": 91}` devuelve los seis campos a la vez: `name` y `address` `required`,
`specialties` `invalid_format`, `latitude` `out_of_range`, `longitude` y `contact_email` `required`.

---

## 3. Aviso al prestador (correo)

Al confirmar la asignación el prestador recibe, en su `contact_email`, un correo con lo necesario para desplazarse. Los dos que
recibió el stub de correo en la corrida, uno con la ubicación del analista y otro con la de la app:

```
Asunto: Asignación de siniestro SIN-2026-00002

Solventa le asignó la atención en sitio del siniestro SIN-2026-00002.

Tipo: Hogar · Daño por agua
Ubicación: 4.677100, -74.048300
Dirección: Carrera 15 # 93-40, apto 402, Bogotá
Mapa: https://www.google.com/maps/search/?api=1&query=4.677100,-74.048300
```

```
Asunto: Asignación de siniestro SIN-2026-00001

Solventa le asignó la atención en sitio del siniestro SIN-2026-00001.

Tipo: Autos · Colisión con daños al vehículo
Ubicación: 4.677100, -74.048300 (precisión 8 m)
Dirección: (no registrada)
Mapa: https://www.google.com/maps/search/?api=1&query=4.677100,-74.048300
```

- La precisión aparece solo con la ubicación de la app. La dirección es la que registró el analista; con la ubicación de la app
  dice «(no registrada)».
- No lleva nombre, documento ni teléfono del asegurado.
- Se envía una sola vez por caso (llave `asignacion-<claim_id>` ante el proveedor de correo), con reintentos si falla.

---

## 4. Cómo se elige el prestador (`assignment-policy-v1`, provisional)

| Línea · tipo | Especialidad requerida |
|---|---|
| Autos · colisión, daño a terceros, otro | `vehicle_assessment` |
| Autos · cristales | `auto_glass` |
| Autos · hurto | **Sin visita** (`not_required`) |
| Hogar · daño por agua | `plumbing` |

1. **Elegible** = disponible **hoy** (`available`) + tiene la especialidad + está dentro del radio del intento.
2. **Distancia** en línea recta entre la ubicación del incidente y la registrada del prestador.
3. **Radios:** 5 → 10 → 20 km (máximo). La búsqueda se detiene en el primer radio con elegibles.
4. **Sugerido:** el elegible más cercano; en empate, el de `provider_id` menor.
5. **Sin elegibles en 20 km:** pendiente de asignación con el motivo `no_eligible_provider_within_max_radius`; se busca otra vez
   cada 5 minutos.

**Pendiente de validación con negocio** (especialidades, radios y prestadores de la carga inicial). Si cambia, cambia `version`.

---

## 5. Tratamiento de datos

- Cada búsqueda, cada intento, cada ubicación registrada por el analista y cada asignación quedan en un historial que no se
  modifica ni se borra, con quién y cuándo.
- La asignación guarda una copia del prestador y de la ubicación tal como se compartieron.
- El correo al prestador no lleva datos personales del asegurado.

---

## 6. Lo que queda para otras historias

- **Seguimiento de la intervención, cierre, reasignación y cancelación; liberar al prestador al cerrar:** HU-W2.3-02.
- **Bandeja y despacho de asistencias, especialidades de grúa y asistencia vial y llegada estimada:** HU-W2.3-03 (usa el mismo
  cálculo de distancia, disponibilidad y especialidad).
- **Fecha de la visita en la línea de tiempo del asegurado:** HU-W2.3-02.
- **Geocodificar la dirección:** la hace el front al poner el pin.
- **Distancia por vía y calificación o número de casos del prestador:** fuera de esta historia.
