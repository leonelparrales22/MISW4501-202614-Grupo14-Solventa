# E2E — HU-W1.5 · Renovación de pólizas

Pruebas de punta a punta de las historias de renovación del asesor: en local con Docker (`assisted-renewal.sh`, HU-W1.5-03) y en la
ventana de staging, siempre a través del portal de CloudFront (como lo llama el navegador) y solo con los usuarios permanentes de
`bootstrap/identity` (`asesor@example.com`, `asegurado@example.com`, `operaciones@example.com`). Las pruebas por componente y por
criterio, sin AWS, están en `services/origination/tests/integration` (`expiring_test.go` para HU-W1.5-01 y `assisted_renewal_test.go`
para HU-W1.5-03).

## HU-W1.5-03 · Renovación asistida en local (`assisted-renewal.sh`)

Levanta con [`docker-compose.yml`](docker-compose.yml) los servicios reales `audit` y `origination` (cada uno con su PostgreSQL), Redis,
los *stubs* de Open Finance, Open Data y KYC detrás de Toxiproxy, la pasarela de pagos, el correo y Moto como Cognito, con
`RENEWAL_WINDOW_DAYS=365` (una póliza de 12 meses comprada hoy ya se puede renovar). Compra seis pólizas **por el recorrido real**
(consentimiento en `audit`, `POST /v1/offers`, el asegurado abre y acepta la oferta, entrega la tarjeta 4111 y el cobro se confirma) y
recorre los escenarios:

```
./tests/e2e/hu-w1.5/assisted-renewal.sh
```

| Escenario | Qué comprueba |
|---|---|
| R1 | CA-1: `GET` con `can_requote` y `consent_mode: audit`; `POST …/requotes` → `201`, motor `v1`, descuento 8 %, fuentes `ok` |
| R2 | CA-2: sin aceptación, medio desconocido, fecha futura o firma sin referencia → `422` por campo; ninguna renovación guardada |
| R3 | CA-3: formalizar → cobro confirmado por la prima recotizada; póliza nueva con `renewal_of` y vigencia continua; certificado emitido, su PDF, el correo al titular y el monitor `channel=renewal` |
| R4 | CA-4: tras reiniciar `origination`, el asesor (nombre de Moto), la fecha, el medio, la referencia y la recotización aplicada |
| R5 / R5b | CA-5: Open Finance deshabilitado en Toxiproxy → `provisional`, `cache`, `last_profile_in_use` y `profile_date`; sin perfil en caché (borrado en Redis) → `base_actuarial` y `personalization_unavailable`; las fuentes vuelven → `normal` |
| R6 | Sin *token* `401`; asegurado y operaciones `403` |
| R7 | Dos asesores a la vez → `201` y `409`; el asegurado encuentra la misma renovación; un solo cobro |
| R8 | Las respuestas sin `score`, código, correo del titular ni *token*; los registros de la renovación asistida sin documento, correo, referencia, código ni *token* |
| R9 | D-9: el código llega al correo del titular (leído del *stub*), un código errado `422`, el cuarto envío `429`, el correcto `201` |
| R10 | D-8: operaciones revoca el consentimiento en `audit` (`POST /v1/audit/consents/{id}/revocations`) → `422 consent_required` sin consultas a las fuentes; `audit` detenido → `503 consent_unverifiable` sin consultas |
| R11 | D-10: póliza con el cobro original sin medio de pago, sembrada con `INSERT` en el PostgreSQL local (el recorrido real no la produce) → `payment_method_missing` y `409` al formalizar |
| R12 | 01·D-3 (a+): la pasarela se reinicia y olvida sus *tokens* → el cobro de la renovación se rechaza; «Ver» muestra `failed`, `payment_rejected`, la fecha del fallo y `retry_until`; el asegurado entrega otra tarjeta → renovada |
| R13 | D-13 (c): póliza de origen asistido sembrada con `INSERT` (oferta `off-q-…` con `channel = assisted`, su cobro y su póliza; el recorrido de HU-W1.1-03 necesitaría una cuenta del cliente) → `consent_mode: advisor_affirmation`; sin la casilla `422` sin preguntar a `audit` ni a las fuentes; con ella `201 advisor_affirmed`; formalizar → renovada |

