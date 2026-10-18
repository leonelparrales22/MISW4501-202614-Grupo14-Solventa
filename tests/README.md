# `tests` — pruebas transversales

Las pruebas unitarias, de integración y de contrato viven en cada servicio (`services/<service>/tests`). Aquí van las que cruzan servicios.

**Estado:** `resilience/ha-d01`, `resilience/ha-d02`, `load-k6/ha-l01` y `security/ha-s01` implementados; el resto de carpetas sigue reservado.

| Carpeta | Herramienta | Objetivo |
|---|---|---|
| `load-k6` | k6 | Carga escalonada de 500 a 50.000 cotizaciones/min; p95 ≤ 250 ms. [`ha-l01`](load-k6/ha-l01/README.md): perfilamiento con las tres fuentes en paralelo bajo la rampa de EXP-L01 (8 → 83 req/s) con Toxiproxy y contrato con Schemathesis; en local es humo, en staging mide p95 ≤ 400 ms / p99 ≤ 800 ms (CA-1 de HA-L01) |
| `resilience` | Toxiproxy | Latencia y caídas inyectadas en dependencias. [`ha-d01`](resilience/ha-d01/README.md): caída y lentitud de Open Finance con k6. [`ha-d02`](resilience/ha-d02/README.md): cobro de la prima con la pasarela caída, respuesta ambigua, reinicio de originación y ventana vencida (PostgreSQL, k6, Moto y Schemathesis), con el medio de pago PSE de HU-W1.3-02; 0 cobros perdidos y 0 duplicados. Su `staging/smoke.sh` es el humo de la ventana de staging (cobro con PSE, bandeja, documento en S3 y enlace público por el API Gateway), ensayado contra el mismo ambiente local |
| `security` | OWASP ZAP, curl | Análisis dinámico (DAST) sobre las APIs desplegadas. [`ha-s01`](security/ha-s01/README.md): verificación de acceso en la ventana de staging de HA-S01 (401 del authorizer de Cognito, alta solo administrativa, revocación y separación de la API key de los socios); [`ha-s01/local`](security/ha-s01/local/README.md): Cognito local con Moto (Docker) para el ciclo manual y la prueba de integración opcional de `pkg/security` |
| `e2e` | — | Flujos completos entre servicios |

Estrategia completa: [Estrategia de pruebas](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Estrategia-de-Pruebas-v-1.2). Cobertura mínima de unitarias: 70 %.
