# Contrato de API — HU-W2.1-01 · Radicación de siniestro con soportes (portal web)

Servicio: **siniestros** (`claims`). Consumidor: portal web del asegurado.

> **Fuente única para el front.** Generado a partir del código real del servicio `claims`
> (`internal/adapters/http/web_filings.go`, `internal/filing/{web,supports,queries}.go`, `internal/domain/{catalog,web_filing,policy}.go`,
> `migrations/0003_web_filing.sql`) y de respuestas **capturadas del binario en ejecución** el 2026-10-25 (`claims` en Docker con su
> PostgreSQL, el stub del core de pólizas detrás de Toxiproxy y Cognito local de Moto con verificación JWT RS256 real de
> `pkg/security`). Los identificadores y las fechas de los ejemplos son los de esa ejecución. Contrato formal:
> `services/claims/api/openapi.yaml` (versión 0.3.0).

---

## 1. Resumen del flujo

```
Portal web (sesión de Cognito, rol insured)        claims                                  core de pólizas
──────────────────────────────────────────         ─────────────────────────────────────   ───────────────
GET  /v1/claims/filing-options ──────────────────▶ pólizas del asegurado ───────────────▶ GET /holders/{sub}/policies
  ◀── pólizas activas, tipos cubiertos y soportes de cada tipo          (si no responde: 503)

POST /v1/claims/supports (un archivo por llamada) ▶ valida el contenido (JPG/PNG/PDF ≤ 10 MiB) y lo guarda
  ◀── 201 {support_id}  ·  413 / 415: solo ese archivo se rechaza

POST /v1/claims/web-filings ─────────────────────▶ 1. formulario y soportes del tipo (todo lo que falta, a la vez)
  (Idempotency-Key)                                 2. póliza propia, activa, que cubre el tipo y con el hecho en vigencia
  ◀── 201 caso en estado active con su radicado      3. caso + soportes asociados en una sola transacción

GET  /v1/claims/{claim_id} ──────────────────────▶ confirmación
GET  /v1/claims/{claim_id}/filing-receipt ───────▶ PDF del radicado
GET  /v1/claims ─────────────────────────────────▶ Mis siniestros (también los radicados desde el móvil)
```

| Criterio | Cómo |
|---|---|
| CA-1 Solo pólizas activas propias; soportes por tipo, con los obligatorios | `GET /v1/claims/filing-options` |
| CA-2 Falta un soporte o un dato → no se envía y se dice qué falta | `422 validation_failed` con todos los campos (§2.3) |
| CA-3 Hecho fuera de vigencia o tipo no cubierto → no se radica, con el motivo | `422 policy_not_eligible` con `reason` (§2.3) |
| CA-4 Caso `active`, radicado único, fecha y hora, soportes; confirmación descargable | `201` de la radicación, `GET /v1/claims/{id}` y el PDF |
| CA-5 Archivo de tipo o tamaño no permitido → se rechaza con el motivo, sin perder lo diligenciado | `415` / `413` en la subida de ese archivo |
| CA-6 Solo sobre sus pólizas; el caso aparece en su listado con su estado | La póliza se busca entre las del asegurado; `GET /v1/claims` |

---

## 2. Endpoints

### Cabeceras comunes

- `Authorization: Bearer <ID token de Cognito>`. Rol **`insured`**: con otro rol la respuesta es `403`; sin token, `401`.
- Todas las respuestas llevan `Cache-Control: no-store`.
- Error: `{"error": "<código estable>", "message": "<texto en inglés, no mostrar>"}`. El front muestra su propio texto según `error`.

### 2.1 `GET /v1/claims/filing-options` — opciones del formulario

Llamarla al abrir «Radicar un siniestro». Trae **solo** las pólizas activas hoy cuyo ramo se radica (Autos y Hogar; Viaje es
paramétrico y no aparece) y, por póliza, los tipos que cubre y los soportes de cada tipo.

