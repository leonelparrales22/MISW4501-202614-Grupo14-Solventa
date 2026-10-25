# E2E — HU-W4.2-02 · Revocar un consentimiento de Open Finance desde el portal de cumplimiento

Escenario de punta a punta con los servicios **reales** `origination` y `audit` en Docker: la base PostgreSQL de cada uno, Redis, el stub
`openfinance` detrás de Toxiproxy (un *proxy* para los perfiles de originación y otro para los avisos de `audit`) y Moto como Cognito de
las rutas humanas. Es una copia de [`tests/e2e/hu-m1.3`](../hu-m1.3/README.md) **sin LocalStack**: el vínculo entre la cuenta del asegurado
y su documento se siembra con `psql` en `holder_link` (solo datos de prueba, como hace el contrato), así que no hace falta SNS ni SQS.
Originación usa el `audit` real (`AUDIT_URL`) y el plazo de los avisos se acorta a 5 s (`REVOCATION_DEADLINE_SECONDS`, 300 s en AWS).

```
./tests/e2e/hu-w4.2/run.sh
```

| Escenario | Qué comprueba |
|---|---|
| S1 | CA-1: `GET …/revocation` de un consentimiento activo entrega el titular (nombre y documento enmascarado), el alcance, la entidad y `revocable=true`; un `POST` sin motivo, sin referencia y sin la casilla del rol (o con `role_confirmed=false`) responde `400` con los **tres** campos a la vez y no deja ninguna fila en `consent_revocation` ni en `consent_event` |
| S2 | CA-2 bloqueo: antes, `POST /v1/offers` responde `201` y consulta el stub; después de revocar desde el portal, la verificación dice `active=false` y `POST /v1/offers` responde `422 consent_required` con **cero conexiones nuevas** al *proxy* de Open Finance y cero consultas de perfil en el stub (originación se reinicia antes para no reutilizar conexiones) |
| S3 | CA-2 aviso: la entidad confirma dentro del plazo (`confirmed_at − effective_at` ≤ 5 s aquí, 300 s en AWS): `revoked` / `completed`; el stub registra un aviso, una sola vez, con la clave `revocation-<consentimiento>-<entidad>` |
| S4 | CA-3: con el *proxy* de avisos deshabilitado, al vencer el plazo queda `partially_revoked` / `partial` con la entidad `not_confirmed` y la hora en que se agotó su ventana, y el trabajador escribe `revocation_not_confirmed`; se habilita el *proxy* y `POST …/revocation-retries` responde `202` solo con esa entidad (sigue parcial con la entidad `pending`, porque un reintento mantiene la revocación parcial hasta que confirme); la entidad confirma y pasa a `revoked` / `completed` **sin avisos de más** en el stub; un segundo reintento (con la ventana abierta o ya revocado) responde `409 nothing_to_retry` y uno sobre un consentimiento sin revocación, `409 consent_not_revoked` |
| S5 | CA-4: otra clave sobre el revocado, `409 consent_already_revoked`; la misma, `200` con el mismo comprobante; uno revocado por el titular desde la app (`mobile_app`, actor `holder`, sin motivo) también es `409` para el portal; un vencido (sembrado con `granted_at` de hace 13 meses) no es revocable y responde `409 consent_not_active` sin dejar filas |
| S6 | CA-5: al recibir el `201` la fila de `consent_revocation` ya tiene `actor_type=operations`, el `sub`, canal, motivo, referencia, dispositivo (*user agent*), `confirmation_method=role_attestation`, el alcance y la hora del servidor (`requested_at` ≤ `effective_at`, a menos de 2 s); los eventos del consentimiento llevan actor, motivo y referencia; `UPDATE`/`DELETE`/`TRUNCATE` de `consent_revocation`, `consent_event` y `revocation_notice_step` rechazados por `psql`; el asegurado ve `revoked` por `compliance_portal` en `GET /v1/consents/{id}` y descarga el PDF (`REV-…`) |
| S7 | CA-6: sin token `401`; `advisor` e `insured` `403` en las tres rutas y **ninguna fila nueva** en `consent_revocation`, `consent_event` ni `revocation_notice_step`; el consentimiento sigue activo |
| S8 | Minimización: ni las respuestas ni los logs de `audit` ni los del stub contienen el documento completo, la referencia ni un token |

Requiere Docker (compose v2), `curl`, `jq` y la AWS CLI (solo para sembrar Moto con `tests/security/ha-s01/local/seed.sh`). Los secretos
(PostgreSQL, usuarios de Moto) son aleatorios y no se imprimen; el entorno se elimina al terminar (`down -v`) y el script termina con un
código distinto de cero si algo falla. No usa `date -d`, así que corre igual en macOS y en Linux. Puertos (solo `127.0.0.1`, para no chocar
con `hu-w1.2` ni `hu-m1.3`): `audit` 8490, `origination` 8491, Moto 5140, stub `openfinance` 9490 (su `/debug/consent-revocations`) y la API
de Toxiproxy 8495.

`CAPTURE_DIR=<carpeta> ./tests/e2e/hu-w4.2/run.sh` guarda además, como JSON, las respuestas que ilustran
[`docs/contratos/HU-W4.2-02-api.md`](../../../docs/contratos/HU-W4.2-02-api.md) (con identificadores y horas de esa corrida).

Las pruebas por componente están en `services/audit/tests/integration/compliance_revocation_test.go`; el contrato de `audit`, con el token de
`operations`, en [`services/audit/tests/contract`](../../../services/audit/tests/contract/README.md).
