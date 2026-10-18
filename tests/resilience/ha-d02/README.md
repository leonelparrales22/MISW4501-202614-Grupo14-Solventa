# HA-D02 — Cobro de la prima sin pérdida ante caída de la pasarela de pago

Escenario de resiliencia de la historia de arquitectura **HA-D02** (experimento EC-D02, ADR-01) en Docker Compose local: el
mismo ambiente del experimento original, ahora con el código real de originación. Verifica con fallas inyectadas por Toxiproxy
que el 100 % de las aceptaciones registradas durante la falla termina en **exactamente un cobro real** al restablecerse la
pasarela (CA-5), que la cobertura se activa **solo** con el pago confirmado (CA-4) y que la ventana vencida queda como excepción
reanudable (CA-3).

```
k6 ──▶ origination (8190) ──▶ Toxiproxy ──▶ stub payment-gateway (9040): cobra una vez por Idempotency-Key
          │   │                                 GET /debug/charges: solicitudes y cobros reales por clave
          │   └──▶ PostgreSQL (charge_attempt: claim atómico, reintentos, ventana)
          └──▶ documento contractual de HU-W1.3-03 ──▶ stub email (9030): GET /messages?to=
Moto (Cognito local): tokens del asegurado y de operaciones
```

**Medio de pago (HU-W1.3-02).** Desde HU-W1.3-02 el cobro se crea al aprobarse la suscripción pero su primer intento no vence
hasta que el cliente entrega un medio de pago en la pantalla E4 ([contrato](../../../docs/contratos/HU-W1.3-02-api.md) §1 y §3).
Por eso `k6/create.js`, después de registrar las aceptaciones de cada fase, espera a que exista el cobro de cada una
(`GET /v1/acceptances/{id}/charge` responde `404` mientras corre la suscripción) y entrega **PSE**
(`POST …/charge/payment-method` con `{"method":"pse","bank_code":"1007"}`): PSE no tokeniza, así que ninguna tarjeta pasa por
el escenario. Comprueba el `202`, `payment_method.method = "pse"` y la clave `cobro-<id>`. Sin ese paso los cobros quedarían
`pending` sin vencer y el escenario no convergería. La ventana de 24 h cuenta desde que se crea el cobro, no desde el pago.

## Cómo correrlo

```bash
MSYS_NO_PATHCONV=1 bash tests/resilience/ha-d02/run.sh                 # escenario completo
MSYS_NO_PATHCONV=1 bash tests/resilience/ha-d02/run.sh counterfactual  # pasarela que ignora la clave (D-10)
```

Requisitos: Docker (compose v2), `curl` y la AWS CLI (solo para sembrar Moto con
[`tests/security/ha-s01/local/seed.sh`](../../security/ha-s01/local/README.md), siempre contra `localhost`). En Git Bash,
`MSYS_NO_PATHCONV=1` evita que se reescriban las rutas de los contenedores. `run.sh` genera al vuelo las contraseñas de
PostgreSQL y de los usuarios de Moto y el secreto de los enlaces; nunca los imprime ni los guarda. Al terminar (bien o mal)
borra todo con `docker compose down -v`.

Puertos publicados solo en `127.0.0.1` (cambiables con variables): originación `8190` (`ORIGINATION_PORT`), API de Toxiproxy
`8484` (`TOXIPROXY_PORT`), pasarela `9140` (`GATEWAY_PORT`), correo `9130` (`EMAIL_PORT`), Moto `5110` (`MOTO_PORT`) y PostgreSQL
`5440` (`POSTGRES_PORT`, para correr originación desde la máquina contra estos servicios, como explica el README del servicio).

## Fases

Cada fase pasa solo si todos los *checks* de k6 se cumplen (`checks == 1`).

| # | Fase | Qué se verifica |
|---|---|---|
| 1 | Línea base | 10 aceptaciones con PSE → cobro confirmado, emisión contractual completada, 1 cobro real por clave |
| 2 | Caída total (proxy deshabilitado) | 50 aceptaciones → todas `202` y consultables (CA-1); cobro `pending` con intentos, clave `cobro-<id>` y ventana de 24 h (CA-2); sin emisión (`404`, CA-4) |
| 3 | Reinicio de originación durante la caída | Los 50 cobros siguen `pending` desde PostgreSQL |
| 4 | Respuesta ambigua (*toxic* `timeout` en la respuesta) | 20 aceptaciones → registradas, `pending` y sin emisión; además, entre las 70 pendientes, al menos una **cobrada por la pasarela mientras originación sigue esperando** (la respuesta se perdió), con un solo cobro real |
| 5 | Restablecimiento | Las 70 confirmadas con su emisión completada; en la pasarela, 1 cobro real por clave y 80 en total (0 perdidas, 0 duplicadas); 1 correo por póliza (CA-5) |
| 6 | Ventana vencida (`RETRY_WINDOW_SECONDS=10`, pasarela caída) | 5 aceptaciones con PSE entregado en cuanto aparece el cobro → `failed` (`retry_window_expired`; sin medio de pago sería `payment_method_missing`), visibles en `GET /v1/issuance-exceptions` para operaciones y prohibidas al asegurado, sin emisión; con la pasarela de vuelta, operaciones las reanuda con nota (misma clave) → confirmadas con 1 cobro real y su emisión (CA-3) |
| 7 | Contrato | Schemathesis (`--checks=all`) sobre `GET /v1/acceptances/{id}/charge`, `POST …/charge/resume` y `GET /v1/issuance-exceptions`, con token de asegurado y de operaciones |

