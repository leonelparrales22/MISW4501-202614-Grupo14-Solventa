# Prueba de contrato de `claims` (OBJ 05)

Levanta el servicio real en Docker y corre [Schemathesis](https://schemathesis.readthedocs.io/) contra
[`../../api/openapi.yaml`](../../api/openapi.yaml) con todos los chequeos (`--checks=all`), sobre las rutas `/v1/claims*`.

```bash
./services/claims/tests/contract/run.sh
```

## Qué levanta

| Contenedor | Para qué |
|---|---|
| `postgres-claims` | Base de datos propia de `claims` (migraciones al arrancar) |
| `policy-core` | Stub del core de pólizas (HU-W2.1-01), puerto 9060 |
| `moto` | Cognito local: emite los tokens de las rutas humanas (sembrado con `tests/security/ha-s01/local/seed.sh`) |
| `claims` | El servicio con `DATABASE_URL`, `POLICY_CORE_URL` y `AUTH_*` |
| `schemathesis` | Se lanza bajo demanda con `docker compose run` |

## Qué prueba

1. **Como asegurado:** la radicación móvil (HU-M2.2-01) y todas las rutas de HU-W2.1-01: opciones, subida de un soporte,
   radicación web, listado, detalle y comprobante. Las pólizas generadas no son del asegurado y los ids no existen, así que se
   alcanzan las respuestas `422` y `404`.
2. **Como asesor:** todas las rutas responden `403`.

El script termina con un código distinto de cero si algún chequeo falla, y siempre elimina el entorno (`down -v`).

## Requisitos

Docker (compose v2), `curl`, `jq` y la AWS CLI (solo para sembrar Moto). Las contraseñas son aleatorias, se generan en cada
corrida y no se imprimen. Puertos publicados solo en `127.0.0.1`: `claims` 8296 (`CLAIMS_PORT`) y Moto 5122 (`MOTO_CLAIMS_PORT`).
