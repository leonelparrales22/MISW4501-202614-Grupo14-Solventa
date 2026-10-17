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
`8484` (`TOXIPROXY_PORT`), pasarela `9140` (`GATEWAY_PORT`), correo `9130` (`EMAIL_PORT`) y Moto `5110` (`MOTO_PORT`).

## Fases

Cada fase pasa solo si todos los *checks* de k6 se cumplen (`checks == 1`).

| # | Fase | Qué se verifica |
|---|---|---|
| 1 | Línea base | 10 aceptaciones → cobro confirmado, emisión contractual completada, 1 cobro real por clave |
| 2 | Caída total (proxy deshabilitado) | 50 aceptaciones → todas `202` y consultables (CA-1); cobro `pending` con intentos, clave `cobro-<id>` y ventana de 24 h (CA-2); sin emisión (`404`, CA-4) |
| 3 | Reinicio de originación durante la caída | Los 50 cobros siguen `pending` desde PostgreSQL |
| 4 | Respuesta ambigua (*toxic* `timeout` en la respuesta) | 20 aceptaciones → la pasarela cobra, originación no se entera: `pending` y sin emisión |
| 5 | Restablecimiento | Las 70 confirmadas con su emisión completada; en la pasarela, 1 cobro real por clave y 80 en total (0 perdidas, 0 duplicadas); 1 correo por póliza (CA-5) |
| 6 | Ventana vencida (`RETRY_WINDOW_SECONDS=10`, pasarela caída) | 5 aceptaciones → `failed` (`retry_window_expired`), visibles en `GET /v1/issuance-exceptions` para operaciones y prohibidas al asegurado, sin emisión; con la pasarela de vuelta, operaciones las reanuda con nota (misma clave) → confirmadas con 1 cobro real y su emisión (CA-3) |
| 7 | Contrato | Schemathesis (`--checks=all`) sobre `GET /v1/acceptances/{id}/charge`, `POST …/charge/resume` y `GET /v1/issuance-exceptions`, con token de asegurado y de operaciones |

Notas:

- Las emisiones contractuales de HU-W1.3-03 viven en memoria: cada verificación de emisión se hace antes del siguiente reinicio o
  relanzamiento de originación. Los cobros, en cambio, están en PostgreSQL y sobreviven al reinicio (fase 3).
- El cliente de la pasarela tiene el circuit breaker por defecto (3 fallas, 5 s abierto): durante la caída muchos intentos no
  llegan a enviarse (`not_sent`) y se reprograman igual que los ambiguos.
- La ventana real es de 24 h; aquí se acorta a 10 s solo en la fase 6, como EC-D02 («se validó el mecanismo con una ventana
  acortada»).
- **Contrafáctico** (`counterfactual`): con `SUPPORTS_IDEMPOTENCY=false` la pasarela cobra cada reintento. `verify.js` informa
  cuántas claves quedaron duplicadas en vez de fallar. Documenta que la garantía en el caso ambiguo depende de que el
  proveedor respete la clave (D-10); no cuenta para aprobar la historia.

## Archivos

| Archivo | Qué hace |
|---|---|
| `docker-compose.yml` | PostgreSQL, stubs `payment-gateway` y `email`, Toxiproxy (+ `toxiproxy-init`), Moto, originación y, con el perfil `tools`, k6 y Schemathesis |
| `run.sh` | Orquesta las fases, genera los secretos locales y siembra Moto |
| `toxiproxy.sh` | `down`, `ambiguous` y `up` sobre el proxy `payment-gateway` |
| `k6/create.js` | Crea aceptaciones como el asegurado y comprueba que quedan registradas y consultables |
| `k6/await.js` | Espera el estado del cobro (`pending`, `confirmed`, `failed`) y lo verifica con la emisión contractual; con `RESUME=1` reanuda las excepciones |
| `k6/verify.js` | Cuenta en la pasarela los cobros reales por clave y en el stub de correo los mensajes por póliza |
| `k6/lib.js` | Login en Moto y utilidades comunes |
| `resultados/` | Evidencia de cada corrida (local) |
