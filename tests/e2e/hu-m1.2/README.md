# Ensayo local — HU-M1.2-01 · Billetera de pólizas con consulta sin conexión

*Backend* local para recorrer la historia en el emulador de Android con la app móvil: el servicio **real** `origination` y el stub
`policy-core` sembrado con las pólizas del prototipo para la cuenta del asegurado. Las rutas humanas verifican los tokens del *user pool*
permanente (los valores públicos de `app/build.gradle.kts` del móvil), así que el asegurado inicia sesión en el emulador y la contraseña
nunca pasa por el script.

```
SUBJECT=<sub de asegurado@example.com> bash tests/e2e/hu-m1.2/app-local.sh
```

El `sub` se lee, de solo lectura, con
`aws cognito-idp admin-get-user --user-pool-id us-east-2_EWwg8NL2C --username asegurado@example.com` (atributo `sub`). El script escribe
la semilla en `generated/policy-seed.json` (ignorado por git), levanta el entorno e imprime cómo construir el APK con `URL_API_LOCAL`.

| Póliza (prototipo) | Línea | Estado sembrado | Para qué |
|---|---|---|---|
| VH-2026-004512 · Vida hipotecario | `life` | activa, 20 años | Valor asegurado de $ 250.000.000 |
| AU-2025-118204 · Autos · KJT-482 | `auto` | activa, **vence en 16 días** | «Por vencer», como el aviso del prototipo |
| VJ-2026-000931 · Viaje · retraso de vuelo | `travel` | activa | Paramétrica de $ 180.000 |
| HG-2025-880231 · Hogar | `home` | activa | $ 320.000.000 |
| AU-2024-093377 · Autos · KJT-482 | `auto` | cancelada | La tarjeta «No pago · Sin cobertura» |

Las fechas se anclan al día de hoy para que los estados de las pantallas valgan cualquier día. El asegurado principal es «Carolina Pérez
Ruiz», CC `DOCUMENT` (por omisión `1020304050`, el mismo del ensayo de HU-M1.3).

## Recorrido de los criterios

| Criterio | Cómo se ve |
|---|---|
| CA-1 y CA-2 | Con red, abrir **Pólizas** en la barra inferior: la lista se sincroniza y se guarda. Activar el modo avión y volver a abrirla: el aviso «Sin conexión» con la fecha y hora de la copia; cada póliza abre su detalle (número, tipo, vigencia, coberturas con montos y asegurado principal) |
| CA-3 | Con «Mis pólizas» abierta y sin red, desactivar el modo avión: la lista se actualiza sola y cambia la hora de «Última sincronización» |
| CA-4 | `SUBJECT=<el mismo> bash tests/e2e/hu-m1.2/app-local.sh --changes` renueva AU-2025-118204 por un año y cancela HG-2025-880231 en el núcleo; en la siguiente sincronización la app muestra «Sincronización completada · 2 cambios» |
| CA-5 | Ver la prueba unitaria del móvil: la advertencia sale cuando la copia tiene más de 30 días |
| CA-6 | La copia se guarda cifrada con una clave del Android Keystore: en `adb shell run-as co.solventa.movil sqlite3 databases/solventa.db 'select * from poliza'` el contenido no se lee |

Se detiene con `docker compose -f tests/e2e/hu-m1.2/docker-compose.yml down -v`. Puertos (solo `127.0.0.1`, para no chocar con
`hu-m1.3`): `origination` 8392 y `policy-core` 9392. Sin `DATABASE_URL` ni pasarela, originación no emite pólizas aquí: la lista es la del
núcleo. Las pruebas por componente están en `services/origination/internal/wallet`, `internal/adapters/policycore` y
`internal/adapters/http/wallet_test.go`.
