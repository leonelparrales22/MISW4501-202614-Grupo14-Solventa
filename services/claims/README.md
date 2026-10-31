# Servicio `claims` (siniestros)

Recibe, prioriza, evalúa y resuelve siniestros, incluidos los paramétricos que se pagan automáticamente.

**Estado:** `GET /health` y **HU-M2.2-01:** `POST /v1/claims` (radicación del siniestro desde el asistente guiado del móvil), con autenticación Cognito (`pkg/security`) y los casos en **PostgreSQL** (`DATABASE_URL`); sin base de datos quedan en memoria. **HU-M2.1-01:** `POST /v1/claims/evidence/upload-url` (URL firmada con la que el teléfono sube la evidencia directamente al *bucket* `EVIDENCE_BUCKET`), y la evidencia asociada al caso con su ubicación, su instante de captura y su resumen de integridad. **HU-W2.1-01:** radicación del siniestro con soportes desde el portal web (`GET /v1/claims/filing-options`, `POST /v1/claims/supports` y su confirmación, `POST /v1/claims/web-filings`), validada contra el core de pólizas (`POLICY_CORE_URL`) y con los soportes subidos directo al *bucket* con URL firmada, y lectura de los casos propios de los dos canales (`GET /v1/claims`, `GET /v1/claims/{claim_id}`, que incluye la evidencia del móvil, y el comprobante en PDF `GET /v1/claims/{claim_id}/filing-receipt`). **HU-W2.2-01:** triage automático: cada caso que entra a
evaluación recibe su nivel (Triage 1, 2 o 3) por severidad y monto estimado en ≤ 60 s, se recalcula solo cuando cambia esa
información y se lee por `GET /v1/claims/{claim_id}/triage` (analista y asegurado), `GET /v1/claims/triage` (una página de casos)
y `GET /v1/claims/triage-policy`; la radicación web gana el monto estimado opcional. El resto de módulos sigue reservado.

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `filing` | Radicación y seguimiento del siniestro (web y móvil). **HU-M2.2-01:** `Service` radica desde el asistente del móvil. **HU-W2.1-01:** `WebService` ofrece las pólizas activas del asegurado con los tipos y soportes de cada una y radica desde el portal web validando la póliza con el core de pólizas; `SupportService` declara cada soporte, firma su subida y confirma lo que llegó; `QueryService` lista los casos propios de los dos canales, entrega el detalle y el comprobante en PDF |
| `triage` | Priorización automática por severidad, monto y tiempo de respuesta comprometido. **HU-W2.2-01:** `Service` calcula el nivel de cada caso con la política `triage-policy-v1`, lo registra con su causa y lee el triage de un caso, de una página de casos y la política vigente; `Worker` barre la base cada `TRIAGE_SWEEP_INTERVAL_SECONDS` buscando los casos sin cálculo o cuyos datos cambiaron |
| `assessors` | Prestadores (peritos y talleres) y su asignación al caso: elige el más cercano por distancia y especialidad, ampliando el radio si no hay disponibles (HU-W2.3-01). Seguimiento de la intervención y cierre del caso (HU-W2.3-02). Es el único dueño del cálculo de distancia |
| `assistance` | Asistencia en sitio: recibe las solicitudes del móvil con la ubicación del asegurado (HU-M2.3-01), las muestra en la bandeja de entrantes y gestiona su despacho y cierre con motivo (HU-W2.3-03). Para elegir prestador **llama a `assessors`**; no calcula distancias |
| `parametric` | Pago automático cuando un evento externo (retraso de vuelo, clima) cruza un umbral |
| `evidence` | **HU-M2.1-01:** emite la URL firmada con la que el teléfono sube cada foto o video al *bucket* de evidencia; el binario nunca cruza el servicio y lo que queda con el caso es su llave, su ubicación, su instante de captura y su resumen de integridad |

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

La evidencia no viaja en este cuerpo: ya está en el repositorio cuando se radica, y aquí van sus llaves y sus
metadatos. Entre 4 y 6 fotografías y hasta un video.

## Evidencia del siniestro (HU-M2.1-01)