- «Sin consultas a las fuentes» se comprueba en Toxiproxy (cada llamada a un *stub* abre una conexión y el proxy la registra) y «sin
  preguntar a `audit`» en su registro de peticiones.
- Crea en el Moto local un segundo asesor sin el atributo `name` para R7. Los secretos (PostgreSQL, usuarios de Moto, el enlace de los
  documentos) son aleatorios y nunca se imprimen; el entorno se elimina al terminar (`down -v`).
- Requiere Docker (compose v2), `curl`, `jq` y la AWS CLI (solo contra el Moto local). Termina con un código distinto de cero si alguna
  comprobación falla.

## HU-W1.5-01 · Pólizas próximas a vencer (`expiring-cloud.sh`)

Comprueba `GET /v1/policies/expiring` como `asesor@example.com`. **No siembra datos:** las pólizas las crea, por el recorrido real y para
`asegurado@example.com`, el guion único de siembra de las dos historias (`assisted-renewal-cloud.sh --seed`, de HU-W1.5-03). Si el
asegurado no tiene al menos dos pólizas renovables, el guion lo dice y termina sin comprobar nada.

```
PORTAL_URL=https://<distribución>.cloudfront.net SEED_USER_PASSWORD=… RENEW_ONLINE_POLICY_ID=<P3 de la siembra> \
OUTPUT_FILE=<carpeta de evidencia>/expiring-cloud.txt \
  ./tests/e2e/hu-w1.5/expiring-cloud.sh
```

| Bloque | Qué comprueba |
|---|---|
| Datos | La billetera del asegurado (`GET /v1/policies`) tiene al menos dos pólizas de originación renovables |
| CA-1 | Sin parámetros: `200`, el rango por defecto es hoy (Colombia) → hoy + `window_days`; cada fila trae vencimiento, estado conocido, línea `life` y el titular sin correo; orden por vencimiento; están todas las pólizas renovables del asegurado |
| CA-2 | Con una póliza renovable: un rango que termina el día anterior a su vencimiento la excluye y el rango de su propio día la incluye, con el rango aplicado en la respuesta; el rango invertido responde `400 out_of_range` |
| CA-3 | Un rango sin pólizas (dentro de cinco años) responde `200` con `items` vacío y `total` 0 |
| CA-4 | La fila trae `can_renew: true` y `renewal: null`; imprime el enlace `…/originacion/renovaciones/<policy_id>` de *Renovar*. Si hay una póliza fuera de la ventana (P7 de la siembra, 24 meses), sale sin *Renovar* y con `renewal_window.opens_at` en el futuro («Renovable desde») |
| En curso | El asegurado acepta en línea la renovación de `RENEW_ONLINE_POLICY_ID` (`POST /v1/policies/{id}/renewal`, `201` o `200` si ya estaba aceptada) y el asesor la ve `renewal_in_progress`, sin *Renovar* y con la renovación `accepted`. Si el barrido ya la cerró entre las dos llamadas, comprueba que quedó `renewed` |
| Roles | Sin token `401`; con el token del asegurado `403`, sin pólizas en la respuesta |

- **Cambia datos de la ventana en un solo punto:** acepta la renovación en línea de `RENEW_ONLINE_POLICY_ID`. Por eso la póliza se pasa
  explícitamente (P3 de la siembra, la que el asegurado renueva en línea) y las demás quedan para la demostración de HU-W1.5-03. La
  ventana de staging es efímera: nada de esto sobrevive al `down.sh`.
- Solo llama a `InitiateAuth` del *user pool* (el inicio de sesión público, con el cliente y la región del `config.json` del portal) y al
  portal; nada más en AWS. La contraseña y los tokens nunca se imprimen: la contraseña viaja por la entrada estándar y el token en un archivo
  temporal privado.
- Requiere `curl` y `jq` (sin `date -d`: las fechas de Colombia las calcula `jq`). `COGNITO_ENDPOINT` acepta un *endpoint* local solo para
  ensayar el guion. Termina con un código distinto de cero si alguna comprobación falla.

