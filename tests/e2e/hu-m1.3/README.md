# E2E — HU-M1.3-01 · Consulta de consentimientos en movilidad

Escenario de punta a punta con los servicios **reales** `origination` y `audit` en Docker: la base PostgreSQL de cada uno,
LocalStack 4.14.0 con el tópico SNS de originación y la cola SQS de `audit` con su DLQ (las crea `localstack/init-messaging.sh` igual
que `infra/terraform/modules/messaging`: 5 recepciones, 30 s de visibilidad, entrega *raw*) y Moto como Cognito del registro y de las
rutas humanas. Prueba el vínculo `sub` ↔ documento que viaja por el *outbox* de originación (`CustomerLinked`) y la consulta del
asegurado `GET /v1/consents*`.

```
./tests/e2e/hu-m1.3/run.sh
```

| Escenario | Qué comprueba |
|---|---|
| E1 | A y B se registran (`POST /v1/registrations`) y su vínculo llega a `audit` por *outbox* → SNS → SQS en ≤ 15 s (`GET /v1/consents` deja de responder `403 holder_not_linked`) |
| E2 | CA-1: los consentimientos de A (dos por la ruta del aliado y la semilla `seed-history.sql`) agrupados: vigentes primero, entidad «Banco Aliado», fechas y estado; `Cache-Control: no-store` |
| E3 | CA-2: el detalle (identificador, entidad, alcance, finalidad, canal, vigencia, estado e historial con fecha y hora) |
| E4 | CA-3: el revocado con su comprobante y la hora en que terminó, el vencido terminado en `expires_at`, el historial al final |
| E5 | CA-4: `POST/PUT/PATCH/DELETE` → 405; `UPDATE`/`DELETE`/`TRUNCATE` de `consent_event`, `holder_link` y `consent_record` rechazados por `psql` |
| E6 | CA-6: B solo ve lo suyo; el id de A es 404 para B; sin token 401; `operations` 403; un asegurado de Moto que nunca se registró, `403 holder_not_linked` |
| E7 | Consumidor idempotente: el mismo evento otra vez y uno en conflicto (mismo `sub`, otro documento) publicados con `aws --endpoint-url` al LocalStack local: ninguna fila nueva, se conserva el primer documento, el conflicto queda en el log y nada va a la DLQ |
| E8 | `audit` caído: C se registra, el evento se publica y espera en la cola; al volver `audit`, C queda vinculado |
| E9 | *Outbox* durable: con LocalStack caído D se registra y el evento queda pendiente con intentos y su último error; al volver LocalStack (el *init* recrea los recursos) se publica y D queda vinculado |
| E10 | Minimización: ni las respuestas de `/v1/consents` ni los logs de `audit` contienen un documento; cada vínculo deja la línea «holder linked» |
| `nube.sh` | Las comprobaciones de API de la ventana en la nube, ensayadas contra este entorno |

Requiere Docker (compose v2), `curl`, `jq` y la AWS CLI (para sembrar Moto con `tests/security/ha-s01/local/seed.sh` y publicar en
el LocalStack local, siempre con `--endpoint-url` a `127.0.0.1`). Los secretos (PostgreSQL, usuarios de Moto, contraseñas de los
asegurados) son aleatorios y no se imprimen; el entorno se elimina al terminar (`down -v`) y el script termina con un código distinto de
cero si algo falla. Puertos (solo `127.0.0.1`, para no chocar con `hu-w1.2`): `audit` 8390, `origination` 8391, LocalStack 4576,
Moto 5130.

## Otras herramientas

| Archivo | Para qué |
|---|---|
| `nube.sh` | CA-1, CA-2, CA-3, CA-4 (métodos no publicados) y CA-6 contra `API_URL` (el *stage* del API Gateway en la ventana conjunta). Los tokens salen de `InitiateAuth` con las contraseñas de `USER_A_PASSWORD`, `USER_B_PASSWORD` y `OPS_PASSWORD` (opcional), que nunca se imprimen. Variables: `API_URL`, `COGNITO_CLIENT_ID`, `USER_A_EMAIL`, `USER_B_EMAIL`, `OPS_EMAIL`, `AWS_REGION` (`us-east-2`) y `COGNITO_ENDPOINT` (solo local, para el ensayo con Moto) |
| `app-local.sh` | *Backend* local para el ensayo guiado en el emulador: PostgreSQL, LocalStack y `audit` con los `AUTH_*` del pool permanente (valores públicos de `app/build.gradle.kts` del móvil); publica un `CustomerLinked` para el `SUBJECT` de `asegurado@example.com` (se lee con `aws cognito-idp admin-get-user`, de solo lectura) y el documento `1020304050`, crea dos consentimientos y la semilla, e imprime cómo construir el APK con `URL_API_LOCAL`. `--another` agrega un consentimiento (CA-5); se detiene con `docker compose -f tests/e2e/hu-m1.3/docker-compose.yml down -v` |
| `seed-history.sql` | Un consentimiento vencido y uno revocado con su comprobante para un documento (`psql -v holder_document=… -v suffix=…`); solo inserciones, como las tablas *append-only* de `audit` exigen |

Las pruebas por componente están en `services/origination/tests/integration` (`outbox_test.go`, `customer_linked_test.go`) y
`services/audit/tests/integration` (`holder_consents_test.go`, `customer_linked_test.go`); el contrato de `audit`, con un token de
asegurado, en [`services/audit/tests/contract`](../../../services/audit/tests/contract/README.md).
