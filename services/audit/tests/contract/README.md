# Contrato — `audit`

Schemathesis contra el servicio **real** en Docker (PostgreSQL y Moto como Cognito), con todas las comprobaciones
(`--checks=all`) sobre `services/audit/api/openapi.yaml` (0.6.0).

```
./services/audit/tests/contract/run.sh
```

Corre tres veces: con un token de `operations` (todas las rutas, incluida la evidencia, alcanzada con el enlace de OpenAPI desde la
respuesta de `POST /v1/audit/consents`; las del asegurado le responden `403`), con el token del asegurado de Moto sobre
`/v1/consents*` (HU-M1.3-01: antes se siembran, solo como datos de prueba, su vínculo `sub` ↔ documento en `holder_link` por SQL y
un consentimiento por la ruta del aliado; desde HU-M1.3-02 incluye `POST /v1/consents/{consent_id}/revocations` y
`GET /v1/consents/{consent_id}/revocation-receipt`, cuyas cabeceras `Idempotency-Key` y `Accept-Language` llevan patrón en el
contrato para que solo se generen valores que HTTP admite) y sin credenciales (las rutas que no las necesitan). Desde HU-W4.2-01 la
corrida de `operations` cubre también el listado `GET /v1/audit/consents` (filtros, páginas e indicadores) y el estado ampliado del
portal; la corrida sin credenciales sigue incluyendo la ruta exacta `/v1/audit/consents` por su `POST` del aliado y excluye su `GET`
(`--exclude-operation-id=listComplianceConsents`): sin token, Schemathesis le envía cabeceras `Authorization` que el servidor HTTP de
Go rechaza con un `400` en texto plano antes de llegar al servicio. El `401` de ese `GET` lo comprueban las pruebas HTTP, la de
integración y [`tests/e2e/hu-w4.2/list.sh`](../../../../tests/e2e/hu-w4.2/README.md). Requiere Docker (compose v2), `curl`, `jq` y la
AWS CLI (solo para sembrar Moto). Los secretos son aleatorios y no se imprimen; el entorno se elimina al terminar y el script termina
con un código distinto de cero si algo falla.

Las pruebas por criterio de aceptación están en [`../integration`](../integration/consent_test.go) y la integración con `origination`
en [`tests/e2e/hu-w1.2`](../../../../tests/e2e/hu-w1.2/README.md).
