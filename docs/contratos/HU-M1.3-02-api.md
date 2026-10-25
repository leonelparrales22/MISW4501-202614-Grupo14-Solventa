# Contrato de API — HU-M1.3-02 · Revocación efectiva del consentimiento en movilidad

Servicio: **auditoría** (`audit`). Consumidor: cliente móvil (*Perfil → Privacidad y consentimientos → detalle*).

El asegurado revoca un consentimiento activo, después de confirmar con su biometría o su contraseña de Solventa. `audit` deja el
registro inmutable **antes** de responder (CA-4), desde ese instante deja de verificar el consentimiento como activo, así que
originación no vuelve a consultar las fuentes (CA-3), y avisa a cada entidad dentro de 5 minutos (RN-02). El comprobante se descarga en
PDF. Las rutas de consulta de HU-M1.3-01 ([contrato](HU-M1.3-01-api.md)) no cambian de forma: cada consentimiento gana `revocable` y
`revocation`.

---

## 1. Cómo funciona

```
app ──POST /v1/consents/{id}/revocations (Idempotency-Key, requested_at, device, confirmation_method)──▶ audit
       una transacción, antes de responder: consent_revocation (inmutable, REV-AAAA-NNNN)
                                            + consent_event revocation_requested y revoked
                                            + un aviso por entidad con su ventana de 5 min
   ◀── 201 Revocation (status revoked, entidades pending)          la app muestra «Revocado»
origination ──GET /v1/audit/consent-verifications──▶ active=false desde effective_at ⇒ 422 consent_required, sin consultar fuentes
trabajador de audit ──POST /consent-revocations (Idempotency-Key revocation-<consentimiento>-<entidad>)──▶ Open Finance
       confirmada ⇒ revocation_confirmed · vence la ventana ⇒ revocation_not_confirmed ⇒ partially_revoked
```

- **«Revocado» desde que se registra** (decisión D-1 a): Solventa deja de consultar en ese instante. El aviso a las entidades se sigue
  aparte en `entities` y `notice_status`; si alguna no confirma dentro de su ventana, el estado pasa a `partially_revoked`.
- **Fechas:** `requested_at` es la hora en que el asegurado confirmó en el teléfono (se conserva aunque la app la envíe horas después
  desde su cola sin conexión, CA-5); `effective_at` es la hora del servidor, cuando terminó la autorización.
- **Minimización:** al ecosistema solo viaja el consentimiento, la entidad, el cliente del aliado y la hora efectiva; nunca el documento
  ni el nombre. Las respuestas tampoco los repiten.

---

## 2. `POST /v1/consents/{consent_id}/revocations`

| | |
|---|---|
| Autenticación | Token de identidad (ID token) de Cognito: `Authorization: Bearer <token>` |
| Autorización | Rol `insured`. El titular sale del `sub` del token; un consentimiento ajeno responde como inexistente |
| Idempotencia | Cabecera `Idempotency-Key` obligatoria: un UUID que la app genera **al confirmar** y conserva en su cola. La misma clave devuelve la misma revocación con `200` |
| Caché | `Cache-Control: no-store` |

### Cuerpo

```json
{
  "requested_at": "2026-10-24T14:59:58-05:00",
  "device": "Google Pixel 8",
  "confirmation_method": "biometric"
}
```

| Campo | Regla |
|---|---|
| `requested_at` | Fecha y hora RFC 3339 de la confirmación en el teléfono. Obligatoria; como máximo 2 minutos en el futuro (desfase del reloj) y nunca anterior al otorgamiento |
| `device` | Descripción del dispositivo, 1 a 120 caracteres |
| `confirmation_method` | `biometric` (huella o rostro) o `password` (contraseña de Solventa verificada con Cognito, D-6 a) |

### Respuesta `201` (o `200` con la misma clave)

