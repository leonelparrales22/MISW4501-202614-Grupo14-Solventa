# E2E — HU-W1.4 · Modificaciones de la póliza

Pruebas de punta a punta de la solicitud de modificación del asegurado (HU-W1.4-01) y de su aplicación controlada por el analista
(HU-W1.4-02). En la ventana de staging, siempre a través del
portal de CloudFront (como lo llama el navegador) y solo con los usuarios permanentes de `bootstrap/identity`
(`asegurado@example.com`, `asesor@example.com`, `operaciones@example.com` y, cuando exista, `asegurado2@example.com`). Las pruebas por
componente y por criterio, sin AWS, están en `services/origination/tests/integration/change_request_test.go` y el contrato en
`services/origination/tests/contract/run-change-requests.sh`.

## HU-W1.4-01 · Verificación en la nube (`change-request-cloud.sh verify`)

**No siembra datos:** las pólizas las compra, por el recorrido real, el guion único de siembra de la ventana
(`tests/e2e/hu-w1.5/assisted-renewal-cloud.sh --seed`, decisión V-2 a): **P8** para la demostración, **P9** para esta verificación y,
si `asegurado2@example.com` ya existe en el *pool* permanente (rama `chore/T4-second-insured-user`, ajuste A-6), **P10**, de ese
segundo asegurado. Si `SEED_FILE` no trae P9 vigente del asegurado, el guion lo dice y termina sin comprobar nada.

```
PORTAL_URL=https://<distribución>.cloudfront.net SEED_USER_PASSWORD=… SEED_FILE=<carpeta de evidencia>/semilla.env \
OUTPUT_FILE=<carpeta de evidencia>/change-request-cloud.txt \
  ./tests/e2e/hu-w1.4/change-request-cloud.sh verify
```

| Bloque | Qué comprueba |
|---|---|
| Datos | `SEED_FILE` trae P9 y P9 es una póliza vigente del asegurado (`GET …/current-terms` → `200`, `in_force`) |
| CA-1 | Las condiciones de P9: valor asegurado 68 000 000, sin beneficiarios designados, beneficiario oneroso con base `outstanding_balance` (imprime su nombre o código), correo de contacto conocido y dirección y teléfono sin registrar, nada abierto y los límites de la captura |
| CA-3 | Participaciones que suman 120 %, nombre y documento vacíos, correo inválido, valor fuera de rango y el mismo valor → `422` con cada campo; el beneficiario oneroso en el cuerpo → `400`; la lista de P9 no cambia |
| CA-1 / CA-2 | Beneficiarios → `201` con `SOL-MOD-<año>-<número>`, `filed`, canal `web`; el detalle trae la foto con el oneroso, los dos pedidos y la diferencia; aparece en la lista y en el indicador de abiertas; otro de beneficiarios → `409 change_request_open` con el mismo id; suma asegurada y datos personales → `201`; quedan tres abiertas |
| CA-4 | Sin token `401` en las cuatro rutas; asesor y operaciones `403` en las condiciones y en radicar, y el asesor `403` en la lista. **Con P10:** el asegurado recibe `403` en las condiciones y al radicar sobre la póliza del segundo asegurado y no se radica nada; la lista del segundo asegurado no muestra las solicitudes del primero ni puede leerlas (`403`). Sin P10 se marca `SKIP` y el caso queda con la evidencia local |
| D-2 | Una póliza del núcleo de pólizas, tomada de la billetera (`source: policy_core`) → `404 policy_not_found`; si la billetera no trae ninguna, `SKIP` |

- **Cambia datos de la ventana solo en P9:** deja tres solicitudes radicadas sobre ella. P8 queda para la demostración (guía §6.5 del
  plan). Todo vive en el RDS efímero de staging y se va con el `down.sh`.
- Solo llama a `InitiateAuth` del *user pool* (con el cliente y la región del `config.json` del portal) y al portal; nada más en AWS. La
  contraseña y los tokens nunca se imprimen: la contraseña viaja por la entrada estándar y el token en un archivo temporal privado.
- Requiere `curl` y `jq`. `COGNITO_ENDPOINT` acepta un *endpoint* local solo para ensayar el guion. Termina con un código distinto de cero
  si alguna comprobación falla.

## HU-W1.4-02 · Aplicación controlada del endoso en local (`endorsements.sh`)

Recorre la historia con los servicios reales en Docker (`docker-compose.yml` de esta carpeta): `audit`, `origination` y `claims` con su
PostgreSQL, Redis, los *stubs* de Open Finance, Open Data y KYC detrás de Toxiproxy (KYC responde también la verificación de documentos
de la Registraduría), la pasarela de pagos, el correo y Moto como Cognito. Las pólizas se compran por el recorrido real (consentimiento
en `audit`, `POST /v1/offers`, aceptación y la tarjeta 4111) y las solicitudes las radica el asegurado por las rutas de HU-W1.4-01.

```
./tests/e2e/hu-w1.4/endorsements.sh
```

