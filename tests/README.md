# `tests` — pruebas transversales

Las pruebas unitarias, de integración y de contrato viven en cada servicio (`services/<service>/tests`). Aquí van las que cruzan servicios.

**Estado:** carpetas reservadas; sin código todavía.

| Carpeta | Herramienta | Objetivo |
|---|---|---|
| `load-k6` | k6 | Carga escalonada de 500 a 50.000 cotizaciones/min; p95 ≤ 250 ms |
| `resilience` | Toxiproxy | Latencia y caídas inyectadas en dependencias |
| `security` | OWASP ZAP | Análisis dinámico (DAST) sobre las APIs desplegadas |
| `e2e` | — | Flujos completos entre servicios |

Estrategia completa: [Estrategia de pruebas](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Estrategia-de-Pruebas-v-1.2). Cobertura mínima de unitarias: 70 %.
