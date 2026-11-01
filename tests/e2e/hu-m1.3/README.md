# E2E — HU-M1.3-01 · Consulta y HU-M1.3-02 · Revocación de consentimientos en movilidad

Escenario de punta a punta con los servicios **reales** `origination` y `audit` en Docker: la base PostgreSQL de cada uno,
LocalStack 4.14.0 con el tópico SNS de originación y la cola SQS de `audit` con su DLQ (las crea `localstack/init-messaging.sh` igual
que `infra/terraform/modules/messaging`: 5 recepciones, 30 s de visibilidad, entrega *raw*) y Moto como Cognito del registro y de las
rutas humanas. Prueba el vínculo `sub` ↔ documento que viaja por el *outbox* de originación (`CustomerLinked`) y la consulta del
asegurado `GET /v1/consents*`. **HU-M1.3-02:** el stub `openfinance` detrás de Toxiproxy (un *proxy* para los perfiles de originación y
otro para los avisos de `audit`), originación con `AUDIT_URL` al `audit` real y el plazo de los avisos acortado a 5 s
(`REVOCATION_DEADLINE_SECONDS`); la entidad `aliado-mudo` nunca responde para mostrar la revocación parcial.

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
| R1 | HU-M1.3-02 CA-4: A revoca en línea (`201`); al recibir la respuesta la fila de `consent_revocation` ya existe con todos los campos (titular, alcance, `requested_at` del dispositivo, `effective_at`, canal, dispositivo, método, clave) y los eventos `revocation_requested` y `revoked` |
| R2 | CA-3 bloqueo inmediato: antes, `POST /v1/offers` responde `201` y consulta el stub; después de revocar, la verificación dice `active=false` y `POST /v1/offers` responde `422 consent_required` con **cero conexiones nuevas** al *proxy* de Open Finance y cero consultas de perfil en el stub (originación se reinicia antes para no reutilizar conexiones) |
| R3 | CA-3 aviso: la entidad confirma dentro del plazo (`confirmed_at − effective_at` ≤ 5 s aquí, 300 s en AWS), el historial lo muestra y el stub la registra una vez con la clave del aviso |
| R4 | CA-6 idempotencia: la misma clave responde `200` con el mismo comprobante; otra clave, `409 consent_already_revoked`; sigue habiendo una sola revocación |
| R5 | CA-6: un vencido responde `409 consent_not_active` y no es revocable |
| R6 | Aislamiento: B sobre el de A `404`, `operations` `403`, sin token `401`; el consentimiento de A sigue activo |
| R7 | Revocación parcial: la entidad `aliado-mudo` nunca confirma; al vencer el plazo, `partially_revoked`, `notice_status = partial`, el evento `revocation_not_confirmed` y la verificación también inactiva |
| R8 | CA-4: `UPDATE`/`DELETE`/`TRUNCATE` de `consent_revocation`, `revocation_notice` y `revocation_notice_step` rechazados por `psql` |
| R9 | CA-4 comprobante: `200 application/pdf` que empieza por `%PDF-`; B `404`; un activo no tiene comprobante |
| R10 | Latencia del `POST` de revocación (solo humo, no evidencia): menos de 5 s |
| R11 | CA-5: una revocación confirmada sin conexión hace 2 h conserva su `requested_at`; `effective_at` es la hora del envío |
| `nube.sh` | Las comprobaciones de API de la ventana en la nube, ensayadas contra este entorno (desde HU-M1.3-02, también el comprobante, el `409` de una segunda revocación y el aislamiento sobre los revocados de A) |

Requiere Docker (compose v2), `curl`, `jq` y la AWS CLI (para sembrar Moto con `tests/security/ha-s01/local/seed.sh` y publicar en
el LocalStack local, siempre con `--endpoint-url` a `127.0.0.1`). Los secretos (PostgreSQL, usuarios de Moto, contraseñas de los
asegurados) son aleatorios y no se imprimen; el entorno se elimina al terminar (`down -v`) y el script termina con un código distinto de
cero si algo falla. Puertos (solo `127.0.0.1`, para no chocar con `hu-w1.2`): `audit` 8390, `origination` 8391, LocalStack 4576,
Moto 5130, stub `openfinance` 9390 (su `/debug/consent-revocations`) y la API de Toxiproxy 8394.

## Otras herramientas

| Archivo | Para qué |
|---|---|
| `nube.sh` | CA-1, CA-2, CA-3, CA-4 (métodos no publicados) y CA-6 contra `API_URL` (el *stage* del API Gateway en la ventana conjunta), y de HU-M1.3-02 sobre los consentimientos que A revocó desde la app: el comprobante en PDF, el `409` de otra revocación y el aislamiento (con `REVOKE_CONSENT_ID`, además revoca uno por API). Desde HU-M1.3-02 el gateway deja pasar `POST` bajo `/v1/consents/{id}`, así que un `POST` sobre el consentimiento llega a `audit` y responde `405`. Los tokens salen de `InitiateAuth` con las contraseñas de `USER_A_PASSWORD`, `USER_B_PASSWORD` y `OPS_PASSWORD` (opcional), que nunca se imprimen. Variables: `API_URL`, `COGNITO_CLIENT_ID`, `USER_A_EMAIL`, `USER_B_EMAIL`, `OPS_EMAIL`, `AWS_REGION` (`us-east-2`) y `COGNITO_ENDPOINT` (solo local, para el ensayo con Moto) |
| `app-local.sh` | *Backend* local para el ensayo guiado en el emulador: PostgreSQL, LocalStack y `audit` con los `AUTH_*` del pool permanente (valores públicos de `app/build.gradle.kts` del móvil); publica un `CustomerLinked` para el `SUBJECT` de `asegurado@example.com` (se lee con `aws cognito-idp admin-get-user`, de solo lectura) y el documento `1020304050`, crea dos consentimientos y la semilla, e imprime cómo construir el APK con `URL_API_LOCAL`. Desde HU-M1.3-02 levanta también el stub
`openfinance` y Toxiproxy (dependencias de `audit`), así que las revocaciones del ensayo se avisan y confirman. `--another` agrega un consentimiento (CA-5); se detiene con `docker compose -f tests/e2e/hu-m1.3/docker-compose.yml down -v` |
| `seed-history.sql` | Un consentimiento vencido y uno revocado con su comprobante para un documento (`psql -v holder_document=… -v suffix=…`); solo inserciones, como las tablas *append-only* de `audit` exigen |

Las pruebas por componente están en `services/origination/tests/integration` (`outbox_test.go`, `customer_linked_test.go`) y
`services/audit/tests/integration` (`holder_consents_test.go`, `customer_linked_test.go`); el contrato de `audit`, con un token de
asegurado, en [`services/audit/tests/contract`](../../../services/audit/tests/contract/README.md).