```json
{
  "catalog_version": "claim-catalog-v1",
  "policies": [
    {
      "policy_id": "AU-2026-403432",
      "line": "auto",
      "product": "Autos",
      "insured_object": "IMS-432",
      "valid_from": "2026-08-24T00:00:00Z",
      "valid_to": "2027-08-24T00:00:00Z",
      "incident_types": [
        {
          "code": "collision",
          "label": "Colisión con daños al vehículo",
          "requirements": [
            {
              "code": "vehicle_photos",
              "label": "Fotos del vehículo",
              "required": true,
              "min_files": 4,
              "max_files": 10,
              "formats": [
                "jpeg",
                "png"
              ]
            },
            {
              "code": "driver_document",
              "label": "Documento del conductor",
              "required": true,
              "min_files": 1,
              "max_files": 2,
              "formats": [
                "pdf",
                "jpeg",
                "png"
              ]
            },
            {
              "code": "traffic_report",
              "label": "Informe de tránsito o croquis",
              "required": true,
              "min_files": 1,
              "max_files": 3,
              "formats": [
                "pdf",
                "jpeg",
                "png"
              ]
            }
          ]
        }
      ]
    }
  ]
}
```

(Recortado: en la respuesta real la póliza de Autos trae también `theft`, `third_party_damage` y `other`, y hay una segunda
póliza `HG-2026-486204` de Hogar con `water_damage`.)

- `policies` vacío: el asegurado no tiene pólizas activas que se radiquen. Mostrar el estado vacío, no un error.
- Un soporte que **no aparece** en `requirements` no aplica a ese tipo (en el mockup, «No aplica»).
- `valid_to` es el primer instante **sin** cobertura. Sirve para el aviso «Fecha del hecho dentro de la vigencia» antes de enviar.
- `503 policy_directory_unavailable`: el core de pólizas no respondió. Ofrecer reintentar.

### 2.2 `POST /v1/claims/supports` — subir un soporte

`multipart/form-data` con **un solo** campo `file`. Se sube un archivo por llamada, en cuanto el asegurado lo elige.

| Regla | Valor |
|---|---|
| Formatos | JPG, PNG o PDF. Se decide por el **contenido** del archivo, no por la extensión |
| Tamaño | De 1 byte a 10 MiB (10 485 760 bytes) por archivo |
| Pendientes | Como máximo 50 soportes subidos y aún no asociados a un caso, por asegurado |

Respuesta `201`:

```json
{
  "support_id": "sup-e9a4688ac28cb56a875bb3dbb5a4e84a",
  "file_name": "croquis.pdf",
  "content_type": "application/pdf",
  "byte_size": 4105,
  "sha256": "3ebdbded93068e8b1d3ce03106c0503e911fa0b6961e088e48f3a574b047d767",
  "uploaded_at": "2026-10-25T01:43:02.260708Z"
}
```

| Código | `error` | Qué mostrar |
|---|---|---|
| `400` | `invalid_request` | El cuerpo no es multipart o no trae exactamente un campo `file` |
| `413` | `file_too_large` | «El archivo supera 10 MB» |
| `415` | `unsupported_file_type` | «Solo se admiten JPG, PNG o PDF» |
| `422` | `empty_file` | «El archivo está vacío» |
| `429` | `too_many_pending_supports` | «Tienes demasiados archivos sin radicar» |
| `503` | `supports_unavailable` | Reintentar |

Un rechazo afecta **solo** a ese archivo: los ya subidos siguen valiendo y el formulario no se pierde (CA-5). «Reemplazar» es
subir otro archivo y enviar su `support_id` en lugar del anterior.

### 2.3 `POST /v1/claims/web-filings` — radicar

| | |
|---|---|
| Idempotencia | `Idempotency-Key` obligatoria (1–128, `[A-Za-z0-9._:-]`). El portal genera un UUID por formulario y lo reutiliza en los reintentos |

