# `security/ha-s01` — verificación de acceso de HA-S01 en staging

Guion que comprueba en AWS lo que no se puede medir en local: el authorizer de Cognito del API Gateway, el user pool y la
separación entre personas y socios. Corre contra la ventana efímera
[`infra/terraform/environments/staging/ha-s01`](../../../infra/terraform/environments/staging/ha-s01/README.md) y solo usa
`curl` (sin credenciales de AWS: las llamadas a Cognito son las públicas de los clientes).

> **Estado:** preparado; **no se ha ejecutado en AWS** (la ventana I4 de HA-S01 está pendiente). Se probó en local contra un
> doble de Cognito y del API (todos los casos, incluido el de vencimiento con tokens cortos).

| Caso | Esperado | Criterio |
|---|---|---|
| Ruta humana sin token | `401` | CA-1 |
| Ruta humana con solo la API key del socio | `401` | CA-5 |
| Ruta humana con un ID token válido (asesor) | `200`; las claims `sub` y `cognito:groups` llegan a la ruta | CA-1, CA-2 |
| Ruta humana con el token alterado (un carácter de la firma) | `401` | CA-1 |
| Ruta de socio con solo el token *Bearer* | `403` | CA-5 |
| Ruta de socio con la API key | `200` | CA-5 |
| Usuario del portal del socio | `200`; llegan su grupo y `custom:partner_id` | CA-3 (D-3b) |
| `SignUp` con el cliente web | `NotAuthorizedException` | CA-3 |
| El usuario escribe `custom:partner_id` en su propia cuenta (`UpdateUserAttributes`) | Rechazado | CA-3 |
| `RevokeToken` y renovación con el token revocado | `RevokeToken` `200`; la renovación se rechaza | CA-4 |
| `GetUser` con el access token revocado · ruta humana con el ID token emitido antes de revocar | Se **registra** el código real (línea `RECORD`) | CA-4, D-6 (ventana residual ≤ 5 min) |
| Ruta humana con un ID token vencido (solo con `WAIT_FOR_EXPIRY=1`, espera ~5 min) | `401` | CA-1, CA-4 |

## Uso

Con la ventana aplicada (procedimiento en el README de la composición):

```bash
export TF_VAR_seed_user_password='…'   # la misma del apply; el guion nunca la imprime
bash tests/security/ha-s01/verify-access.sh
WAIT_FOR_EXPIRY=1 bash tests/security/ha-s01/verify-access.sh   # incluye el caso del token vencido
```

| Variable | Defecto | Descripción |
|---|---|---|
| `TF_VAR_seed_user_password` o `SEED_USER_PASSWORD` | — (obligatoria) | Contraseña de los usuarios semilla |
| `TF_DIR` | `infra/terraform/environments/staging/ha-s01` | Composición de la que se leen las salidas (`terraform output`) |
| `WAIT_FOR_EXPIRY` | `0` | `1` espera a que venza un ID token y comprueba el `401` |
| `PARTNER_ID` | `socio-a` | `custom:partner_id` esperado del usuario socio |
| `COGNITO_URL` | endpoint público de la región | Otro endpoint de Cognito (p. ej. un emulador, D-8) |

Cada línea empieza por `OK`, `FAIL`, `RECORD` (evidencia de D-6, no aprueba ni falla) o `SKIPPED`. El guion termina con
código `1` si algún caso falla. La contraseña, los tokens y la API key no se imprimen: los cuerpos van por la entrada
estándar y las cabeceras por un archivo temporal privado que se borra al terminar.
