# Servicio `claims` (siniestros)

Recibe, prioriza, evalúa y resuelve siniestros, incluidos los paramétricos que se pagan automáticamente.

**Estado:** `GET /health` y **HU-M2.2-01:** `POST /v1/claims` (radicación del siniestro desde el asistente guiado del móvil), con autenticación Cognito (`pkg/security`) y los casos en **PostgreSQL** (`DATABASE_URL`); sin base de datos quedan en memoria. **HU-M2.1-01:** `POST /v1/claims/evidence/upload-url` (URL firmada con la que el teléfono sube la evidencia directamente al *bucket* `EVIDENCE_BUCKET`), y la evidencia asociada al caso con su ubicación, su instante de captura y su resumen de integridad. **HU-W2.1-01:** radicación del siniestro con soportes desde el portal web (`GET /v1/claims/filing-options`, `POST /v1/claims/supports` y su confirmación, `POST /v1/claims/web-filings`), validada contra el core de pólizas (`POLICY_CORE_URL`) y con los soportes subidos directo al *bucket* con URL firmada, y lectura de los casos propios de los dos canales (`GET /v1/claims`, `GET /v1/claims/{claim_id}`, que incluye la evidencia del móvil, y el comprobante en PDF `GET /v1/claims/{claim_id}/filing-receipt`). **HU-W2.2-01:** triage automático: cada caso que entra a
evaluación recibe su nivel (Triage 1, 2 o 3) por severidad y monto estimado en ≤ 60 s, se recalcula solo cuando cambia esa
información y se lee por `GET /v1/claims/{claim_id}/triage` (analista y asegurado), `GET /v1/claims/triage` (una página de casos)
y `GET /v1/claims/triage-policy`; la radicación web gana el monto estimado opcional. **HU-W2.3-01:** asignación del prestador
más cercano a un caso en evaluación: ubicación del incidente (la de la app o la que registra el analista), búsqueda por
especialidad con radios de 5, 10 y 20 km, asignación confirmada por el analista con aviso por correo al prestador, búsqueda
automática de los casos pendientes de asignación y el registro de prestadores que administra operaciones
(`/v1/claims/{claim_id}/assignment`, `…/incident-location`, `…/provider-searches`, `/v1/claims/assignments`,
`/v1/claims/assignment-policy` y `/v1/claims/providers*`). **HA-L02:** caché de lectura en Redis delante de la lista, el detalle y
el historial del asegurado (`GET /v1/claims`, `GET /v1/claims/{claim_id}` y `GET /v1/claims/{claim_id}/history`): cada lectura
espera 900 ms a la base y, si no llega, responde la última lectura guardada con `stale: true` y la hora en que se leyó. El resto
de módulos sigue reservado.

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `filing` | Radicación y seguimiento del siniestro (web y móvil). **HU-M2.2-01:** `Service` radica desde el asistente del móvil. **HU-W2.1-01:** `WebService` ofrece las pólizas activas del asegurado con los tipos y soportes de cada una y radica desde el portal web validando la póliza con el core de pólizas; `SupportService` declara cada soporte, firma su subida y confirma lo que llegó; `QueryService` lista los casos propios de los dos canales, entrega el detalle y el comprobante en PDF |
| `triage` | Priorización automática por severidad, monto y tiempo de respuesta comprometido. **HU-W2.2-01:** `Service` calcula el nivel de cada caso con la política `triage-policy-v1`, lo registra con su causa y lee el triage de un caso, de una página de casos y la política vigente; `Worker` barre la base cada `TRIAGE_SWEEP_INTERVAL_SECONDS` buscando los casos sin cálculo o cuyos datos cambiaron |
| `assessors` | Prestadores (peritos y talleres) y su asignación al caso: elige el más cercano por distancia y especialidad, ampliando el radio si no hay disponibles (HU-W2.3-01). Seguimiento de la intervención y cierre del caso (HU-W2.3-02). Es el único dueño del cálculo de distancia. **HU-W2.3-01:** `Service` registra la ubicación del incidente, busca, asigna y lee la asignación de un caso, de una página de casos y la política vigente; `ProviderService` administra el registro de prestadores; `Worker` repite la búsqueda de los casos pendientes cada `ASSIGNMENT_RETRY_INTERVAL_SECONDS` y envía los avisos a los prestadores; `Suggest` es la búsqueda sin estado que reutilizará `assistance` (HU-W2.3-03) |
| `assistance` | Asistencia en sitio: recibe las solicitudes del móvil con la ubicación del asegurado (HU-M2.3-01), las muestra en la bandeja de entrantes y gestiona su despacho y cierre con motivo (HU-W2.3-03). Para elegir prestador **llama a `assessors`**; no calcula distancias |
| `parametric` | Pago automático cuando un evento externo (retraso de vuelo, clima) cruza un umbral |
| `readcache` | **HA-L02:** `Claims` e `History` envuelven la consulta de los casos propios (`filing.QueryService`) y el historial de estados (`review.Service`) con un presupuesto de 900 ms hacia la base y la caché de lectura como respaldo. El comprobante y los enlaces de la evidencia no pasan por la caché |
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
- **Monto estimado, opcional:** `estimated_amount_minor` en centavos de COP, con las reglas de la web (HU-W2.2-01), en la misma
  columna `claim.estimated_amount_minor`. Con él el triage calcula la banda de monto de los casos del móvil. Solo cuenta en la
  huella si se declaró.

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
- **Estado:** el caso nace `active` («Radicado» en el portal): espera la revisión del asesor (HU-W2.1-03). La devolución
  (HU-W2.1-04) y el triage (HU-W2.2-01) no están en esta historia.

