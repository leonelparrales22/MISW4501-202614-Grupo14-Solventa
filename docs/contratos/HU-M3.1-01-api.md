# Contrato de API — HU-M3.1-01 · Notificación push del pago paramétrico automático

> **Fuente para el cliente móvil (Kotlin).** Describe lo que implementa el servicio `notifications` (`internal/application`,
> `internal/push`, `internal/adapters/{http,messaging,postgres,pushprovider}`, migración `0001`). Contrato formal:
> `services/notifications/api/openapi.yaml` (0.2.0). Alimenta **M15 · Centro de notificaciones**, **M15b · Push del sistema** y **M10b ·
> Siniestro paramétrico resuelto automáticamente** de la app.

## 1. Flujo

```
claims (motor paramétrico) ──ParametricPayoutCompleted──▶ SNS claims ──▶ SQS notifications
        │                                                                     │
        └── paid_at (inicio de los 60 s)                notifications ◀───────┘
                                                          ├─ notification (siempre; criterios 5 y 7)
                                                          └─ push_delivery por dispositivo con permiso ──▶ proveedor push (stubs/push)
                                                                 reintentos con la misma clave hasta 24 h (criterio 7)
App ──PUT /v1/devices/{installation_id}──▶ token, idioma y permiso
App ──GET /v1/notifications──────────────▶ centro (M15), el más reciente primero, con unread_count
App ──GET /v1/notifications/{id}─────────▶ detalle del pago (M10b), lo que abre el push (criterio 4)
```

- **Titular:** el `sub` del ID token; nunca viaja como parámetro. La notificación de otro asegurado responde `404`, igual que una que no
  existe.
- **Una notificación por pago** (`ntf-<payout_id>`): varios pagos el mismo día son entradas distintas y ninguna sobrescribe a otra
  (criterio 6). Los *push* comparten el grupo `parametric_payouts`, para que el sistema los apile sin colapsarlos.
- **El centro no depende del *push*:** la notificación se registra al llegar el evento, haya o no dispositivo con permiso y llegue o no el
  *push* (criterios 5 y 7).

## 2. El proveedor de *push* es un simulador (decisión del equipo)

En esta historia no hay proyecto de Firebase: `notifications` envía cada *push* a `stubs/push`, que lo registra pero **no lo entrega al
teléfono**. Para que el asegurado se entere igual, la app:

1. Registra su instalación con `PUT /v1/devices/{installation_id}`: `token` es hoy el mismo id de instalación (no hay token de FCM) y
   `push_enabled` refleja el permiso de notificaciones del sistema.
2. Consulta `GET /v1/notifications` al abrirse, mientras está en primer plano y en segundo plano con WorkManager, y muestra una
   notificación del sistema por cada una nueva, respetando el permiso (criterio 3).
3. Al tocarla abre M10b con el `notification_id`.

Con FCM, el paso 2 lo reemplaza el *push* real: el contrato de las rutas no cambia, `token` pasa a ser el de FCM y el `data` del *push*
trae `notification_id`, `kind` y `claim_id`.

## 3. Rutas

Todas exigen el ID token de Cognito con rol `insured` (`401` sin token, `403` con otro rol) y llevan `Cache-Control: no-store`.

### `PUT /v1/devices/{installation_id}` → `200`

```json
{ "platform": "android", "token": "inst-3f9c…", "push_enabled": true, "locale": "es" }
```

`locale` (`es` o `en`) es el idioma del *push*. Sin permiso (`push_enabled: false`) el servicio no guarda el token ni envía *push*. La
respuesta repite la instalación **sin el token**. `400 invalid_device` si falta la plataforma, el idioma o el token con permiso.

### `GET /v1/notifications?limit=50` → `200`

```json
{
  "unread_count": 1,
  "items": [ {
    "notification_id": "ntf-ppo-20260911-av8421",
    "kind": "parametric_payout",
    "occurred_at": "2026-09-11T21:40:00Z",
    "created_at": "2026-09-11T21:40:02Z",
    "read_at": null,
    "payout": {
      "payout_id": "ppo-20260911-av8421", "claim_id": "SIN-2026-01702", "policy_id": "VJ-2026-000931",
      "amount": 180000, "currency": "COP",
      "trigger": { "parameter": "flight_delay", "event_reference": "AV-8421", "threshold": 180, "observed": 215,
                   "unit": "minutes", "observed_at": "2026-09-11T21:39:00Z" },
      "payment_method": { "brand": "visa", "last4": "1111" },
      "idempotency_key": "pay-ppo-20260911-av8421",
      "paid_at": "2026-09-11T21:40:00Z"
    }
  } ]
}
```

El más reciente primero; `limit` entre 1 y 100 (50 por omisión). `unread_count` cuenta todas las no leídas. **La app escribe los textos**
(«Pago automático recibido: $ 180.000 COP», «Retraso del vuelo AV-8421 (215 min)…») a partir de estos datos, en su idioma.

### `GET /v1/notifications/{notification_id}` → `200`

La misma forma de un elemento de la lista: es lo que muestra M10b (parámetro, umbral, valor observado, clave de idempotencia, monto,
moneda, medio de pago y póliza). `404 notification_not_found` si no existe o es de otro asegurado.

### `POST /v1/notifications/{notification_id}/read` → `204` · `POST /v1/notifications/read-all` → `204`

Marcan leída una o todas («Marcar todas como leídas» de M15). Idempotentes: leerla otra vez conserva la primera hora.

`503 notifications_unavailable` si la base no responde; la app conserva lo que ya tenía.

## 4. Lo que queda fuera

- **El motor paramétrico** (`claims/internal/parametric`): el evento lo publica hoy el ensayo local `tests/e2e/hu-m3.1`.
- **Firebase Cloud Messaging** (ver §2) y las notificaciones de renovación y de soportes pendientes de M15 (HU-M3.2-01 y HU-M3.3-01).
