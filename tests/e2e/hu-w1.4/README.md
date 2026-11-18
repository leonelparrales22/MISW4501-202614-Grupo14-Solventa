# E2E — HU-W1.4 · Modificaciones de la póliza

Pruebas de punta a punta de la solicitud de modificación del asegurado (HU-W1.4-01) en la ventana de staging, siempre a través del
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
