# Contrato de API — HU-M2.3-01 · Solicitud de asistencia (GPS + llamada)

Servicio: **siniestros** (`claims`). Consumidor: cliente móvil (pantallas M14, M14b y M14c).
Especificación: [`services/claims/api/openapi.yaml`](../../services/claims/api/openapi.yaml), versión 0.16.0.
Los ejemplos de respuesta son capturas del servicio real en Docker
([`tests/e2e/hu-m2.3`](../../tests/e2e/hu-m2.3/README.md) con `CAPTURE_DIR`); las pólizas son las del stub del core.

La solicitud de asistencia **no es un siniestro**: tiene su propio número (`AST-…`), no aparece en `GET /v1/claims` y no
pasa por la revisión ni por el triage. La línea de atención **vive en la app**: este servicio no la conoce ni la devuelve.

---

## 1. `POST /v1/claims/assistance-requests`

Registra la solicitud con la póliza, la fecha y hora UTC y el resultado de la ubicación: las coordenadas que el teléfono
pudo compartir o el motivo por el que no pudo.

| | |
|---|---|
| Autenticación | `Authorization: Bearer <ID token de Cognito>` |
| Rol | `insured`. La solicitud queda a nombre del `sub` del token; el cuerpo no dice de quién es |
| Idempotencia | `Idempotency-Key` obligatoria (`^[A-Za-z0-9._:-]{1,128}$`). Una llave nueva por cada toque de «Solicitar asistencia» |
| Caché | `Cache-Control: no-store` |

### Petición con ubicación (M14)

```json
{
  "policy_id": "AU-2026-357374",
  "location": {
    "status": "sent",
    "latitude": 4.7712,
    "longitude": -74.0452,
    "accuracy_m": 8,
    "captured_at": "2026-11-15T02:38:51Z"
  },
  "place": "Autopista Norte km 12, Bogotá"
}
```

### Petición sin ubicación (M14b, M14c)

```json
{
  "policy_id": "AU-2026-357374",
  "location": {
    "status": "unavailable",
    "unavailable_reason": "permission_denied"
  }
}
```

| Campo | Tipo | Reglas |
|---|---|---|
| `policy_id` | string | Obligatorio. 3 a 40 caracteres alfanuméricos, guiones incluidos. Una póliza **propia y activa** del asegurado, de cualquier línea (Autos, Hogar, Viaje) |
| `location.status` | string | Obligatorio. `sent` si el teléfono compartió un *fix*; `unavailable` si no pudo |
| `location.latitude` | número | Con `sent`: obligatorio, de −90 a 90. Con `unavailable`: prohibido |
| `location.longitude` | número | Con `sent`: obligatorio, de −180 a 180. Con `unavailable`: prohibido |
| `location.accuracy_m` | número | Con `sent`: obligatorio, mayor que 0 y hasta 10 000 (metros). Con `unavailable`: prohibido |
| `location.captured_at` | RFC 3339 | Con `sent`: obligatorio. Instante del *fix* en el teléfono; no puede pasar de 5 minutos en el futuro (desfase del reloj). Con `unavailable`: prohibido |
| `location.unavailable_reason` | string | Con `unavailable`: obligatorio, `permission_denied`, `location_disabled` o `no_fix`. Con `sent`: prohibido |
| `place` | string | Opcional. 3 a 160 caracteres. La dirección legible que el teléfono obtiene de su geocodificador; el servicio no geocodifica. Se guarda como texto, sin los espacios de los extremos |

`latitude: 0, longitude: 0` es una ubicación válida, no «ausente». Un campo que no corresponde al `status` es
`not_applicable`. Un campo desconocido (por ejemplo `sub`), un tipo incorrecto (coordenadas en texto), un número fuera del
rango de `double` (`1e400`) o un cuerpo de más de 16 KB son `400 invalid_request`.

### Respuesta `201` con ubicación

```json
{
  "request_id": "5004f477-d43b-4b8a-80b0-b045207c5d11",
  "request_number": "AST-2026-0001",
  "status": "new",
  "policy": {
    "policy_id": "AU-2026-357374",
    "line": "auto",
    "product": "Autos",
    "insured_object": "CUH-374"
  },
  "location": {
    "status": "sent",
    "latitude": 4.7712,
    "longitude": -74.0452,
    "accuracy_m": 8,
    "captured_at": "2026-11-15T02:38:51Z",
    "unavailable_reason": null
  },
  "place": "Autopista Norte km 12, Bogotá",
  "requested_at": "2026-11-15T02:38:57.043927Z"
}
```