### Adaptadores (`internal/adapters`)

| Adaptador | Qué hace |
|---|---|
| `http` | Router `chi`. `WithFilings` (HU-M2.2-01), `WithClaimReads` y `WithWebFilings` (HU-W2.1-01): `security.Authenticate` + `RequireRole(insured)`, `Cache-Control: no-store`. `WithTriage` (HU-W2.2-01): el triage de un caso para `operations` e `insured` (vista por rol), el lote y la política solo para `operations`. `WithAssignment` y `WithProviders` (HU-W2.3-01): la asignación y el registro de prestadores, solo para `operations` |
| `postgres` | `ClaimRecordStore`: único almacén de casos de los dos canales (radicación móvil con evidencia y web con soportes y póliza, transaccional) y `SupportStore`: metadatos de los soportes (HU-W2.1-01). `TriageStore` (HU-W2.2-01): los cálculos del nivel en `claim_triage`, sobre el mismo *pool* que los casos (`TriageStoreOf`). `AssignmentStore` y `ProviderStore` (HU-W2.3-01): ubicaciones, búsquedas, asignaciones y avisos, y el registro de prestadores, sobre el mismo *pool* (`AssignmentStoreOf`, `ProviderStoreOf`). Migraciones embebidas con *advisory lock* |
| `memory` | Sin `DATABASE_URL`: `CaseStore` sirve a los dos canales y a los metadatos de los soportes, y `TriageStore` guarda los cálculos del nivel sobre esos casos (HU-W2.2-01); `AssignmentStore` y `ProviderStore` (HU-W2.3-01) hacen lo mismo con la asignación y con los prestadores de referencia de la semilla (`ReferenceProviders`); todo se pierde al reiniciar |
| `policies` | Puente de `pkg/adapters/policies` al puerto `PolicyDirectory`; deja fuera las pólizas mal descritas y traduce cualquier falla a `ErrPolicyDirectoryUnavailable` |
| `pdf` | Comprobante de radicación con `go-pdf/fpdf` |
| `redis` | **HA-L02:** `ReadCache`, la caché de lectura de las consultas sobre ElastiCache: claves `claims:read:v1:<alcance>:…` con TTL de 5 minutos. Una falla de Redis se registra y se lee como si no hubiera nada guardado; nunca hace fallar una consulta |
| `s3` | `ObjectStore` sobre el *bucket* de evidencia: firma las subidas (evidencia del móvil y soportes del portal), comprueba lo que llegó, lee los primeros bytes de un objeto y firma las lecturas |

