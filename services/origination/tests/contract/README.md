# Contrato — `origination`

Schemathesis contra el servicio **real** en Docker, con todas las comprobaciones (`--checks=all`) sobre las rutas
`/v1/risk-profiles` de `services/origination/api/openapi.yaml` (0.9.0).

```
./services/origination/tests/contract/run.sh
```

Corre dos veces (HU-W1.2-01):

1. `origination` **sin** `AUDIT_URL`: el llamador afirma `consent_active`, así que se alcanzan las respuestas `200`.
2. `origination` **con** `AUDIT_URL` apuntando al `audit` real: el consentimiento se verifica allí y, como los clientes generados no
   tienen consentimiento, se alcanzan las respuestas `422`.

Requiere Docker (compose v2) y `curl`. El secreto de PostgreSQL es aleatorio y no se imprime; el entorno se elimina al terminar y el
script termina con un código distinto de cero si algo falla. Las rutas de ofertas de HU-W1.2-03 se agregan a esta carpeta con esa
historia. Las pruebas por criterio de aceptación están en [`../integration/consent_test.go`](../integration/consent_test.go) y la
integración con `audit` en [`tests/e2e/hu-w1.2`](../../../../tests/e2e/hu-w1.2/README.md).
