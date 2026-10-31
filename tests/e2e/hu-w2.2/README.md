# Pruebas de punta a punta · HU-W2.2-01 (cálculo automático de prioridad del siniestro)

Levanta en Docker el servicio `claims` real con su PostgreSQL y el barrido del triage (cada 2 s en esta prueba; 10 s en AWS),
el stub del core de pólizas (`stubs/policy-core`) detrás de Toxiproxy y Moto como Cognito y como el S3 de los soportes. Radica
los siniestros por la ruta real del portal (HU-W2.1-01) y recorre cada criterio de aceptación por HTTP, como lo harían el portal
del analista y el del asegurado. Es una copia de [`hu-w2.1`](../hu-w2.1/README.md), que no se modifica, con otros puertos.

```bash
./tests/e2e/hu-w2.2/run.sh
```

## Escenarios

| # | Qué verifica | CA | Cómo |
|---|---|---|---|
| 1 | Un siniestro radicado recibe su nivel en ≤ 60 s, con la causa `entered_evaluation`, el actor `system` y la fecha y hora; queda una fila en `claim_triage` | CA-1 | Radicación web de daño por agua con monto y sondeo de `GET /v1/claims/{id}/triage` como `operations`; conteo con `psql` |
| 2 | Daño por agua de $ 4,8 M → Triage 2 (media, baja); hurto sin monto → Triage 1; colisión de $ 40 M → Triage 1; cristales de $ 900.000 → Triage 3 | CA-2 | Una radicación por caso. El stub cubre cristales solo para algunos asegurados: si la póliza no lo cubre, una colisión de $ 900.000 se corrige a `glass` con `psql` |
| 3 | Un monto nuevo recalcula el nivel con su causa y el nivel anterior; pasar el caso a `in_evaluation` no recalcula nada | CA-3 | `UPDATE claim` con `psql`, como lo harán las historias del sprint 3 y HU-W2.2-03 |
| 4 | El analista lee nivel, severidad y banda de una página de casos en una llamada y el detalle; el asegurado, solo el nivel y los días; otro asegurado y el asesor reciben `403`, sin token `401` | CA-4 | `GET /v1/claims` del asegurado + `GET /v1/claims/triage?claim_id=…` como `operations`; vistas por rol |
| 5 | Cada nivel trae su objetivo: Triage 1 inmediata, 2 y 3 en 5 y 10 días hábiles | CA-5 | `GET /v1/claims/triage-policy` y el `target` de cada fila del lote |
| 6 | Un caso que llega a la base con `claims` detenido (como uno radicado justo antes de un despliegue) recibe su nivel al volver el servicio; con el core de pólizas caído un cambio de monto se recalcula igual | Resiliencia (D3, S4) | `docker compose stop claims`, `INSERT` con `psql` y `start`; Toxiproxy deshabilita `policycore` |
| 7 | `claim_triage` no se modifica (`UPDATE`/`DELETE`/`TRUNCATE` fallan); ni la vista del asegurado ni los logs traen el monto; un monto que no es entero es un `422` del campo | Inmutabilidad, minimización, D12 | `psql`, `docker compose logs` y una radicación con `"4.8 millones"` |

El script termina con un código distinto de cero si algún chequeo falla y siempre elimina el entorno (`down -v`).

## Requisitos

Docker (compose v2), `curl`, `jq` y la AWS CLI (solo para sembrar Moto y crear el segundo asegurado). Las contraseñas son
aleatorias, se generan en cada corrida y no se imprimen. Puertos publicados solo en `127.0.0.1`: `claims` 8298, stub del core
de pólizas 9161, API de Toxiproxy 8496 y Moto 5124 (variables `CLAIMS_PORT`, `POLICY_CORE_PORT`, `TOXIPROXY_PORT` y
`MOTO_PORT`). El API Gateway no se emula: las rutas del triage cuelgan de `/v1/claims*`, que ya publica.
