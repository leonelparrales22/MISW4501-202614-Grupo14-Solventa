## Ejecución `C4-tres-tareas`

| Métrica | Valor |
|---|---|
| Solicitudes | 30000 |
| Fallidas | 2162 |
| Disponibilidad de la ejecución | 92.793 % |
| Minutos/mes equivalentes | 3113.3 |
| Ventana de error por falla de cómputo (s) | 19.3 |
| Detección por el monitor (s) | 13.2 |
| Reintegración (s) | 48.8 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 41.6 |
| Recuperación extremo a extremo tras failover (s) | 79.7 |
| Duración del failover de la base (s) | 16.9 |
| Retardo de reconexión del pool (s) | 46.9 |
| Errores esporádicos fuera de las ventanas de falla | 86 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 19 |
| p95 fase linea_base, todas (ms) | 404.4 |
| p95 fase linea_base, solo exitosas (ms) | 337.8 |
| p95 fase una_instancia, todas (ms) | 147.0 |
| p95 fase una_instancia, solo exitosas (ms) | 140.0 |
| p95 fase failover_base, todas (ms) | 5000.0 |
| p95 fase failover_base, solo exitosas (ms) | 562.0 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | sí |
| (a2) sin falsos positivos del monitor | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **sí** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 0.076 | 10.917 | 10.84 | 69 | esporadico |
| 2 | 10.297 | 21.537 | 11.24 | 14 | esporadico |
| 3 | 23.097 | 28.937 | 5.84 | 2 | esporadico |
| 4 | 120.137 | 139.457 | 19.32 | 230 | computo |
| 5 | 375.037 | 393.224 | 18.187 | 760 | base |
| 6 | 405.477 | 428.616 | 23.14 | 1077 | base |
| 7 | 440.077 | 440.323 | 0.247 | 9 | base |
| 8 | 491.637 | 491.734 | 0.097 | 1 | esporadico |

| Minuto | Solicitudes | req/s | Fallidas | p95 exitosas (ms) | Transiciones a unhealthy |
|---|---|---|---|---|---|
| 0 | 3001 | 50.0 | 85 | 419.0 | 0 |
| 1 | 3000 | 50.0 | 0 | 146.0 | 0 |
| 2 | 3000 | 50.0 | 230 | 141.0 | 1 |
| 3 | 3000 | 50.0 | 0 | 146.0 | 0 |
| 4 | 3000 | 50.0 | 0 | 139.0 | 0 |
| 5 | 3000 | 50.0 | 0 | 138.0 | 0 |
| 6 | 3000 | 50.0 | 1481 | 768.4 | 0 |
| 7 | 3000 | 50.0 | 365 | 628.0 | 0 |
| 8 | 3000 | 50.0 | 1 | 107.0 | 0 |
| 9 | 2999 | 50.0 | 0 | 95.0 | 0 |

| Evento | t (s) | Detalle |
|---|---|---|
| tarea_nueva | -29.051 | instancia=3bd032cdc8464b328561a867c33965c9 |
| ecs_target_registrado | -10.108 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | -2.436 | instancia=3bd032cdc8464b328561a867c33965c9;ip=10.0.1.154;estado=healthy;razon= |
| target_estado_inicial | -2.435 | instancia=e8a2d5a475a84d52873a501c77edab17;ip=10.0.0.27;estado=healthy;razon= |
| target_estado_inicial | -2.435 | instancia=1a8020cbfee04861b6cf02894753bace;ip=10.0.1.180;estado=healthy;razon= |
| servicio_estable | -0.77 | mensaje=(service expd03-originacion) has reached a steady state. |
| k6_inicio | -0.056 | config=C4-tres-tareas;rps=50;duracion=600;tipo_falla=hang |
| falla_computo | 120.228 | tipo=hang;instancia=1a8020cbfee04861b6cf02894753bace;ip=10.0.1.180 |
| target_unhealthy | 133.447 | instancia=1a8020cbfee04861b6cf02894753bace;ip=10.0.1.180;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 137.432 | instancia=d24d46e91c7343b8af2bfeee0c06a1ea |
| ecs_target_registrado | 165.606 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 166.357 | instancia=d24d46e91c7343b8af2bfeee0c06a1ea;ip=10.0.0.236;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 169.024 | instancia=d24d46e91c7343b8af2bfeee0c06a1ea;ip=10.0.0.236;estado=healthy;razon= |
| ecs_evento | 174.355 | mensaje=(service expd03-originacion) (task 1a8020cbfee04861b6cf02894753bace) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 174.412 | instancia=1a8020cbfee04861b6cf02894753bace |
| ecs_target_registrado | 184.091 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 184.097 | mensaje=(service expd03-originacion, taskSet ecs-svc/2167165345548783105) has begun draining connections on 1 tasks. |
| target_draining | 184.947 | instancia=1a8020cbfee04861b6cf02894753bace;ip=10.0.1.180;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 193.48 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 214.792 | instancia=1a8020cbfee04861b6cf02894753bace;ip=10.0.1.180 |
| falla_base | 360.59 | metodo=reboot_force_failover |
| base_failover_inicio | 376.515 | mensaje=Multi-AZ instance failover started. |
| base_reiniciada | 393.417 | mensaje=DB instance restarted |
| rds_evento | 431.359 | mensaje=The user requested a failover of the DB instance. |
| base_failover_fin | 431.359 | mensaje=Multi-AZ instance failover completed |
| k6_fin | 600.748 | codigo_salida=0 |