`POST /v1/claims/evidence/upload-url` emite la URL con la que el teléfono sube una evidencia. Contrato:
[`docs/contratos/HU-M2.1-01-api.md`](../../docs/contratos/HU-M2.1-01-api.md).

- **El archivo nunca cruza el servicio.** Va del teléfono al *bucket*; el servicio solo firma la subida y
  después comprueba lo que llegó. Así cabe un video de 30 segundos, que no pasaría por el API Gateway.
- **La firma fija el objeto:** tipo, tamaño y resumen SHA-256. S3 rechaza un cuerpo cuyo resumen no coincida,
  de modo que la integridad no depende de que el cliente diga la verdad. La URL vence a los 10 minutos.
- **La llave sale del `sub` y del resumen:** `evidence/<sub>/<sha256>.jpg`. Un insured no puede escribir en el
  directorio de otro, ni radicar con una evidencia ajena aunque conozca su llave.
- **Al radicar se comprueba cada llave** contra el repositorio: existe, es suya y su tamaño es el
  declarado; el resumen se compara cuando el almacenamiento calcula el suyo. Si no cuadra, `422` con el
  código `not_stored` y el caso no se crea.
- **La ubicación poco precisa no se descarta:** la evidencia queda marcada con `location_trusted` en falso
  cuando la precisión supera los 50 m.

## Radicación web con soportes (HU-W2.1-01)

El portal web radica con su propia ruta y la **misma** tabla `claim`, la misma secuencia `SIN-` y las mismas claves de
idempotencia que el móvil (una clave usada en un canal no sirve en el otro). Contrato:
[`docs/contratos/HU-W2.1-01-api.md`](../../docs/contratos/HU-W2.1-01-api.md).

```
GET  /v1/claims/filing-options ─▶ pólizas activas del asegurado (core de pólizas) con los tipos que cubren y sus soportes   [CA-1]
POST /v1/claims/supports ───────▶ declara un archivo (tipo, tamaño, sha256) y devuelve la URL firmada de subida              [CA-5]
PUT  <upload_url> ──────────────▶ el navegador sube el archivo directo al bucket (no pasa por claims ni por el API Gateway)
POST /v1/claims/supports/{id}/confirmation ─▶ comprueba tamaño, resumen y los primeros bytes: JPG, PNG o PDF por su contenido [CA-5]
POST /v1/claims/web-filings ────▶ 1. formulario y soportes (todo lo que falta, a la vez)                                    [CA-2]
                                  2. póliza propia, activa, que cubre el tipo, hecho dentro de la vigencia                  [CA-3, CA-6]
                                  3. caso `active` + soportes, en una transacción                                           [CA-4]
GET  /v1/claims[/{id}[/filing-receipt]] ─▶ casos propios de los dos canales, detalle y comprobante en PDF                   [CA-4, CA-6]
```

- **Pólizas:** vienen del **core de pólizas** (externo; en local y en pruebas, `stubs/policy-core`), por `pkg/adapters/policies`
  (corte de 700 ms y circuit breaker) y el puente `internal/adapters/policies`. El titular es el `sub` del token. Si el core no
  responde **no se radica** (`503 policy_directory_unavailable`); un reintento con la misma clave sí devuelve el caso ya creado.
  Una póliza de otro asegurado responde igual que una inexistente (`policy_not_found`). Sin `POLICY_CORE_URL` o sin `EVIDENCE_BUCKET` estas
  rutas no se montan.
- **Catálogo `claim-catalog-v1`** (`internal/domain/catalog.go`): Autos (`collision`, `theft`, `third_party_damage`, `glass`,
  `other`) y Hogar (`water_damage`) con sus soportes obligatorios, mínimos, máximos y formatos. Viaje es paramétrico y no se
  radica. **Pendiente de validación con negocio**; si cambia, cambia la versión.
- **Copia de la póliza:** el caso guarda la póliza tal como el core la devolvió (`policy_snapshot`) y la versión del catálogo, para
  reconstruir la validación aunque la póliza cambie después.
