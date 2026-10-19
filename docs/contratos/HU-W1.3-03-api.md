# Contrato de API — HU-W1.3-03 · Documento contractual y notificación de emisión

> **Fuente para el front (Angular).** Describe lo que implementa el servicio `origination`
> (`internal/adapters/http/contract_documents.go`, `internal/issuance/contract*.go`, `internal/adapters/{pdf,filestore,links,email}`).
> Contrato formal: `services/origination/api/openapi.yaml`. Es el contrato que alimenta la pantalla **E5 «Póliza emitida»**
> (`/embebido/poliza-emitida`): documento disponible y notificación enviada.

## 1. Flujo

```
cobertura activada ──▶ origination (en segundo plano, sin intervención manual, ≤ 60 s)
                       ├─ genera el PDF contractual (condiciones, coberturas, prima, vigencia)
                       ├─ lo guarda en el repositorio documental y lo asocia a la póliza        ← document_ready
                       ├─ envía por correo al asegurado un enlace seguro (vence)                ← completed
                       └─ registra cada paso: fecha y hora, versión del documento, resultado de la entrega

Pantalla E5 ──GET /v1/policies/{policy_id}/issuance-status (polling cada 2 s)──▶ estado, documento y notificación
«Descargar» ──GET /v1/policies/{policy_id}/contract-document──────────────────▶ PDF
Enlace del correo ──GET /v1/documents/{token} (sin sesión)────────────────────▶ PDF
```

La activación la publica el cobro de la prima de HA-D02 **solo cuando el pago queda confirmado** (ver
[`HA-D02-api.md`](HA-D02-api.md)); el `policy_id` sigue siendo **`pol-` + el `acceptance_id`** de la aceptación. Mientras el cobro está
pendiente o en excepción no hay activación y estas rutas responden `404 policy_not_found`.

## 2. Endpoints

Todas las respuestas llevan `Cache-Control: no-store`. Las dos primeras exigen el ID token de Cognito y el rol `insured` (solo el dueño de
la póliza), `advisor` u `operations`; `403` si la póliza es de otro asegurado, `401` sin token válido.

### `GET /v1/policies/{policy_id}/issuance-status` → `200`

```json
{
  "policy_id": "pol-3f1c…", "status": "completed",
  "activated_at": "2026-10-16T15:00:00Z", "available_at": "2026-10-16T15:00:01Z", "within_slo": true,
  "document": {
    "document_id": "b1e2…", "version": 1, "content_type": "application/pdf", "size_bytes": 2310,
    "checksum_sha256": "9f2c…", "generated_at": "2026-10-16T15:00:01Z"
  },
  "notification": { "channel": "email", "status": "sent", "attempts": 0, "sent_at": "2026-10-16T15:00:01Z", "last_failure_at": null },
  "audit": [
    { "event": "document_generated", "at": "2026-10-16T15:00:01Z", "document_version": 1, "detail": "2310 bytes, available 1.0 s after the activation" },
    { "event": "notification_sent", "at": "2026-10-16T15:00:01Z", "document_version": 1, "delivery_result": "sent", "channel": "email", "attempt": 1 }
  ]
}
```

| `status` | Qué pinta E5 |
|---|---|
| `pending` | «Estamos generando tu documento» (`document`, `available_at` y `within_slo` son `null`) |
| `document_ready` | Documento disponible para descargar; la notificación sigue pendiente o reintentándose (`notification.status: pending`, `attempts` > 0 si falló) |
| `completed` | Documento disponible **y** notificación enviada a `notification.channel` |
| `failed` | El correo no pudo entregarse tras todos los intentos; el documento sigue disponible para descargar |

- `attempts` cuenta los intentos fallidos; los reintentos esperan 1 s, 2 s, 4 s… hasta 15 s.
- `audit` es el registro de la emisión (solo se agrega): `document_generated`, `notification_failed` (con `attempt` y `detail`) y
  `notification_sent`. Mientras no hay activación la ruta responde `404 policy_not_found`.

### `GET /v1/policies/{policy_id}/contract-document` → `200`

El PDF (`application/pdf`) con `Content-Disposition: attachment; filename="poliza-{policy_id}-v{version}.pdf"` y la cabecera
`X-Document-Checksum-SHA256`. `404 document_not_available` mientras no esté listo (reintentar); `404 policy_not_found` si la póliza no tiene emisión.

### `GET /v1/documents/{token}` → `200`

El mismo PDF, **sin sesión**: el token firmado y con vencimiento (7 días por defecto) del correo es la autorización. `403 invalid_link` si el
enlace fue alterado o venció. No usar el interceptor de autenticación en esta ruta.

## 3. Correo de emisión

Asunto «Tu póliza de Solventa fue emitida»; el cuerpo trae el nombre, el número de póliza, el enlace seguro y su fecha de vencimiento. Se envía
al correo registrado del asegurado. El envío es idempotente (`policy-issued:{policy_id}:v{version}`): un reintento nunca duplica el mensaje.

## 4. Lo que todavía es provisional

- La activación de la cobertura la publica el cobro de HA-D02 tras confirmarse el pago. Una oferta personalizada (HU-W1.2-03) activa su
  propio monto asegurado, plazo y coberturas; las de demostración (`demo-…`) y las ajustadas (`adj-…`) siguen con los de demostración
  (fase AWS de HA-D02, D-16). El número de póliza definitivo es de HU-W1.3-02.
- La emisión y el registro de auditoría viven en memoria: pendientes PostgreSQL y el servicio `audit`. El documento se guarda en **S3** en
  AWS (`DOCUMENTS_BUCKET`, el bucket `documents` de la plataforma) y en un directorio local (`DOCUMENTS_DIR`) en desarrollo; en AWS el
  enlace del correo abre `GET /v1/documents/{token}` por una ruta pública del API Gateway, sin sesión (fase AWS de HA-D02).
- El nombre del evento (`PolicyIssued`) está por confirmar con el equipo; no hay frame de Figma dedicado para este estado de E5.
