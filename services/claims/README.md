# Servicio `claims` (siniestros)

Recibe, prioriza, evalúa y resuelve siniestros, incluidos los paramétricos que se pagan automáticamente.

**Estado:** `GET /health` y **HU-M2.2-01:** `POST /v1/claims` (radicación del siniestro desde el asistente guiado del móvil), con autenticación Cognito (`pkg/security`) y los casos en **PostgreSQL** (`DATABASE_URL`); sin base de datos quedan en memoria. El resto de módulos sigue reservado.

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `filing` | Radicación y seguimiento del siniestro (web y móvil) |
| `triage` | Priorización automática por severidad, monto y tiempo de respuesta comprometido |
| `assessors` | Prestadores (peritos y talleres) y su asignación al caso: elige el más cercano por distancia y especialidad, ampliando el radio si no hay disponibles (HU-W2.3-01). Seguimiento de la intervención y cierre del caso (HU-W2.3-02). Es el único dueño del cálculo de distancia |
| `assistance` | Asistencia en sitio: recibe las solicitudes del móvil con la ubicación del asegurado (HU-M2.3-01), las muestra en la bandeja de entrantes y gestiona su despacho y cierre con motivo (HU-W2.3-03). Para elegir prestador **llama a `assessors`**; no calcula distancias |
| `parametric` | Pago automático cuando un evento externo (retraso de vuelo, clima) cruza un umbral |
| `evidence` | Referencia a fotos/video geoetiquetados; el binario vive en S3 (`s3_key`, tipo, geolocalización) |

## Radicación del siniestro (HU-M2.2-01)

`POST /v1/claims` crea el caso con su número de radicado. Contrato:
[`docs/contratos/HU-M2.2-01-api.md`](../../docs/contratos/HU-M2.2-01-api.md).

- **El caso es de quien lo radica.** El `sub` del token es el dueño; el cuerpo no lo dice. Solo el rol `insured`
  radica.
- **Idempotente por `Idempotency-Key`.** El móvil envía el identificador de su borrador. Repetir la llamada con
  la misma clave devuelve el mismo caso; esa clave con otra radicación responde `422 idempotency_key_reused`.
  La huella de lo declarado es lo que distingue un reintento de otra radicación.
- **Número de radicado:** `SIN-<año>-<cinco dígitos>`, de una secuencia de la base de datos. Puede dejar
  huecos; solo tiene que ser único.
- **Tipos de incidente:** `collision`, `theft`, `third_party_damage` y `other`, los cuatro del prototipo.
- **La declaración bajo gravedad de juramento** cierra la radicación: sin ella la solicitud se rechaza.
- Las respuestas de las preguntas que dependen del tipo viajan en `details` y se guardan como JSON.

Los soportes y la evidencia multimedia **no entran por esta ruta**: son HU-M2.1-01, con el binario en S3.

## Requisitos de calidad que gobiernan el servicio

- Consulta de estado del siniestro p95 ≤ 2 s en la app móvil (EC-L02): caché de lectura en Redis con corte duro de 900 ms; ante timeout responde la última versión válida con `stale=true`.
- Absorber ≥ 1.000.000 de eventos paramétricos en 10 min con contrapresión y sin pérdida (HA-05).
- Despliegue: 2 tareas Fargate, una por zona de disponibilidad.

## Datos y eventos

- **BD propia:** PostgreSQL (siniestros). Caché: Redis (`internal/adapters/redis`). Objetos: S3 (`internal/adapters/s3`).
- **Publica:** `ClaimApproved` (dispara notificación y pago paramétrico). Ver [catálogo de eventos](../../docs/events/README.md).
- **Depende de:** `pkg/adapters` (`iot`, `payments`), `pkg/outbox`.

## Historias asociadas

Épica web 2 (F2.1–F2.3), épica móvil 2 (F2.1–F2.4), HA-L02 y HA-05. Experimento: [EC-L02](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/EC-L02-—-Consulta-ágil-del-estado-del-siniestro).

## Desarrollo

```
go run ./services/claims/cmd/claims
```

Variables que el servicio lee:

| Variable | Sin ella |
|---|---|
| `PORT` | Escucha en el 8080 |
| `AUTH_ISSUER`, `AUTH_CLIENT_IDS` | La radicación no se monta: sin *user pool* no hay de quién es el caso |
| `DATABASE_URL` | Los casos quedan en memoria y se pierden al reiniciar |

El `AUTH_ISSUER` del *user pool* permanente es
`https://cognito-idp.us-east-2.amazonaws.com/us-east-2_EWwg8NL2C`. Las migraciones se aplican solas al arrancar.
