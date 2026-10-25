# Contrato de API — HU-W4.2-02 · Revocar un consentimiento de Open Finance desde el portal de cumplimiento

Servicio: **auditoría** (`audit`). Consumidor: portal web de cumplimiento (*Consentimientos Open Finance → detalle → Revocar*).

El analista del rol de cumplimiento revoca un consentimiento activo después de indicar el motivo, la referencia y confirmar su rol. No es
un proceso nuevo: reutiliza el de HU-M1.3-02 ([contrato](HU-M1.3-02-api.md)). `audit` deja el registro inmutable **antes** de responder
(CA-5), desde ese instante deja de verificar el consentimiento como activo, así que originación no vuelve a consultar las fuentes (CA-2),
y avisa a cada entidad dentro de 5 minutos (RN-02). Si alguna no confirma, el consentimiento queda en *Revocación parcial* y el analista
reintenta solo con las pendientes (CA-3).

> **Los ejemplos son respuestas capturadas** del servicio real en Docker (`CAPTURE_DIR=<carpeta> ./tests/e2e/hu-w4.2/run.sh`); los
> identificadores, el `sub` y las horas son los de esa corrida. Donde el alcance se repite se escribe `[ … ]`.

---

## 1. Cómo funciona

```
1. el detalle abre el modal ──GET /v1/audit/consents/{id}/revocation──▶ titular, alcance, entidades y revocable
2. el modal confirma ─────────POST /v1/audit/consents/{id}/revocations (Idempotency-Key, reason, reference, role_confirmed)──▶ audit
       una transacción, antes de responder: consent_revocation (inmutable, REV-AAAA-NNNN, canal compliance_portal, actor operations)
                                            + consent_event revocation_requested y revoked
                                            + un aviso por entidad con su ventana de 5 min
   ◀── 201 ComplianceRevocationState (status revoked, entidades pending)
3. el portal sondea ──────────GET …/revocation cada 5 s mientras notice_status = in_progress o alguna entidad esté pending (el plazo es de 5 min)
4. alerta de revocación parcial ──POST …/revocation-retries──▶ ventana nueva solo para las entidades no confirmadas
   (sigue «parcial» con la entidad en pending hasta que confirme; entonces pasa a «revocado»)
```

- **El detalle y el listado** del portal son de HU-W4.2-01 (todavía no existe). Esta historia no depende de ella: el modal solo necesita
  el `consent_id` y las tres rutas de este documento.
- **«Revocado» desde que se registra:** Solventa deja de consultar en ese instante. El aviso a las entidades se sigue en
  `revocation.entities` y `revocation.notice_status`; si alguna no confirma dentro de su ventana, `status` pasa a `partially_revoked`.
- **Quién:** `revocation.requested_by` = `{type: "operations", id: <sub del token>}`. El servicio no guarda nombres de personas.
- **Minimización:** las respuestas traen el nombre del titular y su documento **enmascarado** (`******4050`); el documento completo
  sigue solo en la evidencia (`GET /v1/audit/consents/{consent_id}`). Nunca traen la clave de idempotencia ni datos de las fuentes.

Las tres rutas son humanas: `Authorization: Bearer <ID token de Cognito>`, rol **`operations`** y `Cache-Control: no-store`. Los roles
`advisor`, `insured` y `partner` reciben `403` y no se ejecuta nada; sin token, `401`.

---

## 2. `GET /v1/audit/consents/{consent_id}/revocation`

El estado que necesitan el modal «Confirmar revocación», la alerta de revocación parcial y el reintento (CA-1, CA-3, CA-4, CA-5).

### Respuesta `200`: `ComplianceRevocationState`

Consentimiento activo (abre el modal):

```json
{
  "consent_id": "cns-243e298942b76d692ce6dcd8c82a7e57",
  "status": "active",
  "revocable": true,
  "holder": {
    "full_name": "Asegurado De Prueba",
    "document_masked": "******4050"
  },
  "scope": [
    {
      "code": "open_finance",
      "data_categories": [
        "credit_products",
        "balances",
        "payment_behavior"
      ],
      "validity": "12_months"
    },
    {
      "code": "open_data",
      "data_categories": [
        "public_records"
      ],
      "validity": "12_months"
    },
    {
      "code": "kyc",
      "data_categories": [
        "identity_verification"
      ],
      "validity": "single_use"
    }
  ],
  "entities": [
    {
      "code": "aliado-banco-1",
      "name": "Banco Aliado"
    }
  ],
  "revocation": null,
  "history": [
    {
      "status": "granted",
      "occurred_at": "2026-10-25T18:48:32.074264Z",
      "entity_code": null,
      "actor_type": null,
      "reason": null,
      "reference": null
    }
  ]
}
```