## HU-W1.5-01 y HU-W1.5-03 · Siembra única y verificación en la nube (`assisted-renewal-cloud.sh`)

Es **el único guion de siembra de las dos historias** (decisión V-2 a). Todo va por el portal de CloudFront y solo con los usuarios
permanentes; **no crea usuarios** y nada de lo que siembra sobrevive al `down.sh` (la plataforma de staging es efímera).

```
PORTAL_URL=https://<distribución>.cloudfront.net SEED_USER_PASSWORD=… SEED_FILE=<carpeta de evidencia>/semilla.env \
OUTPUT_FILE=<carpeta de evidencia>/seed.txt ./tests/e2e/hu-w1.5/assisted-renewal-cloud.sh --seed

PORTAL_URL=… SEED_USER_PASSWORD=… OUTPUT_FILE=<carpeta de evidencia>/verify.txt ./tests/e2e/hu-w1.5/assisted-renewal-cloud.sh verify

SEED_FILE=<carpeta de evidencia>/semilla.env ./tests/e2e/hu-w1.5/assisted-renewal-cloud.sh code <policy_id de P6>
```

| Subcomando | Qué hace |
|---|---|
| `--seed` | Compra por el recorrido real (consentimiento del titular en `audit` por la ruta del aliado —CloudFront agrega la llave—, `POST /v1/offers`, el asegurado abre y acepta la oferta, tokeniza la tarjeta 4111 en `/pasarela/tokens` y la entrega, y el cobro se confirma) siete pólizas de `asegurado@example.com` con titulares ficticios distintos (`@example.com`): P1 camino feliz (CA-1…CA-4), P2 modo degradado (CA-5), P3 renovada en línea por el asegurado (solo lectura y concurrencia; es la `RENEW_ONLINE_POLICY_ID` de `expiring-cloud.sh`), P4 pago fallido (01·D-3 a+), P5 consentimiento revocado (D-8), P6 código de confirmación (D-9), todas de 12 meses, y P7 de 24 meses, fuera de la ventana. Escribe sus ids, el consentimiento de cada una y el correo de su titular en `SEED_FILE` |
| `verify` | Compra **sus propias** cuatro pólizas (P1…P7 quedan para la demostración) y comprueba por API: CA-1 (motor `v1`, descuento, factores, sin `score`), CA-2 (`422` por campo, nada guardado), CA-3 (renovada, cobro confirmado, certificado emitido, la póliza nueva en «Mis pólizas» con la prima recotizada), CA-4 (asesor, medio, referencia y recotización aplicada), D-9 (el código enmascarado y, con `LOG_GROUP` y la AWS CLI, el código leído del registro: errado `422`, correcto `201`; si no puede leerlo lo marca `SKIP`), D-8 (operaciones revoca por el portal → `422 consent_required`), roles (`401`, `403` del asegurado y de operaciones) y concurrencia (dos formalizaciones del asesor y la del asegurado a la vez → una renovación y un solo cobro) |
| `code <policy_id>` | Lee de CloudWatch Logs, **solo lectura** (`logs filter-log-events` del *stream* `email`), el último código de renovación enviado al titular de una póliza sembrada y lo muestra en la terminal, para dictárselo al asesor en la demostración (aprobado por Nicolás) |

- Lo que el recorrido real no produce se demuestra solo en local (`assisted-renewal.sh`): la póliza sin medio de pago (R11), el perfil
  actuarial base (R5b) y la póliza de origen asistido (R13), que exigiría crear un usuario en el *pool* permanente.
- Solo llama a `InitiateAuth` del *user pool* (con el cliente y la región del `config.json` del portal), al portal y, para el código, a
  `logs filter-log-events`. La contraseña y los tokens nunca se imprimen: la contraseña viaja por la entrada estándar y el token en un
  archivo temporal privado. `COGNITO_ENDPOINT` acepta un *endpoint* local solo para ensayar el guion.
- Requiere `curl` y `jq` (y la AWS CLI para `code` y el D-9 de `verify`). Termina con un código distinto de cero si alguna comprobación
  falla.
