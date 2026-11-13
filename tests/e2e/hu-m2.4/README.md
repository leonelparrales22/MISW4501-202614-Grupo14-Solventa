# E2E — HU-M2.4-01 · Consulta y seguimiento del siniestro (móvil)

*Backend* para el ensayo guiado de la app en el emulador y para la ventana en la nube. Es el servicio **real** `claims` con su PostgreSQL (detrás de Toxiproxy), Redis como caché de lectura (HA-L02), los simuladores del núcleo de pólizas y de la pasarela de pagos, y LocalStack 4.14.0. LocalStack crea el tópico `iot`, la cola de `claims` con su DLQ (5 recepciones, 30 s de visibilidad, entrega *raw*) y el tópico `claims` al que publica el *outbox*. Las rutas humanas verifican los tokens del *user pool* permanente (valores públicos de `app/build.gradle.kts` del móvil), así que el asegurado inicia sesión en el emulador.

```
SUBJECT=<sub de asegurado@example.com> bash tests/e2e/hu-m2.4/app-local.sh
```

| Criterio | Cómo se ve |
|---|---|
| CA-1 · Línea de tiempo | El script siembra dos siniestros del asegurado sobre su póliza de Autos activa: uno recién radicado y otro aprobado por el asesor. `GET /v1/claims/{id}/history` trae cada etapa con su fecha (`changes`) |
| CA-2 · En evaluación | El segundo siniestro queda `in_evaluation`. El triage de `claims` le calcula el nivel en ≤ 2 s, y `GET /v1/claims/{id}/triage` trae los días hábiles estimados |
| CA-3 · Resolución automática | `SUBJECT=<el mismo> bash tests/e2e/hu-m2.4/app-local.sh --payout [VUELO] [MINUTOS]` publica `ParametricTriggerObserved` en el tópico `iot`. El motor paramétrico radica el siniestro, lo resuelve y lo paga a la pasarela, y el *outbox* publica `ParametricPayoutCompleted`. El script espera a que el siniestro quede `paid` y muestra el evento publicado. La lista, el detalle y el historial traen `channel: "parametric"` y `automatic_resolution` (parámetro, umbral, valor observado). Con menos de 180 min no se radica nada |
| CA-4 · Información desactualizada | Abre el siniestro una vez y luego corre `bash tests/e2e/hu-m2.4/app-local.sh --slow-db`: la base responde en 2 s, la lectura espera 900 ms y responde la última de la caché con `stale: true` y `as_of`. Para volver a la normalidad: `--fast-db` |
| CA-5 · Sin conexión | Es de la app: apaga la red del emulador |

Requiere Docker (compose v2), `curl`, Python y la AWS CLI, esta última solo contra LocalStack y siempre con `--endpoint-url` a `127.0.0.1`. La contraseña de PostgreSQL es aleatoria y no se imprime. Puertos, todos en `127.0.0.1`: `claims` 8394, núcleo de pólizas 9394, API de Toxiproxy 8494 y LocalStack 4578. Para detenerlo: `docker compose -f tests/e2e/hu-m2.4/docker-compose.yml down -v`.

## En la nube

```
SUBJECT=<sub de asegurado@example.com> bash tests/e2e/hu-m2.4/nube.sh [VUELO] [MINUTOS]
```

Publica el retraso del vuelo en el tópico `<PLATFORM_NAME>-iot` (por omisión `solventa-stg`) con las credenciales de quien lo corre. La póliza de Viaje del asegurado se calcula igual que en el simulador del núcleo, o se pasa con `POLICY_ID`. El motor de `claims` la consume de su cola y radica, resuelve y paga el siniestro; `notifications` recibe `ParametricPayoutCompleted` y envía el *push* de HU-M3.1-01. Reemplaza a `tests/e2e/hu-m3.1/nube.sh`, que publicaba el pago directamente y no dejaba un siniestro en `claims`.

Requiere la plataforma desplegada con este cambio. Terraform agrega el tópico `iot`, la cola `claims` y las variables `EVENTS_QUEUE_URL` y `EVENTS_TOPIC_ARN` de `claims`.

Las pruebas por componente están en `services/claims/internal/{domain,parametric,adapters/messaging,adapters/postgres,adapters/http}` (`parametric*_test.go`). Las de PostgreSQL usan testcontainers.