```json
{
  "policy_id": "AU-2026-403432",
  "incident_type": "collision",
  "occurred_at": "2026-10-23T01:43:02Z",
  "place": "Calle 80 con carrera 30, Bogotá",
  "description": "Al detenerme en el semáforo, un vehículo particular golpeó la parte trasera.",
  "supports": [
    {
      "requirement_code": "vehicle_photos",
      "support_ids": [
        "sup-0909ce681d629ae34133601c4ca5676e",
        "sup-54c3a54287ef5ca3a6a9a054eabd2e4f",
        "sup-345ce8f41c5e6bb1306c8780ec51677d",
        "sup-f9d63e97a0cf808c40a85096435daedc"
      ]
    },
    {
      "requirement_code": "driver_document",
      "support_ids": [
        "sup-3aaebb756804c135339e435450a448ec"
      ]
    },
    {
      "requirement_code": "traffic_report",
      "support_ids": [
        "sup-1281e360579f871ea627df63cbd2bce9"
      ]
    }
  ]
}
```

| Campo | Reglas |
|---|---|
| `policy_id` | 3 a 40 caracteres alfanuméricos y guiones. Debe ser una póliza del asegurado (§2.1) |
| `incident_type` | Un `code` de los `incident_types` de esa póliza |
| `occurred_at` | RFC 3339. No futuro, no anterior a cinco años y dentro de la vigencia de la póliza |
| `place` | 3 a 160 caracteres |
| `description` | 10 a 1000 caracteres |
| `supports` | Los soportes subidos, agrupados por `requirement_code`. Cada `support_id` una sola vez; como máximo 20 archivos en total |

Las validaciones van en este orden y el orden importa para el front:

1. **Formulario y soportes** → `422 validation_failed` con **todos** los problemas a la vez:

   ```json
   {
     "error": "validation_failed",
     "message": "one or more fields are missing or invalid",
     "fields": [
       {
         "field": "supports.traffic_report",
         "code": "missing"
       }
     ]
   }
   ```

   | `code` | Significado |
   |---|---|
   | `required`, `invalid_format`, `out_of_range` | Los mismos de HU-M2.2-01 para los datos del hecho |
   | `missing` | Soporte obligatorio sin archivos («Falta 1 soporte obligatorio») |
   | `too_few` / `too_many` | Menos del mínimo o más del máximo de archivos de ese soporte (o más de 20 en total: `supports`) |
   | `invalid_format` en `supports.<code>` | Un archivo de un formato que ese soporte no admite (p. ej. un PDF en las fotos) |
   | `not_applicable` | Un soporte que ese tipo de siniestro no usa |
   | `not_found` | Un `support_id` que no existe, que es de otro asegurado, que está repetido o que ya se usó en otro caso |

2. **Póliza** (solo si el formulario está bien) → `422 policy_not_eligible`:

   ```json
   {
     "error": "policy_not_eligible",
     "message": "the incident happened outside the validity of the policy",
     "reason": "occurred_outside_validity",
     "valid_from": "2026-08-24T00:00:00Z",
     "valid_to": "2027-08-24T00:00:00Z"
   }
   ```

   | `reason` | Qué mostrar |
   |---|---|
   | `policy_not_found` | La póliza no es del asegurado o no existe (no se distingue a propósito). `valid_from`/`valid_to` en `null` |
   | `policy_not_active` | La póliza ya no está activa |
   | `incident_not_covered` | La póliza no cubre ese tipo de siniestro |
   | `occurred_outside_validity` | La fecha del hecho está fuera de la vigencia (`valid_from` ≤ hecho < `valid_to`) |

3. **Creación** → `201` con el caso (§2.4). Repetir con la misma clave y el mismo cuerpo responde `200` con el mismo caso (aunque
   el core de pólizas esté caído en ese momento: el reintento no lo consulta); la misma clave con otro cuerpo,
   `422 idempotency_key_reused`. Las claves se comparten con la radicación móvil: una clave usada en el móvil no sirve en el web.

| Otros códigos | `error` | Condición |
|---|---|---|
| `400` | `invalid_request` | El cuerpo no es JSON válido o falta `Idempotency-Key` |
| `503` | `policy_directory_unavailable` | El core de pólizas no respondió. **No se creó nada**; reintentar con la misma clave |
| `503` | `filing_unavailable` | La radicación no se completó; reintentar con la misma clave |