Revocación parcial (alerta con las entidades que no confirmaron y la hora en que se agotó el plazo):

```json
{
  "consent_id": "cns-882bf3ab00566d28188708e3cf8ee2d7",
  "status": "partially_revoked",
  "revocable": false,
  "holder": {
    "full_name": "Asegurado De Prueba",
    "document_masked": "******4050"
  },
  "scope": [ … ],
  "entities": [
    {
      "code": "aliado-banco-1",
      "name": "Banco Aliado"
    }
  ],
  "revocation": {
    "receipt_number": "REV-2026-0002",
    "requested_at": "2026-10-25T18:48:34.634658Z",
    "effective_at": "2026-10-25T18:48:34.635218Z",
    "channel": "compliance_portal",
    "notice_status": "partial",
    "requested_by": {
      "type": "operations",
      "id": "3831237d-c1ab-457b-8def-3f1d5196e773"
    },
    "reason": "regulatory_order",
    "reference": "TCK-r1792954003-P",
    "entities": [
      {
        "code": "aliado-banco-1",
        "name": "Banco Aliado",
        "status": "not_confirmed",
        "confirmed_at": null,
        "deadline_at": "2026-10-25T18:48:39.635218Z"
      }
    ]
  },
  "history": [
    {
      "status": "granted",
      "occurred_at": "2026-10-25T18:48:32.100949Z",
      "entity_code": null,
      "actor_type": null,
      "reason": null,
      "reference": null
    },
    {
      "status": "revocation_requested",
      "occurred_at": "2026-10-25T18:48:34.634658Z",
      "entity_code": null,
      "actor_type": "operations",
      "reason": "regulatory_order",
      "reference": "TCK-r1792954003-P"
    },
    {
      "status": "revoked",
      "occurred_at": "2026-10-25T18:48:34.635218Z",
      "entity_code": null,
      "actor_type": "operations",
      "reason": "regulatory_order",
      "reference": "TCK-r1792954003-P"
    },
    {
      "status": "revocation_not_confirmed",
      "occurred_at": "2026-10-25T18:48:39.755829Z",
      "entity_code": "aliado-banco-1",
      "actor_type": "system",
      "reason": "retry_window_expired",
      "reference": null
    }
  ]
}
```

| Campo | Descripción |
|---|---|
| `status` | `active`, `expired`, `revoked` o `partially_revoked` |
| `revocable` | `true` solo con `active`. Con `false` el portal oculta la acción **Revocar** y muestra por qué (revocado, parcial o vencido, CA-4) |
| `holder` | Nombre completo y documento enmascarado (solo los 4 últimos visibles) |
| `scope` | Copia del alcance con que el cliente consintió |
| `entities` | Entidades que el consentimiento autoriza (hoy una: el aliado) |
| `revocation` | `null` si no se ha revocado |
| `revocation.channel` | `compliance_portal` o `mobile_app` (si lo revocó el titular). Es texto libre: el cliente no debe asumir un conjunto cerrado |
| `revocation.reason`, `reference` | `null` si lo revocó el titular desde la app |
| `revocation.notice_status` | `in_progress` (alguna entidad aún tiene tiempo), `completed` (todas confirmaron) o `partial` (alguna no confirmó). Tras un reintento sigue en `partial` hasta que la entidad confirme |
| `revocation.entities[].status` | `pending`, `confirmed` o `not_confirmed`; `deadline_at` es el fin de su ventana (en `not_confirmed`, la hora en que se agotó el plazo) |
| `history` | Cambios de estado en orden cronológico, con `actor_type`, `reason` y `reference` cuando existen. En los eventos del consentimiento (`revocation_requested`, `revoked`) son el actor, el motivo y la referencia de la revocación. En los de una entidad (`entity_code` informado) el actor es `system` (el trabajador) u `operations` (el reintento); `reason` es la causa del fallo (por ejemplo `retry_window_expired`) y `reference` la de la propia entidad al confirmar |

---

## 3. `POST /v1/audit/consents/{consent_id}/revocations`

| | |
|---|---|
| Autorización | Rol `operations` |
| Idempotencia | Cabecera `Idempotency-Key` obligatoria: un UUID que el portal genera **al abrir el modal**. La misma clave devuelve la misma revocación con `200`; un doble clic o un reintento de red no crea dos revocaciones |
| Caché | `Cache-Control: no-store` |

### Cuerpo

```json
{ "reason": "customer_request", "reference": "TCK-2026-0412", "role_confirmed": true }
```

| Campo | Regla |
|---|---|
| `reason` | `customer_request` (solicitud del cliente) o `regulatory_order` (orden regulatoria). Obligatorio |
| `reference` | Número de ticket u orden, 3 a 64 caracteres una vez colapsados los espacios repetidos; letras, dígitos, espacio y `- _ . / #`. Obligatoria |
| `role_confirmed` | La casilla del modal. Debe ser `true`; `false` o ausente se rechaza |

