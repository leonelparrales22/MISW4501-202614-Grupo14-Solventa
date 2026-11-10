# Contrato — `origination`

Schemathesis contra el servicio **real** en Docker, con todas las comprobaciones (`--checks=all`) sobre las rutas
`/v1/risk-profiles` y `/v1/offers` de `services/origination/api/openapi.yaml` (0.10.0).

```
./services/origination/tests/contract/run.sh
```

Corre tres veces: dos para las rutas de perfil (HU-W1.2-01) y una para las de ofertas (HU-W1.2-03, `run-offers.sh`):

1. `origination` **sin** `AUDIT_URL`: el llamador afirma `consent_active`, así que se alcanzan las respuestas `200`.
2. `origination` **con** `AUDIT_URL` apuntando al `audit` real: el consentimiento se verifica allí y, como los clientes generados no
   tienen consentimiento, se alcanzan las respuestas `422`.
3. Las rutas de **ofertas** (HU-W1.2-03), con [`run-offers.sh`](run-offers.sh) y [`docker-compose.offers.yml`](docker-compose.offers.yml):
   `origination` con su propio PostgreSQL, el `audit` real y Moto como Cognito. `POST /v1/offers` (canal del aliado) va sin token;
   `GET /v1/offers/{offer_id}` y `POST /v1/offers/{offer_id}/renewal` se prueban con el token de un asegurado y, la lectura, también con el de
   un asesor. Los clientes generados no tienen consentimiento (`422`) y los ids generados no existen (`404`). Se puede correr solo:
   `./services/origination/tests/contract/run-offers.sh`. Además requiere `jq` y la AWS CLI (solo para sembrar Moto).

**Renovación asistida (HU-W1.5-03), aparte:** [`run-assisted-renewals.sh`](run-assisted-renewals.sh) con
[`docker-compose.assisted-renewals.yml`](docker-compose.assisted-renewals.yml): `origination` con su PostgreSQL, la pasarela de pagos
(la renovación se monta con el cobro), el `audit` real y Moto como Cognito. Schemathesis recorre con el token del asesor las cuatro rutas
`/v1/policies/{policy_id}/assisted-renewal*` (contrato 0.15.0): los ids generados no existen (`404`) y los cuerpos malformados reciben
`400`/`422`; al final comprueba que el asegurado recibe `403` en las cuatro. Se corre solo:
`./services/origination/tests/contract/run-assisted-renewals.sh` (también requiere `jq` y la AWS CLI). Las pruebas por criterio están en
[`../integration/assisted_renewal_test.go`](../integration/assisted_renewal_test.go) y el recorrido de punta a punta en
[`tests/e2e/hu-w1.5`](../../../../tests/e2e/hu-w1.5/README.md).

Requiere Docker (compose v2) y `curl`. Los secretos (PostgreSQL y usuarios de Moto) son aleatorios y no se imprimen; el entorno se elimina al terminar y el
script termina con un código distinto de cero si algo falla. Las pruebas por criterio de aceptación están en [`../integration/consent_test.go`](../integration/consent_test.go) y [`../integration/offer_test.go`](../integration/offer_test.go), y la
integración con `audit` en [`tests/e2e/hu-w1.2`](../../../../tests/e2e/hu-w1.2/README.md).
