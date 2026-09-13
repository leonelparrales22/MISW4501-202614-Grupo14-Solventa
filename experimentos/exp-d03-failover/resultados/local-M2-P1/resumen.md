## Corrida `local-M2-P1`

| Métrica | Valor |
|---|---|
| Solicitudes | 30001 |
| Fallidas | 3014 |
| Disponibilidad de la corrida | 89.954 % |
| Minutos/mes equivalentes | 4340.0 |
| Ventana de error por falla de cómputo (s) | 31.2 |
| Detección por el monitor (s) | 26.0 |
| Reintegración (s) | 62.1 |
| Falsas alarmas | 0 |
| Ventana de error por failover de base (s) | 47.5 |
| Recuperación extremo a extremo tras failover (s) | 46.7 |
| Duración del failover de la base (s) | 46.0 |
| Brecha atribuible al pool (s) | 0.7 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 4.0 |
| p95 fase linea_base, solo exitosas (ms) | 4.0 |
| p95 fase una_instancia, todas (ms) | 5000.0 |
| p95 fase una_instancia, solo exitosas (ms) | 4.0 |
| p95 fase failover_base, todas (ms) | 3002.0 |
| p95 fase failover_base, solo exitosas (ms) | 4.0 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | no |
| (a2) sin falsas alarmas | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **no** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 120.411 | 151.568 | 31.158 | 652 | computo |
| 2 | 360.69 | 408.155 | 47.465 | 2362 | base |

| Evento | t (s) | Detalle |
|---|---|---|
| k6_inicio | 0.177 | config=M2-P1;rps=50;duracion=600 |
| falla_computo | 120.413 | tipo=colgar;instancia=a |
| monitor_no_sano | 146.462 | instancia=a;fuente=haproxy |
| reemplazo_iniciado | 172.031 | instancia=a;metodo=docker_restart |
| monitor_sano | 182.494 | instancia=a;fuente=haproxy |
| falla_base | 361.449 | metodo=docker_kill |
| base_restablecida | 407.411 | metodo=docker_start |
| k6_fin | 608.652 |  |