### 2.4 `GET /v1/claims/{claim_id}` — detalle (confirmación)

También es el cuerpo del `201`/`200` de la radicación. Solo lo lee quien radicó el caso: otro asegurado recibe `403`; un id
desconocido, `404 claim_not_found`.

```json
{
  "claim_id": "4a54d386-4827-4f0b-9aa2-385288fb4dd3",
  "filing_number": "SIN-2026-00001",
  "status": "active",
  "channel": "web",
  "policy": {
    "policy_id": "AU-2026-403432",
    "line": "auto",
    "product": "Autos",
    "insured_object": "IMS-432"
  },
  "incident_type": "collision",
  "occurred_at": "2026-10-23T01:43:02Z",
  "filed_at": "2026-10-25T01:43:03.08918Z",
  "place": "Calle 80 con carrera 30, Bogotá",
  "description": "Al detenerme en el semáforo, un vehículo particular golpeó la parte trasera.",
  "supports": {
    "required_total": 3,
    "required_attached": 3,
    "files": 6,
    "items": [
      {
        "requirement_code": "vehicle_photos",
        "label": "Fotos del vehículo",
        "files": [
          {
            "support_id": "sup-0909ce681d629ae34133601c4ca5676e",
            "file_name": "photo.jpg",
            "content_type": "image/jpeg",
            "byte_size": 4100
          },
          {
            "support_id": "sup-54c3a54287ef5ca3a6a9a054eabd2e4f",
            "file_name": "photo.jpg",
            "content_type": "image/jpeg",
            "byte_size": 4100
          },
          {
            "support_id": "sup-345ce8f41c5e6bb1306c8780ec51677d",
            "file_name": "photo.jpg",
            "content_type": "image/jpeg",
            "byte_size": 4100
          },
          {
            "support_id": "sup-f9d63e97a0cf808c40a85096435daedc",
            "file_name": "photo.jpg",
            "content_type": "image/jpeg",
            "byte_size": 4100
          }
        ]
      },
      {
        "requirement_code": "driver_document",
        "label": "Documento del conductor",
        "files": [
          {
            "support_id": "sup-3aaebb756804c135339e435450a448ec",
            "file_name": "doc.pdf",
            "content_type": "application/pdf",
            "byte_size": 4105
          }
        ]
      },
      {
        "requirement_code": "traffic_report",
        "label": "Informe de tránsito o croquis",
        "files": [
          {
            "support_id": "sup-1281e360579f871ea627df63cbd2bce9",
            "file_name": "doc.pdf",
            "content_type": "application/pdf",
            "byte_size": 4105
          }
        ]
      }
    ]
  }
}
```

- `status: active` es el estado «Radicado» de la pantalla (CA-4 lo llama *Activo*).
- `filed_at` es la hora del servidor; mostrarla en la zona del usuario.
- `supports` da el «3 de 3 obligatorios · 8 archivos» de la confirmación.
- Caso radicado desde el móvil: `channel: mobile`, `policy.line`/`product`/`insured_object` en `null` y `supports` en ceros.

### 2.5 `GET /v1/claims/{claim_id}/filing-receipt` — descargar el radicado

`200 application/pdf`. Cabeceras capturadas:

```
HTTP/1.1 200 OK
Cache-Control: no-store
Content-Disposition: attachment; filename="radicado-SIN-2026-00001.pdf"
Content-Length: 5985
Content-Type: application/pdf
```

Descargarlo dos veces da el mismo archivo (las fechas internas del PDF son las de la radicación). Contiene el número de
radicado, el estado, la fecha y hora de radicación (hora de Colombia), la póliza, el tipo y la fecha del hecho, el lugar, los
soportes adjuntos y los pasos siguientes. Mismos `403`/`404` que el detalle.

### 2.6 `GET /v1/claims` — Mis siniestros

Casos del asegurado de **los dos canales**, del más reciente al más antiguo.

