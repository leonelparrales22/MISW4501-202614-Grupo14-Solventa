# Integración entre servicios — HU-W1.2

Prueba de punta a punta de los servicios **reales** `audit` y `origination` en Docker: PostgreSQL de `audit`, Redis, los stubs de Open
Finance, Open Data y KYC detrás de Toxiproxy y Moto como Cognito de las rutas humanas. Es la prueba «entre servicios» de la
[estrategia de pruebas](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Estrategia-de-Pruebas-v-1.2); el
API Gateway no se emula (`AUDIT_URL` apunta directo a `audit`) y las latencias locales no son evidencia de p95.

**Estado:** escenarios de **HU-W1.2-01** (consentimiento explícito). Los de HU-W1.2-03 (oferta personalizada, recotización, correo y
PostgreSQL de `origination`) se agregan a esta carpeta con esa historia.

## Qué verifica `run.sh`

| # | Escenario | Criterio | Cómo se comprueba |
|---|---|---|---|
| 1 | Los términos mostrados son los registrados | CA-1 | `GET /v1/audit/consent-terms/current` y el `terms_version` del consentimiento |
| 2 | Sin consentimiento: `422` y cero peticiones a las fuentes | CA-3 | `POST /v1/risk-profiles/resolve` con `consent_active=true` y conexiones nuevas en Toxiproxy |
| 3 | Versión desactualizada `409`; consentimiento `201`, repetido `200`; evidencia con token `operations`; verificación activa | CA-1, CA-2 | `audit` real |
| 4 | Con consentimiento: perfil `normal` desde las fuentes | CA-3 | `origination` → `audit` → fuentes |
| 5 | Con `audit` detenido: `503 request_aborted` y cero peticiones a las fuentes; al volver, `200` | CA-3 | `docker compose stop audit` |
| 6 | `UPDATE`, `DELETE` y `TRUNCATE` sobre `consent_record` fallan | CA-4 | `psql` dentro del contenedor |
| 7 | Evidencia sin token `401`, como `insured` `403`, id desconocido `404` | Seguridad | Tokens de Moto |

«Cero peticiones a las fuentes» se mide en Toxiproxy: cada llamada a un stub abre una conexión por su proxy y el proxy registra
`Accepted client`. `origination` reutiliza conexiones, así que en el escenario 5 se reinicia antes de medir (un `origination` nuevo no
tiene ninguna abierta).

## Cómo correrlo

Requiere Docker (compose v2), `curl`, `jq` y la AWS CLI (solo para sembrar Moto, como
[`tests/security/ha-s01/local`](../../security/ha-s01/local/README.md)).

```
./tests/e2e/hu-w1.2/run.sh
```

Los secretos (PostgreSQL y usuarios de Moto) son aleatorios, se generan en cada corrida y no se imprimen. Al terminar, con éxito o no,
el entorno se elimina (`down -v`). El script termina con un código distinto de cero si alguna comprobación falla. Puertos publicados
en `127.0.0.1` y configurables: `AUDIT_PORT` (8290), `ORIGINATION_PORT` (8291), `TOXIPROXY_PORT` (8494) y `MOTO_PORT` (5120).