### Respuesta `201` sin ubicación

```json
{
  "request_id": "acb75553-41bd-4a8c-a968-aad3275b6dcc",
  "request_number": "AST-2026-0002",
  "status": "new",
  "policy": {
    "policy_id": "AU-2026-357374",
    "line": "auto",
    "product": "Autos",
    "insured_object": "CUH-374"
  },
  "location": {
    "status": "unavailable",
    "latitude": null,
    "longitude": null,
    "accuracy_m": null,
    "captured_at": null,
    "unavailable_reason": "permission_denied"
  },
  "place": null,
  "requested_at": "2026-11-15T02:38:57.378249Z"
}
```

| Campo | Tipo | Descripción |
|---|---|---|
| `request_id` | string | UUID v4 de la solicitud. Con él se lee (`GET`) |
| `request_number` | string | `AST-<año>-<4 dígitos o más>`. El que se le muestra al asegurado. Único; puede dejar huecos |
| `status` | string | `new` al registrarse. `in_call`, `dispatched` y `closed` son de la bandeja de operaciones (HU-W2.3-03); la app debe aceptarlos aunque esta historia no los escriba |
| `policy` | objeto | La póliza tal como la describió el core al registrar: `policy_id`, `line` (`auto`, `home`, `travel`), `product`, `insured_object` |
| `location` | objeto | El resultado de la ubicación tal como se registró. Los campos que no aplican al `status` van en `null` |
| `place` | string o `null` | La dirección que mandó la app |
| `requested_at` | RFC 3339, UTC | Cuándo el **servidor** registró la solicitud, con fracción de segundo (hasta microsegundos). La app la muestra en hora local y en UTC |

---

## 2. `GET /v1/claims/assistance-requests/{request_id}`

Devuelve la solicitud tal como se registró, con su estado actual. Solo la lee su dueño (rol `insured`). Respuesta `200`
con el mismo cuerpo de la sección 1. `Cache-Control: no-store`.

Una solicitud de otro asegurado, una que no existe o un `request_id` que no es un UUID v4 en minúsculas responden igual:
`404 assistance_request_not_found`. No hay lista de solicitudes.

---

## 3. Errores

| Código | `error` | Condición | ¿Se registró algo? |
|---|---|---|---|
| `400` | `invalid_request` | Falta `Idempotency-Key` o no cumple el patrón; el cuerpo no es un objeto JSON, trae un campo desconocido o de otro tipo, o pasa de 16 KB | No |
| `401` | `unauthorized` | Sin token, o el token no es válido | No |
| `403` | `forbidden` | El token no es del rol `insured` | No |
| `404` | `assistance_request_not_found` | (`GET`) No existe, no es del asegurado o el id no es un UUID v4 | — |
| `422` | `validation_failed` | Uno o más campos incumplen sus reglas (todos a la vez, en `fields`) | No |
| `422` | `policy_not_eligible` | La póliza no es del asegurado (`reason: policy_not_found`) o no está vigente hoy (`reason: policy_not_active`) | No |
| `422` | `idempotency_key_reused` | La misma llave con otro contenido | No |
| `503` | `policy_directory_unavailable` | El core de pólizas no respondió. Reintentable con la misma llave | **No** |
| `503` | `assistance_unavailable` | La solicitud no se pudo guardar o leer. Reintentable con la misma llave | No |

`422 validation_failed` lista todos los campos inválidos; los de la ubicación se nombran `location.<campo>`:

```json
{
  "error": "validation_failed",
  "message": "one or more fields are missing or invalid",
  "fields": [
    { "field": "location.latitude", "code": "not_applicable" }
  ]
}
```

(`unavailable` con `latitude`.)

Códigos de campo: `required`, `invalid_format`, `out_of_range` y `not_applicable` (un campo que no va con ese `status`).

`422 policy_not_eligible` tiene la forma de la radicación web ([HU-W2.1-01](HU-W2.1-01-api.md)):