| # | Qué comprueba |
|---|---|
| E1 | Beneficiarios: la bandeja la muestra pendiente con el titular (sin el correo del asegurado); *Revisar* → `in_review`, y el asegurado la ve en revisión; el detalle trae dos filas, el panel de la póliza con el oneroso y las cinco validaciones en `passed` (vista previa, dos verificaciones en la Registraduría); aplicar → `END-<año>-<número>` del analista; el asegurado la ve aplicada y sus condiciones vigentes traen los beneficiarios nuevos |
| E2 | Suma asegurada: el detalle trae la prima nueva del motor `v1`; aplicada, la billetera del asegurado, el listado del asesor y el panel del analista muestran el valor y la prima nuevos |
| E3 | Datos personales: las condiciones vigentes traen dirección, teléfono (normalizado) y correo de contacto nuevos; el correo de inicio de sesión del usuario de Moto no cambia |
| E4 | Un documento que termina en `000`: `not_applied` con `identity_documents_valid`, la póliza intacta y el asegurado la sigue viendo en revisión; *Devolver* sin observaciones `422`; con ellas, el asegurado la ve rechazada con el motivo |
| E5 | *Rechazar* sin observaciones `422`; con ellas `rejected` y el motivo para el asegurado; aplicar después `409 request_already_decided` |
| E6 | Un siniestro `in_evaluation` sembrado con `INSERT` en el PostgreSQL de `claims`: la ruta interna lo cuenta y `no_open_claim` deja el endoso sin aplicar |
| E7 | Dos aplicaciones a la vez: las dos responden `200` con el mismo endoso y se guarda uno |
| E8 | KYC caído (*proxy* deshabilitado) y `claims` detenido: `503 validation_unverifiable` con la validación y nada registrado; de vuelta, se aplican (la suma, sobre la versión 2) |
| E9 | Historial de E1 (endoso con su autor) y de E4 (intento con la validación que falló); una póliza sin endosos responde vacío; igual tras reiniciar `origination` |
| E10 | Las siete rutas del analista: sin token `401`, asegurado y asesor `403` |
| E11 | El *stub* de correo recibe un mensaje por decisión (aplicada, devuelta con las observaciones, rechazada) con la clave `endorsement-outcome:<id>`, y ninguno para una no aplicada sin devolver |

- Al final revisa que los logs de los endosos no lleven documentos, nombres, correos ni teléfonos.
- Puertos (127.0.0.1): `audit` 8690, `origination` 8691, `claims` 8692, Toxiproxy 8694, Moto 5290, correo 9291 y pasarela 9294.
- Requiere Docker (compose v2), `curl`, `jq` y la AWS CLI (solo contra el Moto local). Los secretos se generan al azar y nunca se
  imprimen; el entorno se retira siempre (`down -v`). Termina con un código distinto de cero si alguna comprobación falla.

## HU-W1.4-02 · Verificación en la nube (`endorsement-cloud.sh verify`)

**Compra sus propias pólizas** (V1…V5, de 12 meses y del asegurado, por el recorrido real) y radica sus solicitudes como el asegurado,
así que E1…E6 de la siembra única (`tests/e2e/hu-w1.5/assisted-renewal-cloud.sh --seed`, D-15 a) quedan para la demostración (§7.5 del
plan).

```
PORTAL_URL=https://<distribución>.cloudfront.net SEED_USER_PASSWORD=… \
OUTPUT_FILE=<carpeta de evidencia>/endorsement-cloud.txt \
  ./tests/e2e/hu-w1.4/endorsement-cloud.sh verify
```

| Bloque | Qué comprueba |
|---|---|
| CA-1, CA-2, CA-5 | V1 beneficiarios: la bandeja la muestra pendiente; *Revisar* → `in_review` y el asegurado la ve en revisión; el detalle trae dos filas y cinco validaciones en `passed` (la Registraduría de KYC verificó dos documentos y `claims` respondió dentro de la VPC: ningún siniestro en curso); aplicar → `END-<año>-<número>`; el asegurado la ve aplicada y sus condiciones traen los beneficiarios |
| CA-3 | V2 suma asegurada: el motor `v1` tarifa la prima nueva; aplicada, la billetera del asegurado (web y app) y el panel del analista muestran el valor y la prima nuevos |
| CA-2, CA-5 | V3 un documento que termina en `000`: `not_applied`, la póliza intacta; *Devolver* sin observaciones `422`; con ellas el asegurado lee el motivo |
| CA-5 | V4 *Rechazar* sin observaciones `422`; con ellas `rejected`; aplicar después `409` |
| Concurrencia | V5 dos aplicaciones a la vez: las dos `200` con el mismo endoso y una sola entrada en el historial |
| CA-4 | Historial de V1 (el endoso y su autor) y de V3 (el intento con la validación que falló) |
| Roles | Las siete rutas: sin token `401`, asegurado y asesor `403` |
| D-12 | Con la AWS CLI y `LOG_GROUP` (solo lectura, `logs filter-log-events` del *stream* `email`): el *stub* de correo registró un mensaje por decisión de V1, V3 y V4; si no puede leerlo, `SKIP` |

- Solo llama a `InitiateAuth` del *user pool* (con el cliente y la región del `config.json` del portal), al portal y, para D-12, a
  CloudWatch Logs en lectura. La contraseña y los tokens nunca se imprimen. Todo lo que compra y radica vive en el RDS efímero de
  staging y se va con el `down.sh`.
- Requiere `curl` y `jq`. Termina con un código distinto de cero si alguna comprobación falla.
