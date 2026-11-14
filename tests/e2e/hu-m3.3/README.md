# E2E — HU-M3.3-01 · Notificación por cada cambio de etapa del siniestro

*Backend* local para el ensayo guiado en el emulador. Levanta los servicios **reales** `claims` y `notifications` con su PostgreSQL, LocalStack 4.14.0 con el tópico `claims` y la cola de `notifications` (con su DLQ, 5 recepciones y entrega *raw*), los simuladores del núcleo de pólizas, de la pasarela de pagos y del proveedor de *push*, y un **nginx** delante de los dos servicios (`gateway.conf`).

El nginx existe porque la app usa una sola URL base (`URL_API_LOCAL`), como pasa con el API Gateway en AWS, y esta historia necesita los dos servicios: el centro de notificaciones vive en `notifications` y la línea de tiempo que abre cada notificación, en `claims`. Las rutas humanas verifican los tokens del *user pool* permanente, así que el asegurado inicia sesión en el emulador.

```
SUBJECT=<sub de asegurado@example.com> bash tests/e2e/hu-m3.3/app-local.sh
```

El script radica un siniestro de Hogar del asegurado e imprime cómo construir el APK (`URL_API_LOCAL = http://10.0.2.2:8395`). Después se mueve el caso de etapa en etapa, como lo harían el asesor y operaciones desde el portal:

| Comando | Qué pasa | Criterio |
|---|---|---|
| `--evaluate` | El asesor aprueba la radicación y el caso entra a evaluación | CA-1: notificación «Tu siniestro está en evaluación» con el radicado y la hora |
| `--approve` | Operaciones aprueba $ 4.350.000 con 5 % de deducible | CA-2: «Tu siniestro fue aprobado: $ 4.132.500 COP» |
| (unos 20 s después) | La pasarela simulada paga la indemnización | CA-3: «Pago de tu siniestro realizado: $ 4.132.500 COP», con la fecha del desembolso |
| `--status` | Muestra el estado del caso, las notificaciones del centro y cómo ver los *push* que recibió el simulador | — |

**CA-4 (app cerrada o en segundo plano).** Antes de `--evaluate`, cierra la app o mándala a segundo plano. La notificación del sistema aparece igual si se dio el permiso, y queda en el centro al volver a entrar.

**CA-5 (abrir la notificación).** Al tocar la notificación, en el centro o en el sistema, se abre la línea de tiempo del siniestro.

Requiere Docker (compose v2) y `curl`. La contraseña de PostgreSQL es aleatoria y no se imprime; si el entorno ya está arriba, volver a correr el script la conserva. Puertos, todos en `127.0.0.1`: *gateway* 8395, LocalStack 4579 y el simulador de *push* 9395 (`GET /messages?token=` muestra lo que habría recibido el teléfono). Para detenerlo: `docker compose -f tests/e2e/hu-m3.3/docker-compose.yml down -v`.

## En AWS

No hace falta ningún script: el asegurado radica desde la app y el caso avanza con el portal web. El asesor aprueba la radicación (en evaluación) y operaciones aprueba el caso (aprobado y, en segundos, pagado por la pasarela simulada). `notifications` ya consume la cola suscrita al tópico `claims`, así que no hay cambios de infraestructura.

Las pruebas por componente están en `services/claims/internal/{domain,stages,adapters/postgres}` (`stage*_test.go`) y en `services/notifications/internal/...` (`stage*_test.go`, `claim_stage_changed_test.go`). Las de PostgreSQL usan testcontainers.