Notas:

- Las emisiones contractuales de HU-W1.3-03 viven en memoria: cada verificación de emisión se hace antes del siguiente reinicio o
  relanzamiento de originación. Los cobros, en cambio, están en PostgreSQL y sobreviven al reinicio (fase 3).
- El cliente de la pasarela tiene el circuit breaker por defecto (3 fallas, 5 s abierto): durante la caída muchos intentos no
  llegan a enviarse (`not_sent`) y se reprograman igual que los ambiguos; solo la sonda llega a la pasarela. Por eso la fase 4
  exige al menos una aceptación cobrada durante la falla y no las 20.
- Los reintentos de una caída se sincronizan (todos llegan al tope de 60 s y se intentan en el mismo lote): tras restablecer,
  la convergencia toma a lo sumo un ciclo (9–21 s en las corridas locales). Detalle en `resultados/`.
- La ventana real es de 24 h; aquí se acorta a 10 s solo en la fase 6, como EC-D02 («se validó el mecanismo con una ventana
  acortada»).
- `CHARGE_RETRY_MAX_MS` (60 s por defecto, como el servicio) se puede bajar desde el entorno de `docker compose`; el ensayo local
  del humo de staging lo deja en 15 s, como la ventana de staging (D-6 de la fase AWS).
- **Contrafáctico** (`counterfactual`): con `SUPPORTS_IDEMPOTENCY=false` la pasarela cobra cada reintento. `verify.js` informa
  cuántas claves quedaron duplicadas en vez de fallar. Documenta que la garantía en el caso ambiguo depende de que el
  proveedor respete la clave (D-10); no cuenta para aprobar la historia.

## Archivos

| Archivo | Qué hace |
|---|---|
| `docker-compose.yml` | PostgreSQL, stubs `payment-gateway` y `email`, Toxiproxy (+ `toxiproxy-init`), Moto, originación y, con el perfil `tools`, k6 y Schemathesis |
| `run.sh` | Orquesta las fases, genera los secretos locales y siembra Moto |
| `toxiproxy.sh` | `down`, `ambiguous` y `up` sobre el proxy `payment-gateway` |
| `k6/create.js` | Crea aceptaciones como el asegurado, comprueba que quedan registradas y consultables y entrega PSE a cada cobro en cuanto existe |
| `k6/await.js` | Espera el estado del cobro (`pending`, `confirmed`, `failed`) y lo verifica con la emisión contractual; con `RESUME=1` reanuda las excepciones |
| `k6/verify.js` | Cuenta en la pasarela los cobros reales por clave y en el stub de correo los mensajes por póliza |
| `k6/lib.js` | Login en Moto, entrega de PSE (`payWithPSE`) y utilidades comunes |
| `staging/smoke.sh` | Humo de la ventana de staging (ver «Ventana de staging»); también se ensaya contra este ambiente |
| `resultados/` | Evidencia de cada corrida (local) |

## Ventana de staging

`staging/smoke.sh` prueba de punta a punta, en AWS y **por el API Gateway con tokens reales de Cognito**, el cobro de la prima con
PSE, la bandeja del asesor (HU-W1.1-02) y el documento contractual en S3 con su enlace público (HU-W1.3-03), sobre la plataforma de
[`staging/platform`](../../../infra/terraform/environments/staging/platform/README.md) (plan de la fase AWS de HA-D02, §4.5). Solo
usa `bash` y `curl` (el JSON se lee con `sed`, sin `jq`) y, para la evidencia, lecturas de AWS (`aws logs filter-log-events`,
`aws s3api head-object` / `list-object-versions`, `describe-*`): no muta nada. Nunca imprime la contraseña, los tokens ni los enlaces
completos.

