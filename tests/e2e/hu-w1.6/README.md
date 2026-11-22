# E2E — HU-W1.6 · Cancelaciones y reembolsos

Pruebas de punta a punta de la cancelación de una póliza a solicitud del asegurado con reembolso prorrateado (HU-W1.6-01). La regla del
cálculo y el contrato para el front están en [`docs/contratos/HU-W1.6-01-api.md`](../../../docs/contratos/HU-W1.6-01-api.md). Las
pruebas por criterio, sin AWS, están en `services/origination/tests/integration/cancellation_test.go` (PostgreSQL real con
Testcontainers) y el contrato en `services/origination/tests/contract/run-cancellations.sh`.

## Recorrido local (`cancellation.sh`)

Levanta con Docker el ensayo de [`tests/e2e/hu-w1.4`](../hu-w1.4/README.md) (`audit`, `origination` y `claims` reales con su
PostgreSQL, Redis, los *stubs*, la pasarela de pagos y el correo simulados y Moto como Cognito) con un proyecto y puertos propios
(`audit` 8790, `origination` 8791, `claims` 8792, Toxiproxy 8794, Moto 5390, correo 9391, pasarela 9394). Compra las pólizas por el
recorrido real con la tarjeta 4111, así que el mes en curso lo paga el cobro de la emisión.

```
./tests/e2e/hu-w1.6/cancellation.sh
```

| Escenario | Criterio | Qué comprueba |
|---|---|---|
| C1 | CA-1 | La cotización de una póliza comprada hoy: mes 1 pagado por la emisión, un día usado del periodo, el reembolso `pagado × N / D` truncado al peso, el porcentaje con un decimal, la tarjeta de la emisión y el final del día en Colombia; cotizar no cambia nada |
| C2 | CA-2 | Con otro monto `409 refund_changed` con la cotización; con el monto visto `201` con la póliza cancelada y el reembolso `REF-<año>-<número>` con monto, fecha y medio; la pasarela lo reembolsa una vez (`completed`, `GET /debug/charges`); la billetera dice «Cancelada» sin renovación; no se cancela dos veces |
| C3 | Reglas | La póliza cancelada no admite solicitudes de modificación (`409 policy_not_in_force`) y el asesor no la cuenta como cancelada por no pago |
| C4 | D-5 (b) | Con la pasarela detenida la cancelación no la espera y el reembolso queda `in_process`; al volver (con la memoria vacía) pasa a `completed` con un solo reembolso real |
| C5 | Roles | Sin token `401`; asesor y operaciones `403`; una póliza que no es del asegurado `404` |

Requiere Docker (compose v2), `curl`, `jq` y la AWS CLI (solo contra Moto). Los secretos son aleatorios y no se imprimen; el entorno se
elimina al terminar y el guion termina con un código distinto de cero si algo falla.

## Verificación en la nube (`cancellation-cloud.sh verify`)

Para la ventana de staging, por el portal de CloudFront y solo con los usuarios permanentes de `bootstrap/identity`
(`asegurado@example.com`, `asesor@example.com`, `operaciones@example.com`). **Compra su propia póliza** por el recorrido real
(consentimiento por la ruta del aliado, `POST /v1/offers`, aceptación y la tarjeta 4111 en `/pasarela/tokens`), así que **C1…C3** del
guion único de siembra (`tests/e2e/hu-w1.5/assisted-renewal-cloud.sh --seed`) quedan para la demostración: C1 para CA-1 y CA-2, C2 para
la pasarela caída en W7 y C3 para la E2E de la nube del front.

```
PORTAL_URL=https://<distribución>.cloudfront.net SEED_USER_PASSWORD=… \
OUTPUT_FILE=<carpeta de evidencia>/cancellation-cloud.txt \
  ./tests/e2e/hu-w1.6/cancellation-cloud.sh verify
```

| Bloque | Qué comprueba |
|---|---|
| CA-1 | La cotización de la póliza comprada: mes 1 por la emisión, un día usado, el reembolso truncado al peso, el porcentaje, la tarjeta y el final del día; cotizar no cambia nada |
| Reglas | Un motivo fuera de la lista `422 invalid_request` (`reason=invalid`); otro monto `409 refund_changed` con la cotización |
| CA-2 | El monto visto `201` con `REF-<año>-<número>`, monto, medio y fecha; el detalle dice «Cancelada» y el reembolso termina `completed` (espera hasta `REFUND_WAIT_SECONDS`, 120 s); la billetera la lista cancelada sin renovación; no se cancela dos veces; no admite modificaciones; el indicador de no pago del asesor no cambia |
| Roles | Sin token `401`; asesor y operaciones `403` |
| D-2 | Una póliza del núcleo, tomada de la billetera (`source: policy_core`) → `404 policy_not_found`; si no hay ninguna, `SKIP` |

- Solo llama a `InitiateAuth` del *user pool* (con el cliente y la región del `config.json` del portal) y al portal. La contraseña y los
  tokens nunca se imprimen. Ninguna llamada `GET` lleva cuerpo (CloudFront responde su propio `403` a un `GET` con cuerpo).
- Requiere `curl` y `jq`. Termina con un código distinto de cero si alguna comprobación falla.
