# E2E — HU-M3.2-01 · Alertas 30, 15 y 5 días antes del vencimiento de la póliza

*Backend* local para el ensayo guiado en el emulador. Levanta los servicios **reales** `origination` y `notifications` con su PostgreSQL,
LocalStack 4.14.0 con el tópico `origination` y la cola de `notifications` (con su DLQ, 5 recepciones y entrega *raw*), los simuladores
del núcleo de pólizas (sembrado con las pólizas del prototipo, como en [`hu-m1.2`](../hu-m1.2/README.md)), de Open Finance y del proveedor
de *push*, y un **nginx** delante de los dos servicios (`gateway.conf`): el centro de notificaciones vive en `notifications` y la póliza
que abre cada aviso, en la billetera de `origination`.

```
SUBJECT=<sub de asegurado@example.com> bash tests/e2e/hu-m3.2/app-local.sh
```

El script escribe la semilla del núcleo, levanta el entorno, **vincula al asegurado en originación** (una fila de `customer` con ese
`cognito_sub`: el barrido le pregunta al núcleo por los titulares vinculados; en AWS la fila la crea el registro desde la app) e imprime
cómo construir el APK (`URL_API_LOCAL = http://10.0.2.2:8396`). El barrido corre cada 10 s (`EXPIRY_ALERT_INTERVAL_MS`) en lugar de cada
hora.

| Comando | Qué pasa | Criterio |
|---|---|---|
| (arranque) | AU-2025-118204 vence en 16 días: llega el aviso de **30 días** con la fecha exacta. VJ-2026-000931 (Viaje) vence en 8 y recibe el de **15** | CA-1 |
| (esperar otros barridos) | No llega nada más: cada hito de cada periodo se avisa una vez | CA-2 |
| `--dias-para-vencer 15` · `--dias-para-vencer 5` | AU-2025-118204 pasa a vencer en 15 (o 5) días: llega el aviso de ese hito | CA-1 |
| `--status` | Los avisos que registró originación, las notificaciones del centro y cómo ver los *push* del simulador | — |

Cada fecha nueva de `--dias-para-vencer` es, para el barrido, un periodo nuevo de la póliza (igual que una renovación en el núcleo, que
mueve `valid_to`): recibe el aviso de su último hito cruzado. Volver a correr con el mismo N no avisa nada. **CA-3 (ya renovada):** con una
fecha a más de 30 días (`--dias-para-vencer 120`, como una renovación) no llega ningún aviso de ese periodo.

**CA-4 (app cerrada).** Antes de mover la fecha, cierra la app o mándala a segundo plano. Con el simulador el *push* no llega al teléfono:
la app se entera en su revisión periódica; en AWS sale por FCM como mensaje de solo datos y llega aunque esté cerrada (HU-M3.3-01).

Requiere Docker (compose v2) y `curl`. La contraseña de PostgreSQL es aleatoria y no se imprime; si el entorno ya está arriba, volver a
correr el script la conserva. Puertos, todos en `127.0.0.1`: *gateway* 8396, LocalStack 4580 y el simulador de *push* 9396
(`GET /messages` muestra lo que habría recibido el teléfono). Para detenerlo: `docker compose -f tests/e2e/hu-m3.2/docker-compose.yml down -v`.

## En AWS

El barrido corre en cada tarea de `origination` cada hora y la cola de `notifications` ya está suscrita al tópico `origination`, así que
no hay cambios de infraestructura. Al arrancar, `origination` deja en su log `expiry alerts (HU-M3.2-01): 30, 15 and 5 days before the
end of the policies …`.

**Por qué en AWS se publica el evento a mano.** El simulador del núcleo desplegado no tiene semilla: la vigencia que deriva para cada
titular está anclada al día de hoy, así que sus pólizas nunca quedan a 30, 15 o 5 días y el barrido no avisa nada allá. Para validar en
staging lo que ve el asegurado (CA-1, CA-2 en el centro y CA-4 con Firebase), `nube.sh` publica en el tópico `solventa-stg-origination`
el mismo `PolicyExpiryApproaching` que publicaría el barrido, para la póliza de autos que el núcleo le da a ese asegurado:

```
SUBJECT=<sub de asegurado@example.com> bash tests/e2e/hu-m3.2/nube.sh [DÍAS]   # 15 por omisión; de 0 a 30
```

Con los mismos días el mismo día, el evento repite su id y `notifications` conserva la notificación que ya tenía (CA-2). El barrido en
sí —los hitos, la no repetición y los periodos renovados (CA-2 y CA-3)— lo validan las pruebas de `origination` y este entorno local. Contrato: [`docs/contratos/HU-M3.2-01-api.md`](../../../docs/contratos/HU-M3.2-01-api.md).

Las pruebas por componente están en `services/origination/internal/{domain,expiryalert,adapters/memory,adapters/postgres}`
(`expiry_alert*_test.go`, `sweeper_test.go`) y en `services/notifications/internal/...` (`expiry*_test.go`,
`policy_expiry_approaching_test.go`). Las de PostgreSQL usan testcontainers.