No hay campo de fecha: `requested_at` y `effective_at` son la hora del servidor. El dispositivo se toma del `User-Agent` de la petición
(recortado a 120 caracteres); nunca del cuerpo. Un campo desconocido se rechaza.

Si falta algo, la respuesta nombra **todos** los campos a la vez y **no se registra nada**:

```json
{
  "error": "validation_failed",
  "message": "one or more fields are invalid",
  "fields": [
    {
      "field": "reason",
      "code": "required"
    },
    {
      "field": "reference",
      "code": "required"
    },
    {
      "field": "role_confirmed",
      "code": "must_be_true"
    }
  ]
}
```

| Campo | Códigos |
|---|---|
| `reason` | `required`, `invalid_format` |
| `reference` | `required`, `out_of_range`, `invalid_format` |
| `role_confirmed` | `must_be_true` |
| `Idempotency-Key` | `required`, `invalid_format` |

### Respuesta `201` (o `200` con la misma clave): `ComplianceRevocationState`

El mismo objeto de la sección 2, con `status: "revoked"`, `revocable: false` y `revocation` completa; las entidades salen `pending` con su
`deadline_at` (`effective_at` + el plazo de los avisos, 5 min en AWS y 5 s en la prueba e2e). `requested_at` y `effective_at` son la hora
del servidor y pueden diferir por microsegundos.

```json
{
  "consent_id": "cns-243e298942b76d692ce6dcd8c82a7e57",
  "status": "revoked",
  "revocable": false,
  "holder": {
    "full_name": "Asegurado De Prueba",
    "document_masked": "******4050"
  },
  "scope": [ … ],
  "entities": [
    {
      "code": "aliado-banco-1",
      "name": "Banco Aliado"
    }
  ],
  "revocation": {
    "receipt_number": "REV-2026-0001",
    "requested_at": "2026-10-25T18:48:32.7057Z",
    "effective_at": "2026-10-25T18:48:32.706506Z",
    "channel": "compliance_portal",
    "notice_status": "in_progress",
    "requested_by": {
      "type": "operations",
      "id": "3831237d-c1ab-457b-8def-3f1d5196e773"
    },
    "reason": "customer_request",
    "reference": "TCK-r1792954003",
    "entities": [
      {
        "code": "aliado-banco-1",
        "name": "Banco Aliado",
        "status": "pending",
        "confirmed_at": null,
        "deadline_at": "2026-10-25T18:48:37.706506Z"
      }
    ]
  },
  "history": [
    {
      "status": "granted",
      "occurred_at": "2026-10-25T18:48:32.074264Z",
      "entity_code": null,
      "actor_type": null,
      "reason": null,
      "reference": null
    },
    {
      "status": "revocation_requested",
      "occurred_at": "2026-10-25T18:48:32.7057Z",
      "entity_code": null,
      "actor_type": "operations",
      "reason": "customer_request",
      "reference": "TCK-r1792954003"
    },
    {
      "status": "revoked",
      "occurred_at": "2026-10-25T18:48:32.706506Z",
      "entity_code": null,
      "actor_type": "operations",
      "reason": "customer_request",
      "reference": "TCK-r1792954003"
    }
  ]
}
```

---

## 4. `POST /v1/audit/consents/{consent_id}/revocation-retries`

Sin cuerpo y sin `Idempotency-Key`. Solo procede con la revocación **parcial** (`notice_status = partial`) que tenga una entidad en
`not_confirmed`: abre una ventana nueva de 5 minutos **solo** para las entidades que no confirmaron; las que ya confirmaron nunca se
vuelven a avisar. Deja un evento `revocation_retry_requested` con el actor.

### Respuesta `202`