## Triage automático (HU-W2.2-01)

Cada siniestro que entra a evaluación recibe un nivel de prioridad sin intervención manual, y el nivel se recalcula cuando cambia
la información de la que salió. Contrato: [`docs/contratos/HU-W2.2-01-api.md`](../../docs/contratos/HU-W2.2-01-api.md).

```
claims (cada tarea de ECS)
  triage.Worker ──cada 10 s──▶ casos en in_evaluation sin cálculo o con monto, tipo o versión de la política distintos
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
- **Estados que se priorizan:** solo `in_evaluation`: desde HU-W2.1-03 un caso entra a evaluación cuando el asesor aprueba
  la radicación (fin del supuesto S5). Fuera de él no se recalcula, pero el último nivel sigue visible.
- **Causas:** `entered_evaluation`, `estimated_amount_changed`, `incident_type_changed`, `coverage_changed`,
  `provider_report_received` (reservada para HU-W2.3-02) y `policy_updated`. Un cambio que no mueve el nivel también se registra.
- **El triage no consulta al core de pólizas:** la cobertura es la del tipo de siniestro y la línea sale del catálogo.
- **Monto estimado:** lo declara el asegurado, opcional, en `POST /v1/claims/web-filings` (`estimated_amount_minor`, centavos de
  COP, 1–10¹³) y queda en `claim.estimated_amount_minor`; entra en la huella de idempotencia solo si viene. El móvil radica sin
  monto. El detalle del caso lo devuelve y omite el campo si no se declaró.
- **Cómo lo usa otra historia:** la vista `claim_current_triage` (una fila por caso con su último cálculo) y el puerto
  `ports.TriageStore.Current` dan el nivel vigente sin conocer el historial. El orden de la cola por prioridad es HU-W2.2-02.
- **No publica ni consume eventos.**

## Evaluación, aprobación y pago (HU-W2.2-03)

El analista de operaciones abre un caso en evaluación con todo lo que necesita para decidir, lo aprueba con el monto a
indemnizar —y el pago sale solo— o lo rechaza con su motivo. Contrato:
[`docs/contratos/HU-W2.2-03-api.md`](../../docs/contratos/HU-W2.2-03-api.md).

```
GET  /v1/claims/evaluations ─────────▶ casos en in_evaluation ordenados por prioridad (HU-W2.2-02), con su monto     [CA-1]
GET  /v1/claims/{id}/evaluation ─────▶ caso, soportes y evidencia firmados, triage, póliza y cobertura de hoy
                                       (core), asegurado con la cuenta enmascarada y la decisión                    [CA-1, CA-5]
POST /v1/claims/{id}/approval ───────▶ costo bruto ≤ valor asegurado; paga bruto − deducible ──▶ approved          [CA-2]
        decision.Worker ─────────────▶ pasarela POST /payouts, llave indemnizacion-<id> ──▶ paid
                                       rechazo o 3 intentos sin respuesta ──▶ payment_exception                      [CA-4]
