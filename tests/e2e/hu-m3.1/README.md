# E2E — HU-M3.1-01 · Notificación push del pago paramétrico automático

Escenario de punta a punta con el servicio **real** `notifications` en Docker: su PostgreSQL, LocalStack 4.14.0 con el tópico SNS `claims`
y la cola SQS de `notifications` con su DLQ (las crea `localstack/init-messaging.sh` igual que `infra/terraform/modules/messaging`: 5
recepciones, 30 s de visibilidad, entrega *raw*), el simulador del proveedor de *push* (`stubs/push`) y Moto como Cognito de las rutas
humanas. El pago automático se publica en el tópico `claims` tal como lo publicará el motor paramétrico (`ParametricPayoutCompleted`,
[catálogo](../../../docs/events/README.md)).

```
bash tests/e2e/hu-m3.1/run.sh
```

| Escenario | Qué comprueba |
|---|---|
| P1 | Criterios 1 a 4: un dispositivo con permiso recibe el *push* en ≤ 60 s desde el pago, con el monto, la moneda y el evento («Pago automático recibido: $ 180.000 COP», «AV-8421 … 215 min»), y con `notification_id` para abrir el detalle; la notificación queda en el centro, sin leer, y su detalle trae umbral, valor observado y clave de idempotencia (M10b) |
| P2 | Criterio 6: tres pagos el mismo día son tres notificaciones y tres *push* en el grupo `parametric_payouts`; el mismo evento otra vez no envía un segundo *push* |
| P3 | Criterio 5: sin permiso (`push_enabled: false`) no sale *push* y la notificación espera en el centro |
| P4 | Criterio 7: el proveedor falla las primeras 3 veces (`PUSH_FAIL_FIRST=3`); la notificación ya está en el centro y el *push* llega tras los reintentos, en el idioma del dispositivo |
| P5 | «Marcar todas como leídas», `404` de una notificación que no existe, `403` para `operations` y `401` sin token |

Requiere Docker (compose v2), `curl`, Python y la AWS CLI (solo contra el LocalStack y el Moto locales, siempre con `--endpoint-url` a
`127.0.0.1`). Los secretos son aleatorios y no se imprimen; el entorno se elimina al terminar y el script termina con un código distinto
de cero si algo falla. Puertos (solo `127.0.0.1`): `notifications` 8393, LocalStack 4577, Moto 5131 y `stubs/push` 9393.

## Otras herramientas

| Archivo | Para qué |
|---|---|
| `nube.sh` | Publica un pago automático en el tópico `claims` de un ambiente en AWS (`PLATFORM_NAME`, por omisión `solventa-stg`), con las credenciales de quien lo corre, para el `SUBJECT` del asegurado. Mientras el motor paramétrico de `claims` no exista, es la forma de que un pago llegue al centro de la app en la nube: `notifications` lo consume de su cola, lo registra y envía el *push* al stub `push`, que lo deja en su log de CloudWatch como `message accepted` |
| `app-local.sh` | *Backend* local para el ensayo en el emulador con el pool permanente (los valores públicos de `app/build.gradle.kts` del móvil): levanta el entorno e imprime cómo construir el APK con `URL_API_LOCAL`. `--payout [VUELO] [MINUTOS]` publica un pago automático para el `SUBJECT` de `asegurado@example.com` (por omisión el del prototipo, AV-8421 con 215 min, $ 180.000 COP). Se detiene con `docker compose -f tests/e2e/hu-m3.1/docker-compose.yml down -v` |

Las pruebas por componente están en `services/notifications/internal/...` y `services/notifications/tests/integration` (PostgreSQL con
testcontainers, el trabajador y un proveedor que falla y se recupera).