```json
{
  "consent_id": "cns-4f1c2a9be0d34c6f8a7e5d1b2c3a4f5e",
  "receipt_number": "REV-2026-0088",
  "status": "revoked",
  "requested_at": "2026-10-24T19:59:58Z",
  "effective_at": "2026-10-24T20:00:00.123456Z",
  "channel": "mobile_app",
  "device": "Google Pixel 8",
  "notice_status": "in_progress",
  "entities": [
    { "code": "aliado-banco-1", "name": "Banco Aliado", "status": "pending", "confirmed_at": null, "deadline_at": "2026-10-24T20:05:00.123456Z" }
  ]
}
```

| Campo | Descripción |
|---|---|
| `receipt_number` | `REV-<año en Colombia>-<consecutivo de 4 o más dígitos>` (D-5 a) |
| `status` | `revoked` o `partially_revoked` |
| `notice_status` | `in_progress` (alguna entidad aún tiene tiempo), `completed` (todas confirmaron) o `partial` (alguna no confirmó) |
| `entities[].status` | `pending`, `confirmed` o `not_confirmed`; `deadline_at` es el fin de su ventana |

La pantalla de revocado muestra «Efectivo en X segundos» con `effective_at − requested_at` (si sale negativo por el reloj del teléfono,
«Efectivo desde <hora>»).

---

## 3. `GET /v1/consents/{consent_id}/revocation-receipt`

El comprobante en PDF (CA-4), generado a pedido desde el registro inmutable: la misma revocación da siempre el mismo documento. Trae el
número, el consentimiento, la entidad, el titular (documento enmascarado salvo los 4 últimos), el alcance revocado, la solicitud y la
**fecha y hora exactas de finalización** (con segundos y desfase, en Colombia y en UTC), el canal y el dispositivo.

- `200 application/pdf` con `Content-Disposition: attachment; filename="REV-2026-0088.pdf"`. Español por defecto; inglés con
  `Accept-Language: en…`.
- `404` si el consentimiento no está revocado, no existe o es de otro titular.

La app lo guarda con el selector del sistema (`CreateDocument`), sin permisos de almacenamiento.

---

## 4. Cambios en `GET /v1/consents*` (HU-M1.3-01)

| Campo nuevo | Descripción |
|---|---|
| `revocable` | `true` solo para un consentimiento `active`: la app muestra «Revocar consentimiento» solo entonces (CA-6) |
| `revocation` | El mismo objeto de la sección 2 cuando el consentimiento fue revocado; `null` si no |
| `status` | `revoked` o `partially_revoked` según la sección 1; `revocation_pending` sigue reservado y el servidor no lo usa («pendiente de envío» es un estado de la cola de la app) |
| `history` | Agrega `revocation_requested` y `revoked` (del consentimiento) y, con `entity_code`, `revocation_confirmed`, `revocation_not_confirmed` y `revocation_retry_requested` |

---

## 5. Errores

| Código | `error` | Condición |
|---|---|---|
| `400` | `validation_failed` | Campo inválido (`requested_at`, `device`, `confirmation_method`) o `Idempotency-Key` ausente o que no es un UUID; `fields` dice cuál y por qué |
| `400` | `invalid_request` | El cuerpo no es JSON |
| `401` | `unauthorized` | Sin token, token inválido o vencido |
| `403` | `forbidden` / `holder_not_linked` | El rol no es `insured` / la cuenta todavía no está vinculada a un documento |
| `404` | `not_found` | El consentimiento no existe o es de otro titular |
| `409` | `consent_not_active` | El consentimiento venció (también si venció mientras la revocación esperaba en la cola de la app) |
| `409` | `consent_already_revoked` | Otra solicitud ya lo revocó (CA-6) |
| `409` | `idempotency_key_reused` | La clave ya revocó otro consentimiento |
| `503` | `service_unavailable` | No se pudo registrar; nada cambió. Reintentable con la **misma** clave |

---

## 6. Lo que queda para otras historias

- Entrada desde el portal de cumplimiento (motivo, referencia, rol `operations`), alerta de revocación parcial y reintento manual:
  **HU-W4.2-02**. El servicio ya existe (`RevocationService.Revoke` y `RetryPending`, ver `services/audit/README.md`).
- Listado y filtros para cumplimiento: HU-W4.2-01.
