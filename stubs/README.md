# `stubs` — simuladores del ecosistema externo

El proyecto no integra proveedores reales (restricción de alcance). Cada subcarpeta es un módulo Go que simula un tercero, con latencia y fallos configurables para los experimentos (junto con Toxiproxy).

**Estado:** carpetas reservadas; sin código todavía.

| Stub | Simula |
|---|---|
| `openfinance` | Datos financieros consentidos (Open Finance) |
| `opendata` | Fuentes públicas (clima, geolocalización, listas oficiales) |
| `kyc` | Verificación de identidad, listas restrictivas y AML |
| `payment-gateway` | Cobro de primas y pago de indemnizaciones |
| `esignature` | Firma electrónica y notificación legal |
| `iot` | Telemetría/IoT (clima, vuelos) para eventos paramétricos |
| `embedded-partner` | Un socio de distribución que consume las APIs |

Los servicios nunca importan un stub: lo invocan por HTTP a través de los adaptadores de `pkg/adapters`.