```json
{
  "error": "policy_not_eligible",
  "message": "the policy is not active",
  "reason": "policy_not_active",
  "valid_from": "2025-10-16T00:00:00Z",
  "valid_to": "2026-10-16T00:00:00Z"
}
```

Una póliza de otro asegurado responde igual que una que no existe, sin vigencia:

```json
{
  "error": "policy_not_eligible",
  "message": "the policy is not one of the insured's",
  "reason": "policy_not_found",
  "valid_from": null,
  "valid_to": null
}
```

Los demás errores llevan solo `error` y `message`:

```json
{ "error": "policy_directory_unavailable", "message": "the policies could not be read; nothing was recorded, retry with the same key" }
{ "error": "idempotency_key_reused", "message": "that key was used for another request for assistance" }
{ "error": "assistance_request_not_found", "message": "there is no such request for assistance" }
```

---

## 4. Reintento

La llave identifica la solicitud. Repetir la llamada con la misma llave y el mismo cuerpo devuelve **`200`** con la
solicitud que ya existe: mismo `request_id`, mismo `request_number` y su `requested_at` original. La misma llave con otro
cuerpo responde `422 idempotency_key_reused` y no cambia nada.

Un `503` o una respuesta que no llega se reintentan con la **misma** llave: la solicitud se crea una sola vez. La misma
llave de otro asegurado es otra solicitud.

---

## 5. Lo que hace la app (no son rutas)

- **El marcador abre siempre** (CA-3, CA-4): con `201`/`200`, con cualquier error y sin red. El `POST` y el marcador no se
  esperan entre sí; la llamada nunca depende de este servicio.
- **Una llave por toque** de «Solicitar asistencia». El reintento de red reutiliza la llave. «Volver a llamar» **no** hace
  `POST`: solo reabre el marcador.
- **Desenlace de la ubicación → cuerpo:**

  | Lo que pasó en el teléfono | `location` |
  |---|---|
  | Hay un *fix* de 60 s o menos | `status: sent` con coordenadas, `accuracy_m` y `captured_at` del *fix* |
  | El asegurado rechazó el permiso | `status: unavailable`, `unavailable_reason: permission_denied` |
  | El servicio de ubicación está apagado | `unavailable`, `location_disabled` |
  | No llegó un *fix* en el tiempo de espera, o el último tiene más de 60 s | `unavailable`, `no_fix` |
  | «Llamar sin ubicación» (M14b) | `unavailable` con el motivo que corresponda de los tres anteriores |

  El servicio **no** rechaza un *fix* viejo: es la app la que manda `no_fix` en vez de una ubicación de hace minutos.
- **Con `503` o sin red:** el banner dice que la ubicación no se pudo compartir (CA-4) y la app puede reintentar en segundo
  plano con la misma llave.
- **Mapa mockup → campo:** «Ubicación enviada · 16:31:08» = `requested_at` (en hora local); «precisión 8 m» = `accuracy_m`;
  «Autos · AU-2025-118204 · KJT-482» = `policy` (`product`, `policy_id`, `insured_object`); «Autopista Norte km 12» =
  `place`; «Ver estado de la solicitud» = el `GET` de la sección 2.

---

## 6. Tratamiento de datos

- La solicitud queda asociada al `sub` del token. Con ella se guardan la copia de la póliza y el nombre del token (para la
  bandeja de operaciones); el teléfono del asegurado **no** se guarda.
- **Lo enviado no se reescribe:** póliza, ubicación, motivo, `place` y fecha quedan protegidos por la base de datos. Solo el
  `status` cambia, y lo cambia la bandeja de operaciones (HU-W2.3-03).
- La ubicación es un dato sensible: los registros del servicio no llevan coordenadas, `place`, nombre ni `sub`.

---

## 7. Lo que queda para otras historias

- **Bandeja, atención, despacho, cierre con motivo y los estados `in_call`, `dispatched` y `closed`:** HU-W2.3-03, que lee
  las solicitudes de la misma base (sin eventos).
- **Sugerencia del prestador más cercano** y **ubicación descrita por voz** cuando no hubo GPS: HU-W2.3-03.
- **Aviso de llegada estimada** (*push*) y **lista de solicitudes** del asegurado: no están en los criterios de esta historia.
- **Teléfono del asegurado** en la solicitud: el token no lo trae.
- **Cobertura de asistencia por producto:** el core de pólizas no la informa; se confirma en la llamada.