POST /v1/claims/{id}/rejection ──────▶ motivo obligatorio ──▶ rejected; el worker envía el correo con el motivo      [CA-3]
GET  /v1/claims/payment-exceptions ──▶ los casos en payment_exception (O6b)
POST /v1/claims/{id}/payout-retry ───▶ destino corregido o el mismo, misma llave ──▶ approved y otra vez al worker    [CA-4]
```

- **Solo `operations`** decide y lee (HA-S01): asegurado y asesor reciben `403` [CA-6]. Cada decisión guarda quién la tomó
  (el correo del analista), cuándo, el monto o el motivo [CA-5]; el asegurado ve el estado nuevo en su listado y puede
  filtrar por él.
- **Un desembolso por aprobación:** el pago usa el mecanismo de HA-D02 (`pkg/idempotency`): reclamo atómico en
  `claim_decision`, una llave por caso que nunca cambia y resultados registrados solo mientras el reclamo sigue vigente. A
  diferencia de la prima, la ventana es de **3 intentos** (2 s y 4 s entre ellos), no de 24 h: el analista necesita la
  excepción en segundos para actuar (O6b).
- **Bruto y neto:** el analista aprueba el costo bruto (`gross_amount_minor`); el deducible lo asume el asegurado y la
  aseguradora paga el neto (`payout_amount_minor`), que es el "monto aprobado" de O6. El deducible se calcula solo aquí.
- **El core de pólizas manda:** el valor asegurado, el deducible y la cuenta de pago se leen del core al decidir, no de la
  copia de la radicación. Si el core no responde, la decisión responde `503 policy_unavailable` y el detalle lo dice con
  `policy_unavailable`.
- **La cuenta completa** se guarda porque hace falta para pagar, pero ninguna lectura la devuelve: solo `•••• 1234`.
- **Estados nuevos:** `approved`, `paid`, `rejected` y `payment_exception`. Ninguno es de evaluación, así que el caso sale
  de la cola y el triage deja de recalcularlo.

## Revisión del asesor e historial de estados (HU-W2.1-03)

El asesor revisa cada radicación antes de que entre a evaluación (pantalla S8): da un veredicto sobre cada soporte
(válido, ilegible o falta) y la aprueba o la devuelve al asegurado con el motivo, el mensaje y lo que debe corregir. Contrato: [`docs/contratos/HU-W2.1-03-api.md`](../../docs/contratos/HU-W2.1-03-api.md).

```
GET  /v1/claims/reviews ─────────────▶ radicaciones en active, de la más antigua a la más nueva (advisor)
GET  /v1/claims/{id}/review ─────────▶ lo de /evaluation sin el triage + checklist de soportes, siniestros previos
                                       de la póliza y revisiones                                                    [CA-1]
POST /v1/claims/{id}/review-approval ▶ todos los soportes válidos (sin un requerido faltante) ──▶ in_evaluation     [CA-2]
POST /v1/claims/{id}/review-return ──▶ motivo (lista) + mensaje + veredicto por soporte y/o datos ──▶ returned       [CA-3]
GET  /v1/claims/{id}/history ────────▶ cada cambio de estado (insured: solo los suyos; advisor y operations: todos)
```

- **Veredicto por soporte:** el *checklist* son los soportes del tipo de siniestro con sus archivos. En una radicación del
  móvil la evidencia cuenta como `vehicle_photos`, el único requerido. Lo adjunto que el asesor no marca queda `valid`, y un
  requerido sin adjuntar queda `missing`. Con un requerido faltante no se aprueba (`422 supports_incomplete`). Colisión gana
  `plate_photo` (fotografía de la placa), opcional, para que el asesor pueda pedirla.
- **Solo `advisor`** revisa (HA-S01). Cada revisión guarda quién (correo y nombre del token), cuándo, el veredicto de cada soporte y lo que pidió en `claim_review` (`seq` por ronda: un
  caso reenviado por HU-W2.1-04 se revisa otra vez). La escritura mueve el estado en la misma sentencia y solo si el caso
  sigue `active`, así que de dos asesores a la vez gana uno y el otro recibe `409`.
- **El historial lo escribe la base:** un *trigger* de `claim` agrega una fila a `claim_status_change` en cada cambio de
  `status`, sin importar qué código lo cambie. Es la fuente de la línea de tiempo (HU-W2.1-02, HU-M2.4-01) y de la
  notificación por cambio de etapa (HU-M3.3-01). `claim_review` y `claim_status_change` son de solo inserción.
- La migración `0007` reconstruye el historial de los casos anteriores: la radicación y, si estaban decididos, su estado
  actual a la hora de la decisión.

## Edición de la radicación devuelta (HU-W2.1-04)

El asegurado corrige una radicación que el asesor le devolvió y la reenvía (pantalla A9), sin crear otro siniestro.
Contrato: [`docs/contratos/HU-W2.1-04-api.md`](../../docs/contratos/HU-W2.1-04-api.md).

```
GET  /v1/claims/{id}/history ───▶ last_return: motivo, mensaje, veredicto por soporte, datos a corregir y su seq  [CA-1]
POST /v1/claims/{id}/resubmission ─▶ review_seq + hechos que cambian + por requisito el conjunto de archivos que queda
     ├─ falta algo ──▶ 422 con todo lo pendiente (not_replaced, missing, reglas de la radicación)             [CA-3]
     └─ completo ───▶ mismos id y radicado, active para una nueva revisión; claim_resubmission registra
                      quién, cuándo y qué cambió                                                     [CA-2, CA-4, CA-5]
