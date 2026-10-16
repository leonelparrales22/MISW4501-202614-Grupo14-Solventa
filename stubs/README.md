# `stubs` — simuladores del ecosistema externo

El proyecto no integra proveedores reales (restricción de alcance). Cada subcarpeta es un módulo Go que simula un tercero, con latencia y fallos configurables para los experimentos (junto con Toxiproxy).

**Estado:** `openfinance` implementado (HA-D01); `opendata` y `kyc` implementados (HA-L01); `email` (HU-W1.3-03); el resto de carpetas sigue reservado.

| Stub | Simula |
|---|---|
| `openfinance` | Datos financieros consentidos (Open Finance): `GET /profiles/{id}` determinista por cliente y `GET /health`; `PORT` (9000) y `ARTIFICIAL_DELAY_MS` opcionales |
| `opendata` | Fuentes públicas (clima, geolocalización, listas oficiales): `GET /profiles/{id}` determinista (señal distinta a la de Open Finance) y `GET /health`; `PORT` (9010) y `ARTIFICIAL_DELAY_MS` opcionales |
| `kyc` | Verificación de identidad, listas restrictivas y AML: `GET /profiles/{id}` determinista (señal propia) y `GET /health`; `PORT` (9020) y `ARTIFICIAL_DELAY_MS` opcionales |
| `email` | Proveedor de correo: `POST /messages` (cabecera `Idempotency-Key` obligatoria; 201 si es nuevo, 200 si ya lo había aceptado) guarda el mensaje en memoria, `GET /messages?to=` lo lista para verificar lo que recibiría el asegurado y `GET /health`; `PORT` (9030), `ARTIFICIAL_DELAY_MS` y `FAIL_FIRST` (las primeras N peticiones responden 503) opcionales. **No se publica en ECR todavía:** su repositorio no existe en `infra/terraform/bootstrap/cd-ecr`; hasta agregarlo allí y a `scripts/ci/changed-images.sh`, se usa en local y en pruebas |
| `payment-gateway` | Cobro de primas y pago de indemnizaciones |
| `esignature` | Firma electrónica y notificación legal |
| `iot` | Telemetría/IoT (clima, vuelos) para eventos paramétricos |
| `embedded-partner` | Un socio de distribución que consume las APIs |

Los servicios nunca importan un stub: lo invocan por HTTP a través de los adaptadores de `pkg/adapters`.

El `Makefile` no corre lint ni pruebas sobre `stubs/`; se ejecutan aparte, por ejemplo
`golangci-lint run ./stubs/opendata/... ./stubs/kyc/...` y `go test -cover ./stubs/opendata/... ./stubs/kyc/...`.
