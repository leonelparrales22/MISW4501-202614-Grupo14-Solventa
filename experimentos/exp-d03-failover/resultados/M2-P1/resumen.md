## Ejecución `M2-P1`

| Métrica | Valor |
|---|---|
| Solicitudes | 30001 |
| Fallidas | 1334 |
| Disponibilidad de la ejecución | 95.553 % |
| Minutos/mes equivalentes | 1920.9 |
| Ventana de error por falla de cómputo (s) | 31.2 |
| Detección por el monitor (s) | 25.4 |
| Reintegración (s) | 83.3 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 18.2 |
| Recuperación extremo a extremo tras failover (s) | 25.5 |
| Duración del failover de la base (s) | 16.9 |
| Retardo de reconexión del pool (s) | 0.3 |
| Errores esporádicos fuera de las ventanas de falla | 0 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 237.0 |
| p95 fase linea_base, solo exitosas (ms) | 237.0 |
| p95 fase una_instancia, todas (ms) | 5000.0 |
| p95 fase una_instancia, solo exitosas (ms) | 95.0 |
| p95 fase failover_base, todas (ms) | 3083.0 |
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
| 1 | 120.137 | 151.357 | 31.22 | 651 | computo |
| 2 | 367.836 | 386.044 | 18.208 | 683 | base |

| Evento | t (s) | Detalle |
|---|---|---|
| ecs_evento | -9.546 | mensaje=(service expd03-originacion) (deployment ecs-svc/9369312827647202267) deployment completed. |
| servicio_estable | -9.545 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_estado_inicial | -2.579 | instancia=c82e1c9763fb443290548b49cbe28999;ip=10.0.0.197;estado=healthy;razon= |
| target_estado_inicial | -2.579 | instancia=64a0a283c020430f8d098508c65305b4;ip=10.0.1.196;estado=healthy;razon= |
| k6_inicio | -0.053 | config=M2-P1;rps=50;duracion=600;tipo_falla=colgar |
| falla_computo | 120.212 | tipo=colgar;instancia=64a0a283c020430f8d098508c65305b4;ip=10.0.1.196 |
| target_unhealthy | 145.632 | instancia=64a0a283c020430f8d098508c65305b4;ip=10.0.1.196;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 172.224 | instancia=78a5fdc9571e4f2d859fa52f72402ec1 |
| ecs_target_registrado | 201.03 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 202.453 | instancia=78a5fdc9571e4f2d859fa52f72402ec1;ip=10.0.1.225;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 203.558 | instancia=78a5fdc9571e4f2d859fa52f72402ec1;ip=10.0.1.225;estado=healthy;razon= |
| ecs_evento | 209.989 | mensaje=(service expd03-originacion) (task 64a0a283c020430f8d098508c65305b4) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 210.021 | instancia=64a0a283c020430f8d098508c65305b4 |
| ecs_target_registrado | 219.1 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 219.106 | mensaje=(service expd03-originacion, taskSet ecs-svc/9369312827647202267) has begun draining connections on 1 tasks. |
| target_draining | 219.557 | instancia=64a0a283c020430f8d098508c65305b4;ip=10.0.1.196;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 228.285 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 249.294 | instancia=64a0a283c020430f8d098508c65305b4;ip=10.0.1.196 |
| falla_base | 360.588 | metodo=reboot_force_failover |
| base_failover_inicio | 368.814 | mensaje=Multi-AZ instance failover started. |
| base_reiniciada | 385.704 | mensaje=DB instance restarted |
| base_failover_fin | 393.741 | mensaje=Multi-AZ instance failover completed |
| rds_evento | 393.741 | mensaje=The user requested a failover of the DB instance. |
| k6_fin | 600.744 | codigo_salida=0 |
