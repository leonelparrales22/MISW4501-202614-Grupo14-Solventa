# `tests` — pruebas transversales

Las pruebas unitarias, de integración y de contrato viven en cada servicio (`services/<service>/tests`). Aquí van las que cruzan servicios.

**Estado:** `resilience/ha-d01` y `load-k6/ha-l01` implementados; el resto de carpetas sigue reservado.

| Carpeta | Herramienta | Objetivo |
|---|---|---|
| `load-k6` | k6 | Carga escalonada de 500 a 50.000 cotizaciones/min; p95 ≤ 250 ms. [`ha-l01`](load-k6/ha-l01/README.md): perfilamiento con las tres fuentes en paralelo bajo la rampa de EXP-L01 (8 → 83 req/s) con Toxiproxy y contrato con Schemathesis; en local es humo, en staging mide p95 ≤ 400 ms / p99 ≤ 800 ms (CA-1 de HA-L01) |
| `resilience` | Toxiproxy | Latencia y caídas inyectadas en dependencias. [`ha-d01`](resilience/ha-d01/README.md): caída y lentitud de Open Finance con k6 |
| `security` | OWASP ZAP | Análisis dinámico (DAST) sobre las APIs desplegadas |
| `e2e` | — | Flujos completos entre servicios |

Estrategia completa: [Estrategia de pruebas](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Estrategia-de-Pruebas-v-1.2). Cobertura mínima de unitarias: 70 %.