```

- **Editables:** fecha y hora, lugar, descripción y monto estimado; la póliza y el tipo no. La fecha se valida contra la
  radicación y la vigencia de la póliza copiada.
- **Soportes:** cada grupo es el conjunto completo que queda en el requisito. Los archivos nuevos se agregan a
  `claim_support`, y los que salen se registran en `claim_support_withdrawal` y dejan de mostrarse: `claim_support` sigue
  siendo de solo inserción. Un soporte ilegible exige un archivo nuevo, y uno faltante exige al menos un archivo.
- **Concurrencia e idempotencia:** el `UPDATE` corrige el caso solo si sigue `returned` por esa misma revisión, así que de
  dos reenvíos a la vez gana uno. Un reintento del mismo asegurado recibe el reenvío que ya entró.
- **Radicaciones del móvil:** se corrigen desde el portal. Las fotos que se suben reemplazan las del móvil en el
  *checklist* del asesor (`domain.Checklist`).
- `claim_resubmission` y `claim_support_withdrawal` son de solo inserción (migración `0010`).

## Asignación del prestador más cercano (HU-W2.3-01)

El analista de operaciones asigna a un caso en evaluación el prestador más cercano con la especialidad que el caso necesita,
para que el perito o el técnico vaya al sitio. Contrato:
[`docs/contratos/HU-W2.3-01-api.md`](../../docs/contratos/HU-W2.3-01-api.md).

```
GET  /v1/claims/{id}/assignment ──────▶ especialidad, ubicación, última búsqueda y asignación; el estado de la asignación   [CA-1…CA-6]
POST /v1/claims/{id}/incident-location ▶ dirección + pin del analista, solo si la app no dio coordenadas                   [CA-1]
POST /v1/claims/{id}/provider-searches ▶ los prestadores con la especialidad por distancia; radio 5 → 10 → 20 km
                                         hasta el primero con elegibles, cada radio registrado; sugerido el más cercano      [CA-2…CA-4]
        sin elegibles en 20 km ────────▶ exhausted, pending_assignment                                                     [CA-6]
        assessors.Worker ──cada 300 s──▶ busca de nuevo los pendientes ──▶ proposed (no asigna)                            [CA-6]
POST /v1/claims/{id}/assignment ──────▶ prestador elegible de la última búsqueda ──▶ assigned; el prestador queda busy
        assessors.Worker ──────────────▶ correo al prestador, llave asignacion-<id>, reintentos 5 s, 30 s, 2 min, 10 min  [CA-5]
