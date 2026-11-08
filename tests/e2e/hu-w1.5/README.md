# E2E — HU-W1.5 · Renovación de pólizas en la nube

Verificaciones por API de las historias de renovación del asesor en la ventana de staging, siempre a través del portal de CloudFront
(como lo llama el navegador) y solo con los usuarios permanentes de `bootstrap/identity` (`asesor@example.com`, `asegurado@example.com`).
Las pruebas por componente y por criterio, sin AWS, están en `services/origination/tests/integration` (`expiring_test.go` para
HU-W1.5-01).

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
