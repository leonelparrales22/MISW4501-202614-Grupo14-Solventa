## Corrida `humo1`

| Métrica | Valor |
|---|---|
| Solicitudes | 5909 |
| Fallidas | 1206 |
| Disponibilidad de la corrida | 79.590 % |
| Minutos/mes equivalentes | 8816.9 |
| Ventana de error por falla de cómputo (s) | 16.4 |
| Detección por el monitor (s) | 11.4 |
| Reintegración (s) | 34.4 |
| Falsas alarmas | 0 |
| Ventana de error por failover de base (s) | 22.7 |
| Recuperación extremo a extremo tras failover (s) | 22.2 |
| Duración del failover de la base (s) | 20.8 |
| Brecha atribuible al pool (s) | 1.4 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 1 |
| p95 fase linea_base, todas (ms) | 4.0 |
| p95 fase linea_base, solo exitosas (ms) | 4.0 |
| p95 fase una_instancia, todas (ms) | 5001.0 |
| p95 fase una_instancia, solo exitosas (ms) | 4.0 |
| p95 fase failover_base, todas (ms) | 3121.0 |
| p95 fase failover_base, solo exitosas (ms) | 1712.5 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | sí |
| (a2) sin falsas alarmas | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **sí** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 30.77 | 47.151 | 16.38 | 256 | computo |
| 2 | 75.849 | 98.55 | 22.701 | 950 | base |

| Evento | t (s) | Detalle |
|---|---|---|
| k6_inicio | 0.327 | config=humo;rps=50;duracion=120 |
| falla_computo | 30.763 | tipo=colgar;instancia=a |
| monitor_no_sano | 42.16 | instancia=a;fuente=haproxy |
| reemplazo_iniciado | 57.5 | instancia=a;metodo=docker_restart |
| monitor_sano | 65.168 | instancia=a;fuente=haproxy |
| falla_base | 76.389 | metodo=docker_kill |
| base_restablecida | 97.141 | metodo=docker_start |
| k6_fin | 128.598 |  |
