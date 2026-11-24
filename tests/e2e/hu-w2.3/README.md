# Pruebas de punta a punta · HU-W2.3-01 (asignación del prestador más cercano)

Levanta en Docker:

- el servicio `claims` real, con su PostgreSQL, el registro de prestadores que siembra (`0009_provider_seed`) y su *worker*.
  El *worker* busca de nuevo los casos pendientes cada 10 s en esta prueba (5 min en AWS) y envía los avisos;
- el stub de correo (`stubs/email`) detrás de Toxiproxy, que recibe el aviso al prestador;
- el stub del core de pólizas (`stubs/policy-core`) para la radicación web;
- Moto como Cognito y como el S3 de los soportes y la evidencia.

Los casos se radican por las rutas reales: el móvil con su evidencia (HU-M2.1-01 y HU-M2.2-01) y el portal con sus soportes
(HU-W2.1-01). Llegan a evaluación con la aprobación real del asesor (HU-W2.1-03). Después recorre cada criterio de aceptación
por HTTP, como lo haría el portal del analista de operaciones.

Es una copia de [`hu-w2.2`](../hu-w2.2/README.md), que no se modifica, con otros puertos y el correo en lugar del core de
pólizas detrás de Toxiproxy.

```bash
./tests/e2e/hu-w2.3/run.sh
```

## Escenarios

| # | Qué verifica | CA | Cómo |
|---|---|---|---|
| 1 | Un choque radicado desde el teléfono queda `not_applicable` hasta que el asesor lo aprueba; después queda `ready` con la ubicación de la captura confiable más precisa (4,6771 N · 74,0483 O, 8 m), y no la de una captura de 3 m marcada como no confiable. El analista no puede reemplazarla (`409 location_from_app`). Un daño por agua del portal queda `location_missing`: la búsqueda se rechaza (`422`), una dirección de 4 caracteres es un campo inválido y el analista registra dirección y pin (`source: analyst`, sin precisión, con quien la registró) | CA-1 | `POST /v1/claims` con 4 fotos subidas a Moto; `POST /v1/claims/web-filings`; `POST …/review-approval` como asesor; `GET`/`POST …/incident-location` como `operations` |
| 2 | Solo aparecen los tres plomeros del registro, del más cercano al más lejano (Humedades y Acabados, solo impermeabilización, no aparece). Servicios Hogar Norte (2,3 km), disponible solo mañana, aparece `unavailable` y no se sugiere (P8). Se sugiere Reparaciones Chapinero (3,8 km); Técnicos Unidos (6,1 km) queda `out_of_radius` | CA-2, CA-3 | `POST …/availability` con `unavailable_until` de mañana y `POST …/provider-searches` |
| 3 | Con Reparaciones Chapinero también `unavailable`, la búsqueda encuentra a Técnicos Unidos en 10 km y registra los intentos 5 → 0 y 10 → 1. Asignar con la búsqueda anterior es `409 search_outdated` | CA-4 | Segunda búsqueda y `POST …/assignment` con el `search_seq` anterior |
| 4 | La asignación guarda prestador, hora, quién asignó, distancia, búsqueda y ubicación compartida. El estado del caso no cambia (`in_evaluation`) y el prestador queda `busy` con ese caso. Le llega **un** correo con la llave `asignacion-<id>`, el radicado, el tipo, las coordenadas, la dirección y el mapa, sin datos del asegurado; el aviso queda `sent`. Una segunda asignación es `409`. Con el correo cortado por Toxiproxy, la asignación del choque responde `201`, el aviso queda `pending` con el intento fallido registrado y sale una sola vez al restaurar el correo, con la precisión de la captura | CA-5 | `POST …/assignment`; `GET /messages?to=…` del stub de correo; Toxiproxy deshabilita y vuelve a habilitar el *proxy* `email` |
| 5 | Sin plomeros disponibles en 20 km, el segundo daño por agua queda `exhausted` con los 3 intentos y el motivo, en estado `pending_assignment` y sin sugerencia. Asignar uno ocupado es `422 provider_not_eligible`. Cuando Servicios Hogar Norte vuelve a `available`, la búsqueda automática lo propone en ≤ 40 s (`trigger: automatic_retry`) sin asignarlo | CA-6 | `GET /v1/claims/assignments?claim_id=…` muestra el motivo; sondeo de `GET …/assignment` |
| 6 | `operations` registra un prestador (id `prv-` + 12 hexadecimales, `available`, sin teléfono) y cambia su perfil y su disponibilidad. El registro filtra por especialidad y disponibilidad, y un perfil inválido nombra todos sus campos a la vez. Además se verifican la política vigente (`assignment-policy-v1`, 5/10/20 km, 10 s) y la cola por lote (`assigned`, `assigned`, `proposed`, `not_found`). El asegurado y el asesor reciben `403` en el registro, la asignación y la búsqueda; sin token, `401` | Administración, S14 | `POST /v1/claims/providers`, `…/profile`, `…/availability`; `GET /v1/claims/assignment-policy` |
| 7 | `UPDATE`, `DELETE` y `TRUNCATE` fallan en `claim_incident_location`, `claim_provider_search`, `claim_provider_search_attempt` y `claim_assignment`, y el historial sigue ahí. Los logs de `claims` no traen correos ni teléfonos de prestadores ni coordenadas, y sí las búsquedas y las asignaciones | Inmutabilidad, minimización | `psql` y `docker compose logs` |

El script termina con un código distinto de cero si algún chequeo falla y siempre elimina el entorno (`down -v`).

## Requisitos

- Docker (compose v2), `curl`, `jq` y la AWS CLI (solo para sembrar Moto).
- Las contraseñas son aleatorias, se generan en cada corrida y no se imprimen.
- Puertos publicados solo en `127.0.0.1`. Se cambian con las variables indicadas.

  | Servicio | Puerto | Variable |
  |---|---|---|
  | `claims` | 8299 | `CLAIMS_PORT` |
  | Stub del core de pólizas | 9162 | `POLICY_CORE_PORT` |
  | Stub de correo | 9133 | `EMAIL_PORT` |
  | API de Toxiproxy | 8499 | `TOXIPROXY_PORT` |
  | Moto | 5126 | `MOTO_PORT` |

- El API Gateway no se emula: las rutas de la asignación y del registro cuelgan de `/v1/claims*`, que ya publica.
