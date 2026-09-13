## Ejecución `M3-P1`

| Métrica | Valor |
|---|---|
| Solicitudes | 30001 |
| Fallidas | 3108 |
| Disponibilidad de la ejecución | 89.640 % |
| Minutos/mes equivalentes | 4475.4 |
| Ventana de error por falla de cómputo (s) | 99.5 |
| Detección por el monitor (s) | 92.7 |
| Reintegración (s) | 120.2 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 18.2 |
| Recuperación extremo a extremo tras failover (s) | 29.2 |
| Duración del failover de la base (s) | 17.0 |
| Retardo de reconexión del pool (s) | -0.6 |
| Errores esporádicos fuera de las ventanas de falla | 0 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 236.0 |
| p95 fase linea_base, solo exitosas (ms) | 236.0 |
| p95 fase una_instancia, todas (ms) | 5001.0 |
| p95 fase una_instancia, solo exitosas (ms) | 94.0 |
| p95 fase failover_base, todas (ms) | 3084.0 |
| p95 fase failover_base, solo exitosas (ms) | 96.0 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | no |
| (a2) sin falsos positivos del monitor | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **no** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 120.157 | 219.677 | 99.52 | 2348 | computo |
| 2 | 371.578 | 389.809 | 18.231 | 760 | base |

| Evento | t (s) | Detalle |
|---|---|---|
| ecs_evento | -12.564 | mensaje=(service expd03-originacion) (deployment ecs-svc/2761135752602519683) deployment completed. |
| servicio_estable | -12.563 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_estado_inicial | -2.582 | instancia=24bebf893db844819f917736a8eb233a;ip=10.0.1.168;estado=healthy;razon= |
| target_estado_inicial | -2.581 | instancia=60eacbd204c14b2894b691b133e76339;ip=10.0.0.25;estado=healthy;razon= |
| k6_inicio | -0.054 | config=M3-P1;rps=50;duracion=600;tipo_falla=colgar |
| falla_computo | 120.195 | tipo=colgar;instancia=24bebf893db844819f917736a8eb233a;ip=10.0.1.168 |
| target_unhealthy | 212.892 | instancia=24bebf893db844819f917736a8eb233a;ip=10.0.1.168;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 218.764 | instancia=74b04697ac4a4cc599ecb06dc000994d |
| ecs_target_registrado | 238.264 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_healthy | 240.415 | instancia=74b04697ac4a4cc599ecb06dc000994d;ip=10.0.1.82;estado=healthy;razon= |
| ecs_evento | 246.767 | mensaje=(service expd03-originacion) (task 24bebf893db844819f917736a8eb233a) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 246.799 | instancia=24bebf893db844819f917736a8eb233a |
| ecs_target_registrado | 256.3 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 256.305 | mensaje=(service expd03-originacion, taskSet ecs-svc/2761135752602519683) has begun draining connections on 1 tasks. |
| target_draining | 257.409 | instancia=24bebf893db844819f917736a8eb233a;ip=10.0.1.168;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 265.894 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 286.458 | instancia=24bebf893db844819f917736a8eb233a;ip=10.0.1.168 |
| falla_base | 360.576 | metodo=reboot_force_failover |
| base_failover_inicio | 373.379 | mensaje=Multi-AZ instance failover started. |
| base_reiniciada | 390.41 | mensaje=DB instance restarted |
| base_failover_fin | 418.321 | mensaje=Multi-AZ instance failover completed |
| rds_evento | 418.321 | mensaje=The user requested a failover of the DB instance. |
| k6_fin | 600.721 | codigo_salida=0 |
