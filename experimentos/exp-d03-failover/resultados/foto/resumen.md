## Ejecución `foto`

| Métrica | Valor |
|---|---|
| Solicitudes | 4500 |
| Fallidas | 1115 |
| Disponibilidad de la ejecución | 75.222 % |
| Minutos/mes equivalentes | 10704.0 |
| Ventana de error por falla de cómputo (s) | 16.8 |
| Detección por el monitor (s) | 11.8 |
| Reintegración (s) | n/d |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 18.8 |
| Recuperación extremo a extremo tras failover (s) | 18.5 |
| Duración del failover de la base (s) | 15.5 |
| Retardo de reconexión del pool (s) | 3.0 |
| Errores esporádicos fuera de las ventanas de falla | 0 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 4.0 |
| p95 fase linea_base, solo exitosas (ms) | 4.0 |
| p95 fase una_instancia, todas (ms) | 5001.0 |
| p95 fase una_instancia, solo exitosas (ms) | 4.0 |
| p95 fase failover_base, todas (ms) | 3002.0 |
| p95 fase failover_base, solo exitosas (ms) | 607.1 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | sí |
| (a2) sin falsos positivos del monitor | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **sí** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 20.277 | 37.058 | 16.78 | 298 | computo |
| 2 | 50.117 | 68.898 | 18.781 | 817 | base |

| Minuto | Solicitudes | req/s | Fallidas | p95 exitosas (ms) | Transiciones a unhealthy |
|---|---|---|---|---|---|
| 0 | 3011 | 50.2 | 798 | 4.0 | 1 |
| 1 | 1489 | 24.8 | 317 | 607.1 | 0 |

| Evento | t (s) | Detalle |
|---|---|---|
| k6_inicio | -0.081 | config=M1;rps=50;duracion=90 |
| falla_computo | 20.243 | tipo=hang;instancia=a |
| monitor_unhealthy | 32.093 | instancia=a;fuente=haproxy |
| reemplazo_iniciado | 46.91 | instancia=a;metodo=docker_restart |
| falla_base | 50.406 | metodo=docker_kill |
| monitor_healthy | 55.102 | instancia=a;fuente=haproxy |
| base_restablecida | 65.923 | metodo=docker_start |
| k6_fin | 98.245 |  |
