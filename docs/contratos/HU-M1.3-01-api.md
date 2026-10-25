# Contrato de API — HU-M1.3-01 · Consulta de consentimientos en movilidad

Servicio: **auditoría** (`audit`). Consumidor: cliente móvil (*Perfil → Privacidad y consentimientos*).

El asegurado consulta sus consentimientos de Open Finance: activos y revocados o vencidos, con el detalle y el historial de estados de
cada uno. Las rutas son **de solo lectura** (CA-4) y el titular es siempre la cuenta del token (CA-6).

---

## 1. Cómo reconoce `audit` al titular

Un consentimiento se otorga con el **documento** del titular (pantalla E1 del aliado, HU-W1.2-01); el token del asegurado trae su
**`sub`** del *user pool*. El puente entre los dos es el evento `CustomerLinked` que publica originación al terminar el registro
(HU-M1.1-01): `audit` lo consume de su cola y guarda el vínculo `sub` ↔ documento en `holder_link`
([catálogo de eventos](../events/README.md#customerlinked-hu-m13-01)).

- Si el vínculo todavía no llegó —o la cuenta nunca pasó por el registro, como los usuarios permanentes de prueba— la respuesta es
  `403 holder_not_linked`. El cliente muestra «Tu cuenta todavía no está vinculada a tus consentimientos.» con «Reintentar».
- El documento se compara sin distinguir mayúsculas y sin espacios alrededor, en los dos lados.
- La respuesta **no repite el documento ni el nombre del titular** (minimización).

---

## 2. `GET /v1/consents`

Todos los consentimientos del titular, con su detalle e historial, en una sola llamada: así el cliente guarda una copia completa para
consultarla sin conexión (CA-5). No hay paginación.

| | |
|---|---|
| Autenticación | Token de identidad (ID token) de Cognito: `Authorization: Bearer <token>` |
| Autorización | Rol `insured`. El titular sale del `sub` del token, nunca de un parámetro |
| Caché | `Cache-Control: no-store`; el API Gateway no la cachea |

### Respuesta `200`

```json
{
  "items": [
    {
      "consent_id": "cns-4f1c2a9be0d34c6f8a7e5d1b2c3a4f5e",
      "status": "active",
      "current": true,
      "entities": [{ "code": "aliado-banco-1", "name": "Banco Aliado" }],
      "purpose": "risk_profiling_mortgage_life_offer",
      "channel": "embedded",
      "terms_version": "v1",
      "scope": [
        { "code": "open_finance", "data_categories": ["credit_products", "balances", "payment_behavior"], "validity": "12_months" },
        { "code": "open_data", "data_categories": ["public_records"], "validity": "12_months" },
        { "code": "kyc", "data_categories": ["identity_verification"], "validity": "single_use" }
      ],
      "granted_at": "2026-10-20T14:02:11.123456Z",
      "expires_at": "2027-10-20T14:02:11.123456Z",
      "ended_at": null,
      "revocation_receipt_id": null,
      "history": [
        { "status": "granted", "occurred_at": "2026-10-20T14:02:11.123456Z", "receipt_id": null, "channel": "embedded", "entity_code": null }
      ]
    },
    {
      "consent_id": "cns-9a8b7c6d5e4f30211234567890abcdef",
      "status": "revoked",
      "current": false,
      "entities": [{ "code": "aliado-banco-1", "name": "Banco Aliado" }],
      "purpose": "risk_profiling_mortgage_life_offer",
      "channel": "embedded",
      "terms_version": "v1",
      "scope": ["…"],
      "granted_at": "2026-06-24T10:00:00Z",
      "expires_at": "2027-06-24T10:00:00Z",
      "ended_at": "2026-10-21T09:15:03Z",
      "revocation_receipt_id": "REV-0001",
      "history": [
        { "status": "granted", "occurred_at": "2026-06-24T10:00:00Z", "receipt_id": null, "channel": "embedded", "entity_code": null },
        { "status": "revocation_requested", "occurred_at": "2026-10-21T09:14:58Z", "receipt_id": null, "channel": "mobile", "entity_code": null },
        { "status": "revoked", "occurred_at": "2026-10-21T09:15:03Z", "receipt_id": "REV-0001", "channel": "mobile", "entity_code": null }
      ]
    }
  ],
  "generated_at": "2026-10-24T20:00:00Z"
}
```

| Campo | Descripción |
|---|---|
| `status` | `active`, `expired`, `revoked`; reservados `revocation_pending` (HU-M1.3-02) y `partially_revoked` (HU-W4.2-02). Se deriva del registro y de sus eventos *append-only*, nunca de una columna que cambie |
| `current` | `true` solo para `active`: separa **activos** de **revocados o vencidos** (CA-1, CA-3) |
| `entities` | Entidad autorizada: el aliado por el que se otorgó, con su nombre visible (`PARTNER_DISPLAY_NAMES`); un código sin nombre se muestra tal cual |
| `granted_at`, `channel` | Fecha y hora del otorgamiento y canal (CA-2) |
| `expires_at` | Vigencia: 12 meses desde el otorgamiento |
| `ended_at` | Cuándo dejó de estar vigente (CA-3): la hora de la revocación o `expires_at`; `null` mientras está vigente |
| `revocation_receipt_id` | Comprobante de la revocación, cuando existe (CA-3) |
| `history` | Cambios de estado con fecha y hora, en orden: `granted` (otorgamiento), los eventos del consentimiento y `expired` cuando venció. `entity_code` es `null` para un cambio de todo el consentimiento |

**Orden:** primero los vigentes, del más reciente al más antiguo por `granted_at`; luego el historial, del que terminó más
recientemente. **Vencimiento:** un consentimiento es activo hasta justo antes de `expires_at`; a partir de ese instante es `expired` sin
que nadie escriba nada. Una revocación posterior al vencimiento no cambia el estado, pero aparece en el historial.

---

## 3. `GET /v1/consents/{consent_id}`

El detalle de un consentimiento del titular (CA-2), con la misma forma de cada elemento de la lista. Mismas autenticación,
autorización y caché.

- `consent_id`: 1 a 64 caracteres `[A-Za-z0-9_-]`.
- Un consentimiento que no existe y uno **de otro titular** responden igual, `404 not_found`, para no revelar ids ajenos (CA-6).

---

## 4. Errores

| Código | `error` | Condición |
|---|---|---|
| `401` | `unauthorized` | Sin token, token inválido o vencido |
| `403` | `forbidden` | El rol no es `insured` |
| `403` | `holder_not_linked` | La cuenta todavía no está vinculada a un documento. Reintentable más tarde |
| `404` | `not_found` | El consentimiento no existe o es de otro titular (solo el detalle) |
| `405` | — | Cualquier método distinto de `GET` (solo lectura, CA-4); en el API Gateway esos métodos no se publican |
| `503` | `service_unavailable` | La base de `audit` no respondió. Reintentable |

---

## 5. Lo que queda para otras historias

- **Revocar** (botón, biometría, cola sin conexión, comprobante): HU-M1.3-02. Hasta entonces el cliente muestra «Revocar
  consentimiento» deshabilitado en los activos.
- Listado y filtros para cumplimiento: HU-W4.2-01 (`/v1/audit/*`, con la misma derivación de estado).
- «Última consulta realizada» y el comprobante PDF del otorgamiento: sin criterio ni registro de uso todavía.