GET  /v1/claims/assignments?claim_id=… ▶ el estado de una página de casos en una llamada (cola de operaciones)
GET  /v1/claims/assignment-policy ────▶ especialidad por tipo, radios e intervalo de la búsqueda automática
GET|POST /v1/claims/providers* ───────▶ registro de prestadores: alta, perfil y disponibilidad                             [CA-2]
```

- **Solo `operations`** (HA-S01): el asegurado, el asesor y el socio reciben `403`. El asegurado no ve la asignación en esta
  historia; la fecha de la visita es de HU-W2.3-02.
- **Qué casos se asignan:** los que están en `in_evaluation` (aprobados por el asesor, HU-W2.1-03) y cuyo tipo necesita visita.
  Otro estado responde `409 claim_not_assignable`. Una asignación ya hecha sigue visible aunque el caso cambie de estado después.
- **Ubicación del incidente:** la de la app es la evidencia del caso con `location_trusted` y la menor `accuracy_m` (≤ 50 m,
  HU-M2.1-01); en un empate, la primera capturada. Sin ella (todo caso web) el estado es `location_missing` hasta que el analista
  registra la dirección (5–200 caracteres) y las coordenadas del pin. El front resuelve la dirección en el mapa; **no hay
  geocodificador**. El analista puede corregirla antes de asignar (vale la última) y no puede reemplazar la de la app
  (`409 location_from_app`).
- **Política `assignment-policy-v1`** (`internal/domain/assignment_policy.go`), **provisional, pendiente de validación con
  negocio**, versionada como la del triage:
  - colisión, daño a terceros y otro (autos) → `vehicle_assessment`; cristales → `auto_glass`; daño por agua (hogar) →
    `plumbing`; hurto no necesita visita (`not_required`);
  - radios de 5, 10 y 20 km; 20 es el máximo.

  Cada búsqueda y cada asignación guardan la versión.
- **Distancia:** en línea recta (Haversine) entre el incidente y el prestador (`internal/domain/geo.go`, que solo usa
  `assessors`). Se compara sin redondear; se muestra redondeada a 0,1 km.
- **Elegible** = disponible **hoy** + con la especialidad + dentro del radio del intento. Un prestador «disponible mañana» está
  `unavailable` con `unavailable_until` y no es elegible. El analista ve a todos los de la especialidad con su
  `eligibility` (`eligible`, `unavailable`, `out_of_radius`). Solo se asigna uno `eligible` de la **última** búsqueda (si no,
  `409 search_outdated` o `422 provider_not_eligible`); a uno fuera del radio se llega ampliando el radio, que queda como
  intento. Sugerido: el elegible más cercano; en un empate, el de menor `provider_id`.
- **Asignación y aviso:** una por caso, en una transacción que inserta la asignación, deja al prestador `busy` y registra el
  aviso. De dos analistas a la vez, o de dos casos sobre el mismo prestador, gana uno: el otro recibe `409 assignment_exists` o
  `409 provider_unavailable`. El `Worker` envía el correo por `EMAIL_URL` con el radicado, el tipo, las coordenadas, la
  precisión, la dirección y un enlace al mapa, sin datos del asegurado. Sin `EMAIL_URL`, o con el proveedor caído, la asignación
  se registra igual y el aviso espera.
- **Estado propio:** `claim.status` **no cambia** al buscar, al quedar pendiente ni al asignar, así que el triage, la decisión y la
  revisión no se enteran y `claim_status_change` no gana filas. El estado de la asignación es `not_required`, `not_applicable`,
  `location_missing`, `ready`, `proposed`, `pending_assignment` o `assigned`.
- **Búsqueda automática:** el `Worker` repite la búsqueda de los casos `pending_assignment` cada
  `ASSIGNMENT_RETRY_INTERVAL_SECONDS` (actor `system`). Si encuentra elegibles el caso queda `proposed`, y el analista confirma.
- **Registro de prestadores:** semilla de referencia `0009_provider_seed.sql` con 8 prestadores de Bogotá, **pendiente de
  reemplazo por negocio**; no pisa lo que operaciones haya editado. Operaciones da de alta, cambia el perfil y la
  disponibilidad por API; cada cambio guarda quién y cuándo. `busy` solo lo escribe la asignación.
- **Cómo lo usa otra historia:** `assessors.Suggest(ctx, providers, at, specialty, policy, now)` es la misma búsqueda sin caso y
  sin registrarla: la asistencia en sitio (HU-W2.3-03) la llama en proceso, sin otra copia de la distancia ni de la
  elegibilidad. HU-W2.3-02 libera al prestador al cerrar la intervención y se encarga de reasignar o cancelar; mientras tanto
  operaciones puede devolverlo a `available` por la API.
- **No publica ni consume eventos:** el caso, los prestadores y la asignación están en la misma base, y el aviso es un correo.

## Consulta ágil del estado del siniestro (HA-L02)

La lista, el detalle y el historial de estados que lee el asegurado (y el historial que leen el asesor y operaciones) pasan por
una caché de lectura en Redis. Es el mecanismo validado en el experimento EC-L02 (p95 de 1.121 ms con un corte de 900 ms).
Contrato: [`docs/contratos/HA-L02-api.md`](../../docs/contratos/HA-L02-api.md).

```
GET /v1/claims[/{id}[/history]]
  └─ base de datos, con 900 ms (CLAIM_READ_BUDGET_MS)
       ├─ responde ──────────────▶ se guarda en Redis y sale con stale=false, as_of=ahora
       ├─ responde «no existe», «no es tuyo» o «filtro inválido» ─▶ sale tal cual (la base contestó: no se degrada)
       └─ tarda más o falla ──▶ Redis
                                  ├─ hay lectura guardada ─▶ sale con stale=true, as_of=hora de esa lectura
                                  └─ no hay ──────────────▶ 503 claims_unavailable, como antes
