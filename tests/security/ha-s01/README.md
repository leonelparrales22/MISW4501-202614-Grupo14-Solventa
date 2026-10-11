# `security/ha-s01` — verificación de acceso de HA-S01 en staging

Guion que comprueba en AWS lo que no se puede medir en local: el authorizer de Cognito del API Gateway, el user pool y la
separación entre personas y socios. Corre contra la ventana efímera
[`infra/terraform/environments/staging/ha-s01`](../../../infra/terraform/environments/staging/ha-s01/README.md) y solo usa
`curl` (sin credenciales de AWS: las llamadas a Cognito son las públicas de los clientes).

> **Estado:** preparado; **no se ha ejecutado en AWS** (la ventana de staging de HA-S01 está pendiente). Se probó en local contra
> un doble de Cognito y de las dos APIs (todos los casos, incluidos la cota de revocación y el vencimiento con tokens cortos, y
> los negativos: socio equivocado, sin CORS en el 401 del módulo, módulo que no reenvía el JWT y token revocado que sigue
> pasando después de `exp`).

Para trabajar **sin AWS**, Cognito se emula con Moto en [`local/`](local/README.md) (compose, siembra de pool, grupos, clientes y
un usuario por rol, y la tabla «local ↔ AWS» con lo que Moto no reproduce).

## Las dos APIs de la ventana

- **T** — API REST de prueba de la composición (integraciones MOCK que devuelven las claims que recibió el authorizer):
  `GET /ha-s01/humana` (ruta humana), `GET /ha-s01/socio` (ruta de socio, solo API key).
- **M** — el módulo [`apigateway`](../../../infra/terraform/modules/apigateway/README.md) **real**: `GET /v1/ha-s01-check` (ruta
  humana con el authorizer, `OPTIONS` sin autorización, 4XX con CORS), que reenvía la petición (HTTP_PROXY) a
  `GET /v1/ha-s01-check` de T, protegida por el mismo authorizer; y `GET /v1/risk-profiles/{customer_id}` (ruta de socio). Un
  `200` de M con las claims prueba que **el JWT llega a la integración**. Si la composición no tiene las salidas del módulo, los
  casos de M salen `SKIPPED`.

## Casos

| # | Caso | API | Esperado | Criterio |
|---|---|---|---|---|
| 1 | Ruta humana sin token | T · M | `401`; en M con `Access-Control-Allow-Origin` (el navegador puede leer el 401) | CA-1 |
| 1 | *Preflight* CORS (`OPTIONS`) sin token | M | `200` con `Access-Control-Allow-Methods` que incluye `GET` | CA-1 |
| 1 | Ruta humana con solo la API key del socio | T · M | `401` | CA-5 |
| 2 | Ruta humana con un ID token válido (asesor) | T · M | `200`; llegan `sub` y `cognito:groups`; en M, el eco del *backend* con el mismo `sub` | CA-1, CA-2 |
| 2 | Ruta humana con la firma alterada (un carácter) | T · M | `401` | CA-1 |
| 2 | Ruta humana con el *payload* alterado (grupo cambiado a `operations`, sin volver a firmar) | T · M | `401` | CA-1, CA-2 |
| 2 | Ruta humana con el access token en lugar del ID token | T · M | Se **registra** el código real (`RECORD`, D-4) | — |
| 3 | Ruta de socio con solo el token *Bearer* | T · M | `403` | CA-5 |
| 3 | Ruta de socio con la API key | T | `200` (en M el *backend* de esa ruta no existe en la ventana) | CA-5 |
| 4 | Usuario del portal del socio | T | `200`; llegan su grupo y `custom:partner_id` | CA-3 (D-3b) |
| 4 | Analista de operaciones · asegurado | T | `200`; llega su grupo | CA-3 |
| 5 | `SignUp` con el cliente web | — | `NotAuthorizedException` | CA-3 |
| 5 | El usuario escribe `custom:partner_id` en su propia cuenta (`UpdateUserAttributes`) | — | Rechazado | CA-3 |
| 6 | `RevokeToken` y renovación con el token revocado | — | `RevokeToken` `200`; la renovación se rechaza | CA-4 |
| 6 | `GetUser` con el access token revocado · ruta humana con el ID token emitido antes de revocar | T · M | Se **registra** el código real (`RECORD`, D-6) | CA-4 |
| 7 | **Cota de la ventana residual:** el ID token revocado se consulta cada `POLL_SECONDS` (30 s) hasta el `401` (solo con `WAIT_FOR_EXPIRY=1`) | T · M | `401` a más tardar en `exp` + 30 s; se registran los segundos desde la revocación y respecto de `exp`. **`FAIL`** si sigue pasando 30 s después de `exp` | CA-4 (DS-4) |
| 7 | Ruta humana con un ID token vencido (solo con `WAIT_FOR_EXPIRY=1`) | T · M | `401` | CA-1, CA-4 |

