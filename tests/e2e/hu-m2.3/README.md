# Pruebas de punta a punta · HU-M2.3-01 (solicitud de asistencia con GPS)

Levanta en Docker:

- el servicio `claims` real, con su PostgreSQL;
- el stub del core de pólizas (`stubs/policy-core`) **detrás de Toxiproxy**, para hacerlo lento o cortarlo;
- Moto como Cognito, con el asegurado, operaciones y un segundo asegurado que crea el script.

Sin correo, pasarela ni S3: la solicitud solo necesita el core de pólizas. Llama a las rutas como lo haría la app
([contrato](../../../docs/contratos/HU-M2.3-01-api.md)). Toma la estructura de [`hu-w2.3`](../hu-w2.3/README.md) y el core
detrás de Toxiproxy de [`hu-w2.1`](../hu-w2.1/README.md), sin modificarlos, con otros puertos.

```bash
./tests/e2e/hu-m2.3/run.sh
```

## Escenarios

| # | Qué verifica | CA | Cómo |
|---|---|---|---|
| 1 | Una solicitud con ubicación (4,7712 N · 74,0452 O, 8 m, *fix* de hace 6 s, «Autopista Norte km 12, Bogotá») responde `201` con `Cache-Control: no-store`, número `AST-<año>-<4 dígitos o más>`, `status: new`, la póliza con línea, producto y objeto asegurado, y `requested_at` en UTC con el reloj del servidor. El `GET` devuelve lo mismo y la fila de `assistance_request` también (con el `sub` y el nombre del token). No crea filas en `claim` ni aparece en `GET /v1/claims` | CA-2 | `POST /v1/claims/assistance-requests`, `GET …/{request_id}`, `psql` |
| 2 | Cada motivo (`permission_denied`, `location_disabled`, `no_fix`) responde `201` sin coordenadas, precisión ni instante. `unavailable` con coordenadas es `422 validation_failed` (`location.latitude: not_applicable`). Una póliza de Hogar también recibe la solicitud | CA-4 | `POST` con `location.status = unavailable` |
| 3 | La póliza de otro asegurado es `422 policy_not_found` (sin vigencia) y la vencida del stub es `422 policy_not_active` (con vigencia). Ninguna se registra | CA-5 | `POST` con las pólizas que el stub da a cada asegurado |
| 4 | Con el core más lento que el corte (latencia de 3 s en Toxiproxy) y con el core cortado, la respuesta es `503 policy_directory_unavailable` en menos de 1,5 s y no se registra nada. Al restaurarlo, la **misma** llave responde `201` y queda una sola fila | CA-5, D2 | *Toxic* `latency` y `enabled: false` sobre el *proxy* `policycore` |
| 5 | La misma llave y el mismo cuerpo responden `200` con el mismo `request_id`, número y `requested_at`. La misma llave con otro cuerpo es `422 idempotency_key_reused`; sin llave, `400` | S6 | `POST` repetido |
| 6 | Sin token, `401`; `operations`, `403` al solicitar y al leer. El segundo asegurado no lee la solicitud del primero: `404 assistance_request_not_found`, idéntico al de un id que no existe. Un `sub` en el cuerpo es `400` | S1, S9 | Tokens de Moto |
| 7 | `UPDATE` de coordenadas, póliza o `requested_at`, `DELETE` y `TRUNCATE` fallan; `UPDATE` solo de `status` pasa y la lectura lo muestra con lo demás intacto. Los logs de `claims` traen las solicitudes con su número y `location_status`, pero no las coordenadas, el `place`, el `sub` ni el nombre | D7, S11 | `psql` y `docker compose logs` |

El script termina con un código distinto de cero si algún chequeo falla y siempre elimina el entorno (`down -v`).

Con `CAPTURE_DIR=<carpeta>` guarda además las respuestas de los escenarios 1 a 6 (`201-sent.json`, `422-policy-not-active.json`,
…): son los ejemplos de la [guía del contrato](../../../docs/contratos/HU-M2.3-01-api.md).

## Requisitos

- Docker (compose v2), `curl`, `jq` y la AWS CLI (solo para sembrar Moto).
- Las contraseñas son aleatorias, se generan en cada corrida y no se imprimen.
- Puertos publicados solo en `127.0.0.1`. Se cambian con las variables indicadas.

  | Servicio | Puerto | Variable |
  |---|---|---|
  | `claims` | 8303 | `CLAIMS_PORT` |
  | Stub del core de pólizas | 9163 | `POLICY_CORE_PORT` |
  | API de Toxiproxy | 8503 | `TOXIPROXY_PORT` |
  | Moto | 5127 | `MOTO_PORT` |

- El API Gateway no se emula: las rutas de la solicitud cuelgan de `/v1/claims*`, que ya publica (`GET` y `POST`).