| Fase | Comprueba |
|---|---|
| `prepare` (S-0) | Tokens del asegurado, el asesor y operaciones; sin token y con token alterado → `401`; *preflight* CORS → `200` (solo por el gateway) |
| `happy` (S-1…S-5) | Aceptar → cobro `pending` sin medio de pago (E4) → negativos (PSE sin banco `400`, número de tarjeta como token `400`, PSE con token de asesor `403`) → PSE `202` → `confirmed` con `cobro-<id>` y un solo cobro real → PSE otra vez `409` → emisión `completed` en ≤ 60 s → objeto en S3 (`application/pdf`, AES256, una versión) → correo con el enlace → PDF íntegro por el enlace con tres `Accept` distintos, enlace alterado `403 invalid_link` y el mismo PDF con sesión |
| `inbox` (S-6…S-8) | `demo-review` → `in_review`, sin cobro → el asesor lo ve y lo **ajusta** +20 % → el asegurado ve `adjusted` y la oferta `adj-<id>`, recibe el correo, la acepta y la paga con PSE por el monto ajustado; `demo-highrisk` → **rechazo** → `rejected` y su correo, sin cobro; otro `demo-review` → **aceptar** → PSE → `confirmed` |
| `auth` (S-9) | Las tres decisiones con asesor, fecha, tipo y observaciones; segunda decisión `409`; asegurado en la bandeja `403`; asesor en las excepciones `403` |
| `declined` (S-10) | `demo-active-declined…` → PSE → `payment_rejected`, sin póliza ni cobro real; segundo PSE con `payment_attempt = 2` y clave `cobro-<id>-2` → otra vez rechazado |
| `outage-start` (S-11) | **Con la pasarela caída:** A con PSE queda `pending` con intentos, misma clave y próximo intento (E4b); C queda sin medio de pago |
| `outage-expire` (S-12) | A → `retry_window_expired` (conserva el PSE); C → `payment_method_missing`; los dos en `GET /v1/issuance-exceptions`; PSE a cualquiera → `409 charge_not_payable`; sin pólizas. Luego B1…B5 aceptadas con PSE, `pending` |
| `outage-restore` (S-13) | **Con la pasarela restaurada:** B1…B5 `confirmed` solas, un cobro real cada una y cinco en total (CA-5) |
| `resume` (S-14) | El asegurado no puede reanudar (`403`); operaciones reanuda A con nota → PSE conservado → `confirmed` con `cobro-<A>`, un cobro real, emisión y correo; segunda reanudación `409`. C no se reanuda (H-1) |
| `readings` (S-15) | 10 servicios ECS estables, originación en 1 tarea y sin objetivo de autoescalado, *target groups* sanos y los logs de arranque de originación y `audit` |
| `regression` (S-16) | Ruta de socio sin API key → `403` (HA-L01); en local, `GET /health` |

El estado entre llamadas (identificador de la corrida e ids de las aceptaciones, nunca un secreto) queda en `STATE_DIR`. La
evidencia de los stubs se lee de CloudWatch Logs en AWS (`EMAIL_SOURCE=logs`, `GATEWAY_SOURCE=logs`: el correo deja cada mensaje en
su log y la pasarela una línea `charge request key=… new_charge=…` por cobro) o de sus API en local (`api`).

### En AWS (la corre el agente principal en la ventana)

```bash
cd tests/resilience/ha-d02
export TF_DIR=../../../infra/terraform/environments/staging/platform   # invoke_url, cliente web, bucket y logs
export TF_VAR_seed_user_password=…   # el mismo valor del apply (de SOLVENTA_HA_D02_SEED_PASSWORD), nunca en un archivo
export STATE_DIR=<carpeta de la evidencia>
bash staging/smoke.sh prepare happy inbox auth declined
# terraform plan/apply con -var 'stub_desired_count_overrides={"payment-gateway"=0}' (solo cambia payment-gateway)
bash staging/smoke.sh outage-start
# preparar restore.tfplan (sin el override) mientras corre la espera de la fase siguiente
bash staging/smoke.sh outage-expire      # espera unos 300 s (RETRY_WINDOW_SECONDS de ha-d02.example.tfvars)
# terraform apply restore.tfplan en cuanto termina: B1…B5 deben confirmarse dentro de sus 300 s
bash staging/smoke.sh outage-restore resume readings regression
```

### Ensayo local (a US$ 0)

Contra el ambiente de este escenario, sembrado como en `run.sh`, con originación relanzada con `RETRY_WINDOW_SECONDS=300` y
`CHARGE_RETRY_MAX_MS=15000` y la caída con `./toxiproxy.sh down` / `up`:

```bash
API_URL=http://localhost:8190 COGNITO_URL=http://localhost:5110/ CLIENT_ID=<cliente web de Moto> SEED_USER_PASSWORD=<MOTO_USER_PASSWORD> \
INSURED_USER=asegurado.local@example.com ADVISOR_USER=asesor.local@example.com OPERATIONS_USER=operaciones.local@example.com \
EMAIL_SOURCE=api EMAIL_URL=http://localhost:9130 GATEWAY_SOURCE=api GATEWAY_URL=http://localhost:9140 S3_BUCKET= \
bash staging/smoke.sh prepare happy inbox auth declined   # luego outage-start, outage-expire, outage-restore, resume, regression
```

Sin `S3_BUCKET` se saltan las lecturas de S3 (el documento va al directorio local) y, fuera del gateway, el *preflight* CORS, las
lecturas de AWS y la ruta de socio. `docker-compose.yml` fija `DOCUMENTS_PUBLIC_BASE_URL` en `http://localhost:8190` para que el
enlace del correo se abra desde la máquina, como en staging por el gateway.
