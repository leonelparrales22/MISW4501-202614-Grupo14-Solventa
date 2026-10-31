# Contrato de API — HU-M2.2-01 · Radicación del siniestro

Servicio: **siniestros** (`claims`). Consumidor: cliente móvil.

---

## 1. `POST /v1/claims`

Crea el caso del siniestro con su número de radicado. Es la última etapa del asistente guiado: se llama una sola vez,
al confirmar la declaración.

| | |
|---|---|
| Autenticación | `Authorization: Bearer <ID token de Cognito>` |
| Rol | `insured`. El caso queda a nombre del `sub` del token |
| Idempotencia | `Idempotency-Key` obligatoria. El móvil envía el identificador del borrador |
| Caché | `Cache-Control: no-store` |

### Petición

```json
{
  "policy_id": "AU-2025-118204",
  "incident_type": "collision",
  "occurred_at": "2026-10-20T23:30:00Z",
  "place": "Calle 80 con Carrera 15, Bogotá",
  "description": "Choque por alcance en el semáforo. El otro vehículo se retiró del lugar.",
  "details": {
    "third_party": "yes",
    "police_report": "no"
  },
  "evidence": [
    {
      "object_key": "evidence/9f4ce9cd-85c7-4603-b8e9-0def7c3e1ddd/9f2c4a1e8b0d6f35.jpg",
      "kind": "photo",
      "captured_at": "2026-10-20T23:32:14Z",
      "latitude": 4.7712,
      "longitude": -74.0452,
      "accuracy_m": 8,
      "location_trusted": true,
      "sha256": "9f2c4a1e8b0d6f35c7a9e1b4d8f0a2c6e4b8d1f3a7c9e5b2d4f6a8c0e2b4d6f8",
      "byte_size": 312044
    }
  ],
  "declaration_accepted": true
}
```

| Campo | Tipo | Reglas |
|---|---|---|
| `policy_id` | string | 3 a 40 caracteres alfanuméricos, guiones incluidos |
| `incident_type` | string | `collision`, `theft`, `third_party_damage` u `other` |
| `occurred_at` | RFC 3339 | Instante del hecho. No puede ser futuro ni anterior a cinco años |
| `place` | string | 3 a 160 caracteres |
| `description` | string | 10 a 1000 caracteres |
| `details` | objeto | Opcional. Las respuestas de las preguntas que dependen del tipo de incidente: hasta 20 claves de 1 a 40 caracteres, con valores de hasta 200 |
| `evidence` | arreglo | Entre 4 y 6 fotografías y hasta un video, ya subidos al repositorio de evidencia. Cada uno con su llave, su captura, su ubicación y su resumen de integridad: [contrato de HU-M2.1-01](HU-M2.1-01-api.md), sección 3 |
| `declaration_accepted` | booleano | `true`. La declaración bajo gravedad de juramento es la que cierra la radicación |
| `estimated_amount_minor` | entero | Opcional (paso 2, M11b). Lo que el asegurado estima del daño, en centavos de COP, con las reglas de la radicación web (HU-W2.2-01): de 1 a 10¹³; un decimal o un texto es `invalid_format` y fuera del rango `out_of_range`. Con él el triage calcula la banda de monto y operaciones precarga el costo de la evaluación (HU-W2.2-03). Cuenta en la huella de la radicación solo si viene: las radicaciones sin monto conservan la suya |

### Respuesta `201`

```json
{
  "claim_id": "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90",
  "filing_number": "SIN-2026-01877",
  "status": "active",
  "policy_id": "AU-2025-118204",
  "incident_type": "collision",
  "occurred_at": "2026-10-20T23:30:00Z",
  "filed_at": "2026-10-23T03:10:00Z",
  "evidence_count": { "photos": 4, "videos": 1 }
}
```

| Campo | Tipo | Descripción |
|---|---|---|
| `claim_id` | string | Identificador del caso en la plataforma |
| `filing_number` | string | Número de radicado: `SIN-<año>-<cinco dígitos>`. Es el que se le muestra al asegurado |
| `status` | string | `active` al crearse |
| `filed_at` | RFC 3339, UTC | Instante de la radicación |
| `evidence_count` | objeto | Lo que quedó asociado al caso: `photos` y `videos` |
| `estimated_amount_minor` | entero | El monto declarado, en centavos de COP. No viene si no se declaró |

---

## 2. Errores

| Código | `error` | Condición |
|---|---|---|
| `400` | `invalid_request` | El cuerpo no es JSON válido o falta `Idempotency-Key` |
| `401` | `unauthorized` | Sin token, o el token no es válido |
| `403` | `forbidden` | El token no pertenece al rol `insured` |
| `422` | `validation_failed` | Uno o más campos incumplen sus reglas |
| `422` | `idempotency_key_reused` | La misma clave con un cuerpo distinto |
| `503` | `filing_unavailable` | La radicación no se pudo completar. Reintentable |

`422 validation_failed` lista todos los campos inválidos a la vez:

```json
{
  "error": "validation_failed",
  "message": "one or more fields are missing or invalid",
  "fields": [
    { "field": "occurred_at", "code": "out_of_range" },
    { "field": "description", "code": "out_of_range" },
    { "field": "declaration_accepted", "code": "required" }
  ]
}
```

Códigos de campo: `required`, `invalid_format`, `out_of_range` y `not_stored`, que es la llave de una evidencia que no está en el repositorio o no pertenece a quien radica.

---

## 3. Reintento

La clave de idempotencia identifica la radicación. Repetir la llamada con la misma clave y el mismo cuerpo devuelve
**`200`** con el caso que ya existe, con su mismo `claim_id`, su mismo `filing_number` y su `filed_at` original.

Esa misma clave con un cuerpo distinto responde `422 idempotency_key_reused` y no modifica el caso.

Un `503` o una respuesta que no llega se reintentan con la misma clave: el caso se crea una sola vez.

---

## 4. Tratamiento de datos

El caso queda asociado al `sub` del token, no al correo ni al documento. El lugar y la descripción los escribe el
asegurado y se guardan tal como llegan, con los espacios de los extremos recortados.

El número de radicado es único en la plataforma y no se reutiliza.

Las evidencias no viajan en este cuerpo: ya están en el repositorio de evidencia cuando se radica, y aquí solo van
sus llaves y sus metadatos.