- **Soportes:** un soporte es de quien lo declara y se asocia a **un solo** caso (`claim_support_once`). Los soportes de un caso
  radicado no se modifican ni se borran (disparadores sobre `claim_support`). Como máximo 20 por caso y 50 declarados sin radicar
  por asegurado (`429`).
- **Subida directa a S3 (el mismo *bucket* de la evidencia, prefijo `supports/<sub>/<support_id>`):** el archivo nunca cruza el
  servicio, así que el límite de 10 MB del API Gateway no lo alcanza y no hay plazos de lectura especiales. La firma fija el
  tipo, el tamaño y el SHA-256 del objeto. El tipo y el tamaño se rechazan al declarar (`415`, `413`); el **contenido** se comprueba
  al confirmar, leyendo solo los primeros bytes del objeto: un ejecutable renombrado `.pdf` es `415`, un archivo que no llegó
  es `422 support_not_uploaded` y uno que no es el declarado, `422 support_mismatch`. Solo un soporte **confirmado** se puede
  radicar (si no, `supports.<código>: not_found`). `support_file` conserva los metadatos (`object_key`, `confirmed_at`).
  El *bucket* acepta `PUT` y `GET` desde el origen del portal (CORS, `portal_base_url`).
- **Evidencia del móvil en el detalle:** `GET /v1/claims/{id}` de un caso radicado desde la app lista su evidencia (tipo, ubicación,
  instante, precisión y, si se puede firmar, una URL de lectura de 10 minutos); en un caso web `evidence` va vacío.
- **Un solo escritor:** `postgres.ClaimRecordStore` y `memory.CaseStore` son el único almacén de casos de los dos canales
  (radicación móvil con evidencia, radicación web con soportes y póliza): un `INSERT` de `claim`, una numeración, una huella por
  canal. La clave de idempotencia de un canal no sirve en el otro.
- **Titular (S1):** el core de pólizas identifica al titular por el `sub` del token, así que hoy no hay cruce con el documento del
  cliente. Cuando exista un core real que identifique por documento, el cruce se decide aparte (llamada a `origination` o evento);
  no es parte de esta historia.
- **Comprobante:** PDF generado al vuelo desde el caso (`internal/adapters/pdf`), con fechas en hora de Colombia (UTC−5) y
  determinista: dos descargas dan el mismo archivo.
- **Estado:** el caso nace `active` («Radicado» en el portal). La revisión del asesor (HU-W2.1-03), la devolución (HU-W2.1-04) y
  el triage (HU-W2.2-01) no están en esta historia.

### Adaptadores (`internal/adapters`)

| Adaptador | Qué hace |
|---|---|
| `http` | Router `chi`. `WithFilings` (HU-M2.2-01), `WithClaimReads` y `WithWebFilings` (HU-W2.1-01): `security.Authenticate` + `RequireRole(insured)`, `Cache-Control: no-store`. `WithTriage` (HU-W2.2-01): el triage de un caso para `operations` e `insured` (vista por rol), el lote y la política solo para `operations` |
| `postgres` | `ClaimRecordStore`: único almacén de casos de los dos canales (radicación móvil con evidencia y web con soportes y póliza, transaccional) y `SupportStore`: metadatos de los soportes (HU-W2.1-01). `TriageStore` (HU-W2.2-01): los cálculos del nivel en `claim_triage`, sobre el mismo *pool* que los casos (`TriageStoreOf`). Migraciones embebidas con *advisory lock* |
| `memory` | Sin `DATABASE_URL`: `CaseStore` sirve a los dos canales y a los metadatos de los soportes, y `TriageStore` guarda los cálculos del nivel sobre esos casos (HU-W2.2-01); todo se pierde al reiniciar |
| `policies` | Puente de `pkg/adapters/policies` al puerto `PolicyDirectory`; deja fuera las pólizas mal descritas y traduce cualquier falla a `ErrPolicyDirectoryUnavailable` |
| `pdf` | Comprobante de radicación con `go-pdf/fpdf` |
| `s3` | `ObjectStore` sobre el *bucket* de evidencia: firma las subidas (evidencia del móvil y soportes del portal), comprueba lo que llegó, lee los primeros bytes de un objeto y firma las lecturas |