```json
{
  "retried_entities": [
    "aliado-banco-1"
  ],
  "state": {
    "consent_id": "cns-882bf3ab00566d28188708e3cf8ee2d7",
    "status": "partially_revoked",
    "revocable": false,
    "holder": {
      "full_name": "Asegurado De Prueba",
      "document_masked": "******4050"
    },
    "scope": [ … ],
    "entities": [
      {
        "code": "aliado-banco-1",
        "name": "Banco Aliado"
      }
    ],
    "revocation": {
      "receipt_number": "REV-2026-0002",
      "requested_at": "2026-10-25T18:48:34.634658Z",
      "effective_at": "2026-10-25T18:48:34.635218Z",
      "channel": "compliance_portal",
      "notice_status": "partial",
      "requested_by": {
        "type": "operations",
        "id": "3831237d-c1ab-457b-8def-3f1d5196e773"
      },
      "reason": "regulatory_order",
      "reference": "TCK-r1792954003-P",
      "entities": [
        {
          "code": "aliado-banco-1",
          "name": "Banco Aliado",
          "status": "pending",
          "confirmed_at": null,
          "deadline_at": "2026-10-25T18:48:45.439346045Z"
        }
      ]
    },
    "history": [
      {
        "status": "granted",
        "occurred_at": "2026-10-25T18:48:32.100949Z",
        "entity_code": null,
        "actor_type": null,
        "reason": null,
        "reference": null
      },
      {
        "status": "revocation_requested",
        "occurred_at": "2026-10-25T18:48:34.634658Z",
        "entity_code": null,
        "actor_type": "operations",
        "reason": "regulatory_order",
        "reference": "TCK-r1792954003-P"
      },
      {
        "status": "revoked",
        "occurred_at": "2026-10-25T18:48:34.635218Z",
        "entity_code": null,
        "actor_type": "operations",
        "reason": "regulatory_order",
        "reference": "TCK-r1792954003-P"
      },
      {
        "status": "revocation_not_confirmed",
        "occurred_at": "2026-10-25T18:48:39.755829Z",
        "entity_code": "aliado-banco-1",
        "actor_type": "system",
        "reason": "retry_window_expired",
        "reference": null
      },
      {
        "status": "revocation_retry_requested",
        "occurred_at": "2026-10-25T18:48:40.439346Z",
        "entity_code": "aliado-banco-1",
        "actor_type": "operations",
        "reason": null,
        "reference": null
      }
    ]
  }
}
```

Mientras corre la ventana nueva, la entidad vuelve a `pending` con la nueva `deadline_at`, pero `status` sigue en `partially_revoked` y
`notice_status` en `partial`: la revocación solo pasa a `revoked` / `completed` cuando la entidad confirma, y si la ventana se agota
otra vez la entidad vuelve a `not_confirmed` y se puede reintentar de nuevo. El portal sigue sondeando la sección 2 mientras haya
entidades `pending`. Un segundo reintento con la ventana abierta responde `409 nothing_to_retry`.

---

## 5. Errores

| Código | `error` | Ruta | Condición |
|---|---|---|---|
| `400` | `validation_failed` | POST revocations | Campo inválido o `Idempotency-Key` ausente o que no es un UUID; `fields` dice cuál y por qué |
| `400` | `invalid_request` | POST revocations | El cuerpo no es JSON, trae un campo desconocido o pasa de 16 KiB |
| `401` | `unauthorized` | las tres | Sin token, token inválido o vencido |
| `403` | `forbidden` | las tres | El rol no es `operations`; no se ejecuta nada |
| `404` | `consent_not_found` | las tres | El consentimiento no existe (un id mal formado se responde igual) |
| `409` | `consent_already_revoked` | POST revocations | Ya está revocado, también si lo revocó el titular desde la app o quedó parcial |
| `409` | `consent_not_active` | POST revocations | El consentimiento venció |
| `409` | `idempotency_key_reused` | POST revocations | La clave ya revocó otro consentimiento |
| `409` | `consent_not_revoked` | POST retries | El consentimiento no tiene revocación |
| `409` | `nothing_to_retry` | POST retries | El aviso está `in_progress` o `completed`, o todas las entidades no confirmadas ya están en una ventana de reintento; el mensaje lleva el `notice_status` |
| `503` | `service_unavailable` | las tres | No se pudo leer o registrar; nada cambió. La revocación es reintentable con la **misma** clave |

---

## 6. Lo que ve el titular

Nada cambia para la app móvil (HU-M1.3-01 y HU-M1.3-02): el consentimiento aparece `revoked` (o `partially_revoked`) en
`GET /v1/consents/{consent_id}` y el comprobante en PDF se descarga en `GET /v1/consents/{consent_id}/revocation-receipt`, sin importar
el canal. En su historial, el canal de la revocación del portal es `compliance_portal` (**equipo móvil:** `channel` es texto libre; la app
debe mostrarlo sin asumir que solo existe `mobile_app`). El PDF lo nombra «Portal de cumplimiento de Solventa» y como confirmación
«Confirmación del rol de cumplimiento».

---

## 7. Lo que queda para otras historias

- Listado, filtros, indicadores y detalle completo del portal de cumplimiento: **HU-W4.2-01**. El indicador «revocaciones fuera de plazo»
  se puede derivar de `notice_status = partial`.
- Comprobante al cliente por correo o *push*: `audit` no conoce el correo del titular y `notifications` es un esqueleto.
- Un grupo de Cognito `compliance` propio: hoy cualquier analista de `operations` puede revocar (queda registrado su `sub`).
- Nombre del analista en el registro: solo se guarda el `sub`.
- Publicación en AWS: el API Gateway publica `/v1/audit/consents/{proxy+}` en un PR de infraestructura aparte.
