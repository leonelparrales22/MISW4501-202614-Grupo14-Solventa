## Ejecución `M1-P1-rep-r1`

| Métrica | Valor |
|---|---|
| Solicitudes | 30000 |
| Fallidas | 1168 |
| Disponibilidad de la ejecución | 96.107 % |
| Minutos/mes equivalentes | 1681.9 |
| Ventana de error por falla de cómputo (s) | 18.7 |
| Detección por el monitor (s) | 12.5 |
| Reintegración (s) | 77.3 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 18.2 |
| Recuperación extremo a extremo tras failover (s) | 26.1 |
| Duración del failover de la base (s) | 17.8 |
| Retardo de reconexión del pool (s) | -1.8 |
| Errores esporádicos fuera de las ventanas de falla | 81 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 327.0 |
| p95 fase linea_base, solo exitosas (ms) | 296.4 |
| p95 fase una_instancia, todas (ms) | 175.0 |
| p95 fase una_instancia, solo exitosas (ms) | 147.0 |
| p95 fase failover_base, todas (ms) | 3105.0 |
| p95 fase failover_base, solo exitosas (ms) | 150.0 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | sí |
| (a2) sin falsos positivos del monitor | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **sí** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 0.097 | 10.817 | 10.72 | 58 | esporadico |
| 2 | 10.015 | 21.377 | 11.361 | 13 | esporadico |
| 3 | 22.837 | 30.496 | 7.66 | 3 | esporadico |
| 4 | 35.476 | 41.377 | 5.9 | 2 | esporadico |
| 5 | 46.316 | 52.217 | 5.901 | 2 | esporadico |
| 6 | 57.176 | 62.177 | 5.001 | 1 | esporadico |
| 7 | 120.177 | 138.856 | 18.68 | 329 | computo |
| 8 | 196.937 | 197.041 | 0.104 | 1 | esporadico |
| 9 | 354.576 | 354.673 | 0.097 | 1 | esporadico |
| 10 | 368.537 | 386.771 | 18.234 | 758 | base |

| Minuto | Solicitudes | req/s | Fallidas | p95 exitosas (ms) | Transiciones a unhealthy |
|---|---|---|---|---|---|
| 0 | 3001 | 50.0 | 79 | 352.0 | 0 |
| 1 | 3000 | 50.0 | 0 | 148.0 | 0 |
| 2 | 3000 | 50.0 | 329 | 148.0 | 1 |
| 3 | 3000 | 50.0 | 1 | 139.0 | 0 |
| 4 | 3000 | 50.0 | 0 | 165.0 | 0 |
| 5 | 3000 | 50.0 | 1 | 143.0 | 0 |
| 6 | 3000 | 50.0 | 758 | 858.8 | 0 |
| 7 | 3000 | 50.0 | 0 | 139.0 | 0 |
| 8 | 3000 | 50.0 | 0 | 139.0 | 0 |
| 9 | 2999 | 50.0 | 0 | 155.0 | 0 |

| Evento | t (s) | Detalle |
|---|---|---|
| ecs_evento | -11.033 | mensaje=(service expd03-originacion) (deployment ecs-svc/2167165345548783105) deployment completed. |
| servicio_estable | -11.032 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_estado_inicial | -2.369 | instancia=11d3880007bf4a3e8a38850bc0936216;ip=10.0.1.49;estado=healthy;razon= |
| target_estado_inicial | -2.368 | instancia=a70980ed7d7945488c70f1ba568127b8;ip=10.0.0.26;estado=healthy;razon= |
| k6_inicio | -0.053 | config=M1-P1;rps=50;duracion=600;tipo_falla=hang |
| falla_computo | 120.251 | tipo=hang;instancia=11d3880007bf4a3e8a38850bc0936216;ip=10.0.1.49 |
| target_unhealthy | 132.746 | instancia=11d3880007bf4a3e8a38850bc0936216;ip=10.0.1.49;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 175.933 | instancia=e8a2d5a475a84d52873a501c77edab17 |
| ecs_target_registrado | 195.335 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_healthy | 197.548 | instancia=e8a2d5a475a84d52873a501c77edab17;ip=10.0.0.27;estado=healthy;razon= |
| ecs_evento | 204.482 | mensaje=(service expd03-originacion) (task 11d3880007bf4a3e8a38850bc0936216) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 204.518 | instancia=11d3880007bf4a3e8a38850bc0936216 |
| ecs_target_registrado | 214.795 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 214.801 | mensaje=(service expd03-originacion, taskSet ecs-svc/2167165345548783105) has begun draining connections on 1 tasks. |
| target_draining | 215.334 | instancia=11d3880007bf4a3e8a38850bc0936216;ip=10.0.1.49;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 224.57 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 246.842 | instancia=11d3880007bf4a3e8a38850bc0936216;ip=10.0.1.49 |
| falla_base | 360.65 | metodo=reboot_force_failover |
| base_failover_inicio | 370.794 | mensaje=Multi-AZ instance failover started. |
| base_reiniciada | 388.548 | mensaje=DB instance restarted |
| base_failover_fin | 415.633 | mensaje=Multi-AZ instance failover completed |
| rds_evento | 415.633 | mensaje=The user requested a failover of the DB instance. |
| k6_fin | 601.298 | codigo_salida=0 |