## Triage automático (HU-W2.2-01)

Cada siniestro que entra a evaluación recibe un nivel de prioridad sin intervención manual, y el nivel se recalcula cuando cambia
la información de la que salió. Contrato: [`docs/contratos/HU-W2.2-01-api.md`](../../docs/contratos/HU-W2.2-01-api.md).

```
claims (cada tarea de ECS)
  triage.Worker ──cada 10 s──▶ casos en active o in_evaluation sin cálculo o con monto, tipo o versión de la política distintos
                              └─▶ Service.Assess: nivel = severidad × banda de monto ──▶ INSERT en claim_triage (con su causa)   [CA-1…CA-3]
GET /v1/claims/{id}/triage ──▶ operations: nivel, severidad, banda, monto, objetivo e historial · insured dueño: nivel y días    [CA-4]
GET /v1/claims/triage?claim_id=… ─▶ el nivel de una página de casos en una llamada (operations)                                   [CA-4, CA-5]
GET /v1/claims/triage-policy ────▶ severidad por tipo, umbrales y objetivo de cada nivel (operations)                              [CA-5]
```

- **Regla:** Triage 1 si la severidad es alta **o** el monto supera el umbral alto; Triage 3 si la severidad es baja **y** el monto
  está por debajo del umbral bajo; Triage 2 en los demás casos. Sin monto, la banda es `unknown` y no lleva a 1 ni a 3.
- **Política `triage-policy-v1`** (`internal/domain/triage_policy.go`), **provisional, pendiente de validación con negocio**:
  hurto y daño a terceros `high`; colisión, otro y daño por agua `medium`; cristales `low`; umbrales de $ 5.000.000 y
  $ 30.000.000; objetivos: Triage 1 atención inmediata (2 días hábiles de resolución para el cliente), Triage 2 cinco y Triage 3
  diez días hábiles. Si cambia, cambia la versión y los casos vigentes se recalculan solos con la causa `policy_updated`.
- **Por barrido, no por llamada:** el triage no lo invoca la radicación ni la evaluación. Lee la base, así que cualquier historia
  que cambie el monto, el tipo o la cobertura de un caso (HU-W2.1-03, HU-W2.1-04, HU-W2.3-02) obtiene el recálculo sin llamar a
  nada. El primer barrido corre al arrancar, por eso un caso que llegó con el servicio detenido se prioriza apenas vuelve. Dos
  tareas no duplican un cálculo: cada uno ocupa el lugar `seq` siguiente del caso (llave primaria) y la que pierde no escribe.
- **Estados que se priorizan:** `active` (mientras no exista la revisión del asesor, radicado es entrar a evaluación; HU-W2.1-03
  lo quitará) e `in_evaluation` (HU-W2.2-03). Fuera de ellos no se recalcula, pero el último nivel sigue visible.
- **Causas:** `entered_evaluation`, `estimated_amount_changed`, `incident_type_changed`, `coverage_changed`,
  `provider_report_received` (reservada para HU-W2.3-02) y `policy_updated`. Un cambio que no mueve el nivel también se registra.
- **El triage no consulta al core de pólizas:** la cobertura es la del tipo de siniestro y la línea sale del catálogo.
- **Monto estimado:** lo declara el asegurado, opcional, en `POST /v1/claims/web-filings` (`estimated_amount_minor`, centavos de
  COP, 1–10¹³) y queda en `claim.estimated_amount_minor`; entra en la huella de idempotencia solo si viene. El móvil radica sin
  monto. El detalle del caso lo devuelve y omite el campo si no se declaró.
- **Cómo lo usa otra historia:** la vista `claim_current_triage` (una fila por caso con su último cálculo) y el puerto
  `ports.TriageStore.Current` dan el nivel vigente sin conocer el historial. El orden de la cola por prioridad es HU-W2.2-02.
- **No publica ni consume eventos.**

## Requisitos de calidad que gobiernan el servicio