Con ID tokens de 5 minutos, la corrida con `WAIT_FOR_EXPIRY=1` dura unos 6 minutos: la cota espera hasta el `exp` del token del
asesor (más 30 s como máximo) y luego el vencimiento del token de operaciones, emitido después.

## Resultados en staging (2026-10-11)

Ventana `staging/ha-s01` aplicada y destruida el mismo día (`us-east-2`). Corridas del agente principal y del validador:

| Corrida | Resultado |
|---|---|
| Sin espera, antes del ajuste | 17 `OK`, 4 `FAIL`, 5 `RECORD`, 4 `SKIPPED`: la firma y el *payload* alterados respondían `403` (`ACCESS_DENIED`) en T y M |
| Ajuste | `ACCESS_DENIED` → `401` en `modules/apigateway` y en la API de prueba (el `403` de la falta de API key no cambia) |
| Sin espera, después del ajuste | 21 `OK`, 0 `FAIL`, 5 `RECORD`, 4 `SKIPPED` (los de espera) |
| Con `WAIT_FOR_EXPIRY=1` | 25 `OK`, 0 `FAIL`: vencido → `401` en T y M; el ID token revocado dejó de pasar **310 s** después de revocarlo, a `exp` + 19–20 s (cota `exp` + 30 s) |

`RECORD` reales: el access token en lugar del ID token → `401` (las rutas esperan ID tokens, D-4); `GetUser` con el access token
revocado → `400 NotAuthorizedException`; el ID token recién revocado → `200` (ventana residual, ver `docs/api/README.md`). La
contraseña, las API keys y los tokens aparecieron 0 veces en las salidas.

## Uso

Con la ventana aplicada (procedimiento en el README de la composición):

```bash
export TF_VAR_seed_user_password='…'   # la misma del apply; el guion nunca la imprime
bash tests/security/ha-s01/verify-access.sh
WAIT_FOR_EXPIRY=1 bash tests/security/ha-s01/verify-access.sh   # incluye la cota de revocación y el token vencido
```

| Variable | Defecto | Descripción |
|---|---|---|
| `TF_VAR_seed_user_password` o `SEED_USER_PASSWORD` | — (obligatoria) | Contraseña de los usuarios semilla |
| `TF_DIR` | `infra/terraform/environments/staging/ha-s01` | Composición de la que se leen las salidas (`terraform output`) |
| `WAIT_FOR_EXPIRY` | `0` | `1` mide la cota de revocación y espera a que venza un ID token |
| `POLL_SECONDS` | `30` | Intervalo de la consulta del token revocado |
| `PARTNER_ID` | `socio-a` | `custom:partner_id` esperado del usuario socio |
| `COGNITO_URL` | endpoint público de la región | Otro endpoint de Cognito (p. ej. un emulador, D-8) |

Cada línea empieza por `OK`, `FAIL`, `RECORD` (evidencia de D-4 o D-6, no aprueba ni falla) o `SKIPPED`, y lleva la API (`[T]` o
`[M]`). El guion termina con código `1` si algún caso falla y con `2` si falta la contraseña. La contraseña, los tokens y las API
keys no se imprimen: los cuerpos van por la entrada estándar, las cabeceras de la petición por un archivo temporal privado
(`umask 077`) y las de la respuesta (`curl -D`) también, y el directorio temporal se borra al terminar.
