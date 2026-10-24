# Contrato — `audit`

Schemathesis contra el servicio **real** en Docker (PostgreSQL y Moto como Cognito), con todas las comprobaciones
(`--checks=all`) sobre `services/audit/api/openapi.yaml` (0.3.0).

```
./services/audit/tests/contract/run.sh
```

Corre tres veces: con un token de `operations` (todas las rutas, incluida la evidencia, alcanzada con el enlace de OpenAPI desde la
respuesta de `POST /v1/audit/consents`; las del asegurado le responden `403`), con el token del asegurado de Moto sobre
`GET /v1/consents*` (HU-M1.3-01: antes se siembran, solo como datos de prueba, su vínculo `sub` ↔ documento en `holder_link` por SQL y
un consentimiento por la ruta del aliado) y sin credenciales (las rutas que no las necesitan). Requiere Docker (compose v2), `curl`, `jq` y la
AWS CLI (solo para sembrar Moto). Los secretos son aleatorios y no se imprimen; el entorno se elimina al terminar y el script termina
con un código distinto de cero si algo falla.

Las pruebas por criterio de aceptación están en [`../integration`](../integration/consent_test.go) y la integración con `origination`
en [`tests/e2e/hu-w1.2`](../../../../tests/e2e/hu-w1.2/README.md).