- Consulta de estado del siniestro p95 ≤ 2 s en la app móvil (EC-L02): caché de lectura en Redis con corte duro de 900 ms; ante timeout responde la última versión válida con `stale=true`.
- Absorber ≥ 1.000.000 de eventos paramétricos en 10 min con contrapresión y sin pérdida (HA-05).
- Despliegue: 2 tareas Fargate, una por zona de disponibilidad.

## Datos y eventos

- **BD propia:** PostgreSQL (siniestros): `claim` (con `channel`, `policy_snapshot` y `catalog_version` desde HU-W2.1-01 y `estimated_amount_minor` desde HU-W2.2-01), `claim_evidence`, `support_file` (metadatos; el archivo vive en S3), `claim_support` y `claim_triage` (HU-W2.2-01: cada cálculo del nivel, solo se agregan filas) con su vista `claim_current_triage`. Caché: Redis (`internal/adapters/redis`). Objetos: S3 (`internal/adapters/s3`).
- **Publica:** `ClaimApproved` (dispara notificación y pago paramétrico). Ver [catálogo de eventos](../../docs/events/README.md).
- **Depende de:** `pkg/adapters` (`iot`, `payments`, `policies` desde HU-W2.1-01), `pkg/outbox`. HU-W2.1-01 no publica ni consume eventos.

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
| `AUTH_ISSUER`, `AUTH_CLIENT_IDS` | La radicación no se monta: sin *user pool* no hay de quién es el caso (desde HU-W2.1-01: tampoco el listado, el detalle ni el comprobante) |
| `DATABASE_URL` | Los casos quedan en memoria y se pierden al reiniciar (desde HU-W2.1-01: también los soportes) |
| `EVIDENCE_BUCKET` | HU-M2.1-01: la URL firmada no se monta y una radicación que declare evidencia se rechaza con `not_stored`; **HU-W2.1-01:** tampoco se monta la radicación web (los soportes no tendrían dónde ir); el resto del servicio sigue respondiendo. Un *bucket* configurado que no se alcanza sí impide arrancar: es un error de configuración |
| `EVIDENCE_ENDPOINT_URL` | Solo para una corrida local contra un emulador de S3 (LocalStack); vacía en AWS, donde se usa el servicio real y el rol de la tarea |
| `AWS_REGION` | Región del SDK de AWS (la plataforma la fija, `us-east-2`) |
| `POLICY_CORE_URL` | HU-W2.1-01: las opciones, la declaración y confirmación de soportes y la radicación web no se montan (sin el core no se sabe qué pólizas son del asegurado) |
| `POLICY_CORE_TIMEOUT_MS` | 700 ms por llamada al core de pólizas |
| `TRIAGE_SWEEP_INTERVAL_SECONDS` | HU-W2.2-01: el barrido del triage corre cada 10 s. Entre 1 y 30; otro valor impide arrancar. Solo se acorta en local y en la e2e |

El `AUTH_ISSUER` del *user pool* permanente es
`https://cognito-idp.us-east-2.amazonaws.com/us-east-2_EWwg8NL2C`. Las migraciones se aplican solas al arrancar.

Con la radicación web (HU-W2.1-01), en local:

```
go run ./stubs/policy-core/cmd/policy-core &          # core de pólizas en el 9060
POLICY_CORE_URL=http://localhost:9060 AUTH_ISSUER=... AUTH_CLIENT_IDS=... go run ./services/claims/cmd/claims
```

Pruebas: `REQUIRE_DOCKER=1 go test ./services/claims/...` (PostgreSQL con Testcontainers; sin la variable, las pruebas nuevas
que necesitan Docker se saltan). `tests/integration/web_filing_test.go` ejercita cada criterio de HU-W2.1-01 por HTTP;
[`tests/contract`](tests/contract/README.md) corre Schemathesis contra el servicio real y
[`tests/e2e/hu-w2.1`](../../tests/e2e/hu-w2.1/README.md) recorre los criterios de punta a punta con el core detrás de Toxiproxy.
**HU-W2.2-01:** `tests/integration/triage_test.go` ejercita cada criterio del triage por HTTP con el barrido corriendo; el contrato
también pasa como `operations`; [`tests/e2e/hu-w2.2`](../../tests/e2e/hu-w2.2/README.md) lo recorre de punta a punta.