| Parámetro | Reglas |
|---|---|
| `status` | Opcional. Hoy solo `active` |
| `policy_id` | Opcional |
| `from` | Opcional, `YYYY-MM-DD`: radicados desde ese día a las 00:00 (hora de Colombia) |
| `page_size` | 1 a 50; 20 por defecto |
| `page_token` | El `next_page_token` de la página anterior, sin cambios. Al cambiar un filtro, empezar sin él |

```json
{
  "items": [
    {
      "claim_id": "4a54d386-4827-4f0b-9aa2-385288fb4dd3",
      "filing_number": "SIN-2026-00001",
      "status": "active",
      "channel": "web",
      "policy": {
        "policy_id": "AU-2026-403432",
        "line": "auto",
        "product": "Autos",
        "insured_object": "IMS-432"
      },
      "incident_type": "collision",
      "occurred_at": "2026-10-23T01:43:02Z",
      "filed_at": "2026-10-25T01:43:03.08918Z"
    }
  ],
  "next_page_token": null
}
```

`next_page_token: null` es la última página. Un filtro o un token inválido responde `400 invalid_request`.

---

## 3. Catálogo de tipos y soportes (`claim-catalog-v1`)

El front **no** debe fijar esta tabla en su código: viene en `GET /v1/claims/filing-options`. Se incluye para referencia.
`image` = JPG o PNG; `document` = PDF, JPG o PNG.

| Ramo | Tipo | Soportes (obligatorio, archivos, formato) |
|---|---|---|
| Autos | `collision` Colisión con daños al vehículo | Fotos del vehículo (sí, 4–10, image) · Documento del conductor (sí, 1–2, document) · Informe de tránsito o croquis (sí, 1–3, document) |
| Autos | `theft` Hurto | Denuncia ante autoridad (sí, 1–3, document) · Documento del conductor (sí, 1–2, document) · Fotos del vehículo (opcional, hasta 10, image) |
| Autos | `third_party_damage` Daño a terceros | Fotos del vehículo (sí, 4–10, image) · Documento del conductor (sí, 1–2, document) · Informe de tránsito o croquis (sí, 1–3, document) |
| Autos | `glass` Cristales | Fotos de los cristales (sí, 2–6, image) · Documento del conductor (opcional, hasta 2, document) |
| Autos | `other` Otro | Documento de soporte (sí, 1–5, document) |
| Hogar | `water_damage` Daño por agua | Fotos del daño (sí, 2–10, image) · Factura o cotización de la reparación (sí, 1–3, document) |

El catálogo está pendiente de validación con negocio. Si cambia, cambia `catalog_version`.

---

## 4. Tratamiento de datos

- El caso queda a nombre del `sub` del token. La póliza se busca entre las del asegurado en el core de pólizas: el cuerpo nunca
  dice de quién es.
- El caso guarda una copia de la póliza tal como estaba al radicar (ramo, producto, objeto asegurado, vigencia y tipos cubiertos)
  y la versión del catálogo, para que la validación se pueda reconstruir aunque la póliza cambie después.
- Un soporte se asocia a **un solo** caso, y los soportes de un caso radicado no se modifican ni se borran.
- Las respuestas nunca incluyen el contenido de los archivos.

---

## 5. Provisional hasta HU-M2.1-01

Esta historia avanza sin esperar la evidencia multimedia de HU-M2.1-01. Cuando esa historia se integre, cambian:

- **La subida de soportes:** pasará a una URL firmada de S3 (el archivo irá directo del navegador al almacenamiento), como la
  evidencia del móvil. `POST /v1/claims/supports` cambiará de forma: se avisará con una versión nueva del contrato.
- **Posible unificación** de `POST /v1/claims/web-filings` con `POST /v1/claims` (con un campo `channel`). Por decidir con el
  equipo.
- **En AWS, mientras tanto,** el API Gateway limita cada petición a 10 MB, así que un archivo de 10 MiB justos no pasa. En local
  el límite es el del contrato.