```

- **Campos nuevos en las tres respuestas:** `stale` y `as_of`, más las cabeceras `X-Stale` y `X-Data-Source`
  (`database` o `cache`). Son aditivos: el portal y el móvil que no los leen siguen funcionando. La respuesta de una
  radicación no los lleva, porque no es una lectura.
- **Aislamiento:** la clave lleva el `sub` de quien lee (`owner:<sub>:claim:<id>`, `owner:<sub>:list:<hash del filtro>`), así
  que la caché solo devuelve algo que esa misma persona ya leyó de la base. El historial del asesor y de operaciones va en
  claves `staff:` aparte.
- **Frescura ≤ 5 minutos:** las lecturas normales siempre van primero a la base, y cada entrada vence a los 5 minutos
  (`CLAIM_READ_CACHE_TTL_SECONDS`, máximo 300). Un cambio de estado nunca queda tapado por la caché más de ese tiempo.
- **Sin Redis no pasa nada:** sin `REDIS_ADDR` las consultas leen solo la base. Si Redis se cae, la falla se registra y la
  consulta sigue contra la base.
- Prueba de carga: [`tests/load-k6/ha-l02`](../../tests/load-k6/ha-l02/README.md).

## Cola de trabajo ordenada por prioridad (HU-W2.2-02)

`GET /v1/claims/evaluations` es la cola de operaciones (O5): los casos `in_evaluation`, ordenados por nivel del triage
(1, 2, 3 y al final los que aún no tienen) y en cada nivel por tiempo de espera desde la radicación. Contrato:
[`docs/contratos/HU-W2.2-02-api.md`](../../docs/contratos/HU-W2.2-02-api.md).

- **Orden en la base:** `PriorityQueue` (`internal/adapters/postgres/priority.go`) une `claim` con `claim_current_triage` y
  ordena por `(rango, filed_at, id)` con paginación por cursor. Un nuevo cálculo del triage mueve el caso en la siguiente
  lectura. En memoria, `memory.PriorityQueue` ordena con los niveles del servicio de triage.
- **Filtros:** `level` (1, 2, 3, `none`), `line` (por el tipo de siniestro) y `status`. Los indicadores (`summary`) cuentan
  toda la cola por nivel. `matching` cuenta lo que cumple los filtros, y el tiempo medio de resolución es el de los casos
  decididos en los últimos 30 días (`null` si no hay ninguno).
- **Asegurado de cada fila:** se lee del núcleo de pólizas una vez por asegurado de la página, con hasta 8 lecturas a la
  vez. Si falla, la fila llega sin él.

## Requisitos de calidad que gobiernan el servicio

- Consulta de estado del siniestro p95 ≤ 2 s en la app móvil (EC-L02): caché de lectura en Redis con corte duro de 900 ms; ante timeout responde la última versión válida con `stale=true`.
- Absorber ≥ 1.000.000 de eventos paramétricos en 10 min con contrapresión y sin pérdida (HA-05).
- Despliegue: 2 tareas Fargate, una por zona de disponibilidad.

## Datos y eventos

- **BD propia:** PostgreSQL (siniestros): `claim` (con `channel`, `policy_snapshot` y `catalog_version` desde HU-W2.1-01 y `estimated_amount_minor` desde HU-W2.2-01), `claim_evidence`, `support_file` (metadatos; el archivo vive en S3), `claim_support` y `claim_triage` (HU-W2.2-01: cada cálculo del nivel, solo se agregan filas) con su vista `claim_current_triage`, `claim_decision` (HU-W2.2-03: la decisión y el pago de la indemnización), `claim_payout_retry` (cada reintento manual), `claim_review` (HU-W2.1-03: cada revisión del asesor) y `claim_status_change` (cada cambio de estado, escrito por un *trigger*). HU-W2.3-01: `provider` (registro de prestadores), `claim_incident_location` (cada ubicación que registra el analista), `claim_provider_search` y `claim_provider_search_attempt` (cada búsqueda y cada radio intentado), `claim_assignment` (una por caso) y `provider_notice` (el aviso al prestador y sus intentos); las cuatro de historial son de solo inserción. Caché: Redis (`internal/adapters/redis`). Objetos: S3 (`internal/adapters/s3`).
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
| `PAYMENT_GATEWAY_URL` | HU-W2.2-03: aprobar, rechazar y reintentar el pago no se montan (una aprobación no tendría cómo pagarse). La lectura de la evaluación sí |
| `EMAIL_URL` | HU-W2.2-03: los rechazos se registran igual y sus avisos esperan hasta que se configure. **HU-W2.3-01:** también avisa al prestador asignado; sin ella las asignaciones se registran y sus avisos esperan |
| `REDIS_ADDR` | HA-L02: las consultas del asegurado no tienen caché de lectura y solo leen la base. Un Redis que no responde al arrancar no impide arrancar |
| `CLAIM_READ_BUDGET_MS` | HA-L02: cada consulta espera 900 ms a la base antes de usar la caché. Entre 50 y 5000; otro valor impide arrancar |
| `CLAIM_READ_CACHE_TTL_SECONDS` | HA-L02: cada lectura guardada vence a los 300 s (5 minutos). Entre 1 y 300; otro valor impide arrancar |
| `ASSIGNMENT_RETRY_INTERVAL_SECONDS` | HU-W2.3-01: la búsqueda automática de los casos pendientes de asignación corre cada 300 s. Entre 10 y 3600; otro valor impide arrancar. Solo se acorta en local y en la e2e |

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
**HU-W2.3-01:** `tests/integration/assignment_test.go` ejercita cada criterio de la asignación por HTTP con el `Worker` corriendo,
además de las carreras y las tablas de solo inserción; el contrato pasa con los tres roles;
[`tests/e2e/hu-w2.3`](../../tests/e2e/hu-w2.3/README.md) la recorre de punta a punta con el correo detrás de Toxiproxy.
