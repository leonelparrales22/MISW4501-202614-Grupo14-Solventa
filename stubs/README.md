# `stubs` — simuladores del ecosistema externo

El proyecto no integra proveedores reales (restricción de alcance). Cada subcarpeta es un módulo Go que simula un tercero, con latencia y fallos configurables para los experimentos (junto con Toxiproxy).

**Estado:** `openfinance` implementado (HA-D01); `opendata` y `kyc` implementados (HA-L01); `email` (HU-W1.3-03); `payment-gateway` (HA-D02); el resto de carpetas sigue reservado.

| Stub | Simula |
|---|---|
| `openfinance` | Datos financieros consentidos (Open Finance): `GET /profiles/{id}` determinista por cliente y `GET /health`; `PORT` (9000) y `ARTIFICIAL_DELAY_MS` opcionales |
| `opendata` | Fuentes públicas (clima, geolocalización, listas oficiales): `GET /profiles/{id}` determinista (señal distinta a la de Open Finance) y `GET /health`; `PORT` (9010) y `ARTIFICIAL_DELAY_MS` opcionales |
| `kyc` | Verificación de identidad, listas restrictivas y AML: `GET /profiles/{id}` determinista (señal propia) y `GET /health`; `PORT` (9020) y `ARTIFICIAL_DELAY_MS` opcionales |
| `email` | Proveedor de correo: `POST /messages` (cabecera `Idempotency-Key` obligatoria; 201 si es nuevo, 200 si ya lo había aceptado) guarda el mensaje en memoria, `GET /messages?to=` lo lista para verificar lo que recibiría el asegurado y `GET /health`; `PORT` (9030), `ARTIFICIAL_DELAY_MS` y `FAIL_FIRST` (las primeras N peticiones responden 503) opcionales. **No se publica en ECR todavía:** su repositorio no existe en `infra/terraform/bootstrap/cd-ecr`; hasta agregarlo allí y a `scripts/ci/changed-images.sh`, se usa en local y en pruebas |
| `payment-gateway` | Cobro de primas y pago de indemnizaciones (HA-D02): `POST /charges` con la cabecera `Idempotency-Key` obligatoria registra y procesa el cobro **antes** de responder (si Toxiproxy corta la respuesta, el cobro ya ocurrió); la misma clave devuelve el mismo resultado sin cobrar otra vez (201 la primera vez, 200 después) y con otro monto responde 422 `idempotency_key_reused`; una referencia que contiene `declined` se rechaza con 402 `payment_declined` y `decline_code` 51. `GET /debug/charges` separa las solicitudes recibidas de los cobros reales por clave (la evidencia de 0 perdidos y 0 duplicados), `POST /debug/reset` y `GET /health`. `PORT` (9040; el 9030 es del stub `email`), `ARTIFICIAL_DELAY_MS` (después de registrar el cobro) y `SUPPORTS_IDEMPOTENCY=false` (simula un proveedor que no respeta la clave, para el contrafáctico) opcionales. Requisito para cualquier proveedor real: respetar la `Idempotency-Key` (decisión D-10). Igual que `email`, **no se publica en ECR todavía** |
| `esignature` | Firma electrónica y notificación legal |
| `iot` | Telemetría/IoT (clima, vuelos) para eventos paramétricos |
| `embedded-partner` | Un socio de distribución que consume las APIs |

Los servicios nunca importan un stub: lo invocan por HTTP a través de los adaptadores de `pkg/adapters`.

El `Makefile` no corre lint ni pruebas sobre `stubs/`; se ejecutan aparte, por ejemplo
`golangci-lint run ./stubs/opendata/... ./stubs/kyc/...` y `go test -cover ./stubs/opendata/... ./stubs/kyc/...`.
