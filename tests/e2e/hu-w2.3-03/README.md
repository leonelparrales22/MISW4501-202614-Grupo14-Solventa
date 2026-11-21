# Pruebas de punta a punta · HU-W2.3-03 (atención de solicitudes de asistencia entrantes)

Levanta en Docker:

- el servicio `claims` real, con su PostgreSQL, los prestadores de grúa y asistencia vial que siembra
  (`0015_assistance_provider_seed`) y el *worker* de los avisos;
- el stub de correo (`stubs/email`) **detrás de Toxiproxy**, para cortarlo mientras se despacha;
- el stub del core de pólizas (`stubs/policy-core`), para las solicitudes de la app (HU-M2.3-01);
- Moto como Cognito, con el asegurado, el asesor, operaciones (Andrés Ruiz) y un segundo analista de operaciones (Diana Torres)
  que crea el script.

Sin S3 ni pasarela. Llama a las rutas como lo harían la app del asegurado y el portal de operaciones
([contrato](../../../docs/contratos/HU-W2.3-03-api.md)). Toma la estructura de [`hu-m2.3`](../hu-m2.3/README.md) y el correo
detrás de Toxiproxy de [`hu-w2.3`](../hu-w2.3/README.md), sin modificarlos, con otros puertos.

```bash
./tests/e2e/hu-w2.3-03/run.sh
```

## Escenarios

El asegurado envía cuatro solicitudes: **A** con la ubicación del teléfono (4,7712 N · 74,0452 O, 8 m, «Autopista Norte km 12,
Bogotá»), **B** sin GPS (`no_fix`), **C** de Hogar sin permiso de ubicación y **D** sin ubicación.

| # | Qué verifica | CA | Cómo |
|---|---|---|---|
| 1 | A y B aparecen en la bandeja en menos de 60 s (se mide), la más reciente primero, con `Cache-Control: no-store`. Cada una trae póliza, hora en UTC y estado de la ubicación; las coordenadas solo vienen en A. Los indicadores cuentan 2 nuevas. El filtro por estado funciona y un estado desconocido es `400` | CA-1 | `POST /v1/claims/assistance-requests` (asegurado), `GET /v1/claims/assistance-inbox` |
| 2 | Abrir el detalle no cambia el estado. Antes de atender, la necesidad es `409 assistance_not_attended`. Andrés atiende A (`in_call`). Una necesidad de otra línea es `422 need_not_for_line` y despachar sin necesidad es `422 need_missing`. Con `towing` se sugiere Grúas Autopista Norte a 0,9 km con llegada en 12 min; todos los candidatos tienen la especialidad y van del más cercano al más lejano. A no acepta ubicación por voz (`409 location_from_app`). B: Andrés la atiende y Diana la toma. Sin ubicación no hay búsqueda (`location_missing`), ni siquiera con la necesidad. Una ubicación por voz inválida es `422` con todos los campos; la válida («Vía Cali–Palmira km 4» y el pin) sugiere Grúas del Valle | CA-2 | `POST …/{id}/attend`, `/need`, `/voice-location`; `GET …/{id}` |
| 3 | Un prestador fuera del radio es `422 provider_not_eligible`. A se despacha a Grúas Autopista Norte: `dispatched` con hora, analista y la ubicación compartida (0,9 km, 12 min). El prestador recibe **un** correo con la llave `asistencia-<id>`, el número de la solicitud, las coordenadas con su precisión y el lugar, sin el nombre del asegurado; el aviso queda `sent`. Despachar otra vez es `409`. Con Toxiproxy cortando el correo, Diana despacha B: `200`, el aviso queda `pending` con el intento fallido registrado y el prestador no recibe nada. Al restaurar el correo, el aviso sale una sola vez, con la ubicación indicada por voz. Los indicadores cuentan 2 despachadas hoy con su tiempo medio | CA-3 | `POST …/dispatch`, `GET /messages` del stub, `enabled: false` sobre el *proxy* `email` |
| 4 | Cerrar C con `other` sin nota es `422` (`note: required`). C se cierra desde `new` con `resolved_by_phone`: `closed`, con hora, motivo y analista, sin despacho; no sale ningún correo ni se escribe un despacho. Una cerrada no se atiende y una despachada no se cierra (`409 assistance_not_open`). Si Andrés y Diana cierran D a la vez, una respuesta es `200` y la otra `409`, y queda una sola decisión | CA-4 | `POST …/close`, `psql` |
| 5 | El historial de A (atender, necesidad, despacho), de B (atender, tomar con el correo del analista anterior, necesidad, ubicación por voz, despacho) y de C (cierre con su motivo) trae el analista y la hora de cada decisión, en orden. `UPDATE`, `DELETE` y `TRUNCATE` sobre `assistance_action`, `assistance_need`, `assistance_voice_location`, `assistance_dispatch` y `assistance_closure` fallan. De lo que envió la app solo cambió `status` | CA-5 | `GET …/{id}`, `psql` |
| 6 | El asegurado y el asesor reciben `403` en la bandeja, el detalle y las decisiones; sin token, `401`; un id que no existe, `404`. El asegurado ve A `dispatched` y C `closed` en su consulta, sin datos del prestador. Los logs de `claims` no traen coordenadas, direcciones, nombres, correos ni el `sub` del asegurado | S1, S15 | Tokens de Moto, `docker compose logs` |

El script termina con un código distinto de cero si algún chequeo falla y siempre elimina el entorno (`down -v`).

Con `CAPTURE_DIR=<carpeta>` guarda además las respuestas (`200-inbox-new.json`, `200-need.json`, `200-dispatch.json`,
`200-close.json`, …): son los ejemplos de la [guía del contrato](../../../docs/contratos/HU-W2.3-03-api.md).

## Requisitos

- Docker (compose v2), `curl`, `jq` y la AWS CLI (solo para sembrar Moto).
- Las contraseñas son aleatorias, se generan en cada corrida y no se imprimen.
- Puertos publicados solo en `127.0.0.1`. Se cambian con las variables indicadas.

  | Servicio | Puerto | Variable |
  |---|---|---|
  | `claims` | 8304 | `CLAIMS_PORT` |
  | Stub del core de pólizas | 9164 | `POLICY_CORE_PORT` |
  | Stub de correo | 9134 | `EMAIL_PORT` |
  | API de Toxiproxy | 8504 | `TOXIPROXY_PORT` |
  | Moto | 5128 | `MOTO_PORT` |

- El API Gateway no se emula: las rutas de la bandeja cuelgan de `/v1/claims*`, que ya publica (`GET` y `POST`).
