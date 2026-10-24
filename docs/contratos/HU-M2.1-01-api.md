# Contrato de API — HU-M2.1-01 · Evidencia del siniestro

Servicio: **siniestros** (`claims`). Consumidor: cliente móvil.

La evidencia se sube en dos tiempos: el servicio emite una URL firmada y el teléfono escribe el archivo
directamente en el repositorio de evidencia. La radicación de [HU-M2.2-01](HU-M2.2-01-api.md) viaja después con
la lista de lo subido.

---

## 1. `POST /v1/claims/evidence/upload-url`

Emite la URL con la que se sube una evidencia.

| | |
|---|---|
| Autenticación | `Authorization: Bearer <ID token de Cognito>` |
| Rol | `insured`. La evidencia queda bajo el `sub` del token |
| Caché | `Cache-Control: no-store` |

### Petición

```json
{
  "kind": "photo",
  "content_type": "image/jpeg",
  "byte_size": 312044,
  "sha256": "9f2c4a1e8b0d6f35c7a9e1b4d8f0a2c6e4b8d1f3a7c9e5b2d4f6a8c0e2b4d6f8"
}
```

| Campo | Tipo | Reglas |
|---|---|---|
| `kind` | string | `photo` o `video` |
| `content_type` | string | `image/jpeg` para `photo`, `video/mp4` para `video` |
| `byte_size` | entero | 1 KiB a 2 MB en una foto; 1 KiB a 60 MB en un video |
| `sha256` | string | 64 caracteres hexadecimales en minúscula: el resumen del archivo que se va a subir |

### Respuesta `201`

```json
{
  "object_key": "evidence/9f4ce9cd-85c7-4603-b8e9-0def7c3e1ddd/9f2c4a1e8b0d6f35.jpg",
  "upload_url": "https://solventa-production-demo-evidence-368083581678.s3.us-east-2.amazonaws.com/evidence/...",
  "expires_at": "2026-10-24T21:52:10Z"
}
```

| Campo | Tipo | Descripción |
|---|---|---|
| `object_key` | string | Llave del objeto en el repositorio de evidencia. Es la que después viaja con la radicación |
| `upload_url` | string | URL firmada de subida. Vence a los **10 minutos** |
| `expires_at` | RFC 3339, UTC | Instante en que la URL deja de servir |

La llave se deriva del `sub` del token y del resumen del archivo. Pedir la URL dos veces para el mismo archivo
devuelve la misma llave.

---

## 2. `PUT <upload_url>`

Sube el archivo al repositorio de evidencia. La petición va **directamente al almacenamiento**, no al API Gateway.

| | |
|---|---|
| Autenticación | Ninguna. La firma viaja en la URL. **No se envía `Authorization`** |
| Encabezados | `Content-Type` y `Content-Length` exactamente iguales a los declarados al pedir la URL |
| Cuerpo | El archivo, en binario |

| Código | Condición |
|---|---|
| `200` | El archivo quedó guardado |
| `403` | La URL venció, la firma no corresponde, o el `Content-Type` o el tamaño no son los declarados |

Una URL vencida se reemplaza pidiendo otra para el mismo archivo: la llave es la misma y el objeto no se duplica.

---

## 3. La evidencia en `POST /v1/claims`

La radicación de HU-M2.2-01 lleva un campo más:

```json
{
  "policy_id": "AU-2025-118204",
  "incident_type": "collision",
  "evidence": [
    {
      "object_key": "evidence/9f4ce9cd-85c7-4603-b8e9-0def7c3e1ddd/9f2c4a1e8b0d6f35.jpg",
      "kind": "photo",
      "captured_at": "2026-10-20T23:32:14Z",
      "latitude": 4.7712,
      "longitude": -74.0452,
      "altitude_m": 2601,
      "accuracy_m": 8,
      "location_trusted": true,
      "sha256": "9f2c4a1e8b0d6f35c7a9e1b4d8f0a2c6e4b8d1f3a7c9e5b2d4f6a8c0e2b4d6f8",
      "byte_size": 312044
    }
  ]
}
```

| Campo | Tipo | Reglas |
|---|---|---|
| `object_key` | string | La llave que devolvió la emisión de la URL. Tiene que existir en el repositorio y pertenecer al `sub` del token |
| `kind` | string | `photo` o `video` |
| `captured_at` | RFC 3339, UTC | Instante de la captura. No puede ser futuro ni anterior al hecho |
| `latitude` | número | −90 a 90 |
| `longitude` | número | −180 a 180 |
| `altitude_m` | número | Opcional. Metros sobre el nivel del mar |
| `accuracy_m` | número | Precisión de la ubicación, en metros |
| `location_trusted` | booleano | `false` cuando la precisión supera los **50 m** o no hubo lectura reciente |
| `sha256` | string | Resumen del archivo. Tiene que coincidir con el del objeto guardado |
| `byte_size` | entero | Tamaño del archivo. Tiene que coincidir con el del objeto guardado |
| `duration_s` | entero | Solo en `video`. Hasta 30 |

**Mínimo y tope.** Entre **4 y 6 fotografías**, y hasta **un video**. El video no cuenta para el mínimo.

El servicio comprueba cada llave contra el repositorio antes de crear el caso: el objeto existe, es del `sub` del
token, y su tamaño y su resumen son los declarados. Una llave que no cumple responde `422` y el caso no se crea.

---

## 4. Errores

| Código | `error` | Condición |
|---|---|---|
| `400` | `invalid_request` | El cuerpo no es JSON válido |
| `401` | `unauthorized` | Sin token, o el token no es válido |
| `403` | `forbidden` | El token no pertenece al rol `insured` |
| `422` | `validation_failed` | Uno o más campos incumplen sus reglas |
| `503` | `evidence_unavailable` | La URL no se pudo emitir. Reintentable |

`422 validation_failed` lista todos los campos inválidos a la vez:

```json
{
  "error": "validation_failed",
  "message": "one or more fields are missing or invalid",
  "fields": [
    { "field": "evidence", "code": "out_of_range" },
    { "field": "evidence.0.object_key", "code": "not_stored" }
  ]
}
```

Códigos de campo: `required`, `invalid_format`, `out_of_range` y **`not_stored`**, que es la llave que no está en el
repositorio o no pertenece a quien radica.

---

## 5. Tratamiento de datos

La evidencia vive en el *bucket* de evidencia del ambiente: privado, cifrado, con versiones y accesible solo por
TLS. El archivo nunca se guarda en la base de datos; lo que queda con el caso es su llave, sus coordenadas, su
instante de captura, su precisión y su resumen de integridad.

El resumen permite comprobar después que el archivo es el mismo que se capturó. La ubicación no confiable se
conserva con la evidencia marcada como tal, y no se descarta.
