# Pruebas de punta a punta · HU-W2.1-01 (radicación de siniestro con soportes)

Levanta en Docker el servicio `claims` real con su PostgreSQL, el stub del core de pólizas (`stubs/policy-core`) detrás de
Toxiproxy y Moto como Cognito y como el S3 de los soportes, y recorre cada criterio de aceptación por HTTP, igual que lo haría el portal web.

```bash
./tests/e2e/hu-w2.1/run.sh
```

## Escenarios

| # | Qué verifica | CA | Cómo |
|---|---|---|---|
| 1 | Las opciones de radicación son solo las pólizas activas del asegurado (sin Viaje ni la vencida) con los soportes obligatorios de cada tipo | CA-1 | `GET /v1/claims/filing-options` comparado con lo que el stub tiene para ese `sub` |
| 2 | Sin el informe de tránsito no se radica, se nombra el soporte que falta y no se guarda nada | CA-2 | `422 validation_failed` con `supports.traffic_report: missing` y conteo en `psql` |
| 3 | Hecho anterior al inicio de la póliza, tipo no cubierto y póliza vencida se rechazan con su motivo | CA-3 | `422 policy_not_eligible` con `occurred_outside_validity`, `incident_not_covered` y `policy_not_active` |
| 4 | Con todo completo se crea el caso `active` con su radicado; el detalle y el PDF lo traen; un reintento devuelve el mismo caso | CA-4 | `201`, `200` con la misma clave, `GET` del detalle y del comprobante |
| 5 | Un archivo de 10 MiB + 1 byte se rechaza al declararlo (`413`), un ejecutable renombrado `.pdf` al confirmarlo (`415`), uno que no llegó a S3 da `422 support_not_uploaded` y otro asegurado no puede confirmarlo (`404`); 10 MiB exactos sí se aceptan; los soportes subidos antes siguen sirviendo | CA-5 | Declaración, `PUT` a la URL firmada (el S3 de Moto) y confirmación; luego la radicación con los válidos |
| 6 | La póliza de otro asegurado no existe para este, el caso de otro asegurado da `403` y el caso aparece en el listado propio | CA-6 | Segundo asegurado creado en Moto con la AWS CLI |
| 7 | Con el core de pólizas caído la radicación y las opciones responden `503` y no se guarda nada | Resiliencia (D1) | Toxiproxy deshabilita el proxy `policycore` |
| 8 | Tras reiniciar `claims` el caso, sus soportes y su comprobante siguen; `UPDATE` sobre `claim_support` falla | Persistencia | `docker compose restart claims` y `psql` |

El script termina con un código distinto de cero si algún chequeo falla y siempre elimina el entorno (`down -v`).

## Requisitos

Docker (compose v2), `curl`, `jq` y la AWS CLI (solo para sembrar Moto y crear el segundo asegurado). Las contraseñas son
aleatorias, se generan en cada corrida y no se imprimen. Puertos publicados solo en `127.0.0.1`: `claims` 8297, stub del core
de pólizas 9160, API de Toxiproxy 8495 y Moto 5123 (variables `CLAIMS_PORT`, `POLICY_CORE_PORT`, `TOXIPROXY_PORT` y
`MOTO_PORT`). El API Gateway no se emula; los soportes ya no pasan por él (van directo a S3 con URL firmada), así que su límite de 10 MB no aplica a ellos.
