## Ejecución `M1-P1-rep-r2`

| Métrica | Valor |
|---|---|
| Solicitudes | 30000 |
| Fallidas | 1179 |
| Disponibilidad de la ejecución | 96.070 % |
| Minutos/mes equivalentes | 1697.8 |
| Ventana de error por falla de cómputo (s) | 17.0 |
| Detección por el monitor (s) | 11.3 |
| Reintegración (s) | 51.9 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 18.2 |
| Recuperación extremo a extremo tras failover (s) | 28.9 |
| Duración del failover de la base (s) | 16.9 |
| Retardo de reconexión del pool (s) | -1.9 |
| Errores esporádicos fuera de las ventanas de falla | 96 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 838.8 |
| p95 fase linea_base, solo exitosas (ms) | 520.0 |
| p95 fase una_instancia, todas (ms) | 295.9 |
| p95 fase una_instancia, solo exitosas (ms) | 174.0 |
| p95 fase failover_base, todas (ms) | 3099.0 |
| p95 fase failover_base, solo exitosas (ms) | 411.9 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | sí |
| (a2) sin falsos positivos del monitor | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **sí** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 0.017 | 21.477 | 21.459 | 89 | esporadico |
| 2 | 25.317 | 32.117 | 6.8 | 4 | esporadico |
| 3 | 119.936 | 136.917 | 16.98 | 287 | computo |
| 4 | 367.337 | 367.438 | 0.101 | 1 | esporadico |
| 5 | 371.456 | 389.663 | 18.208 | 796 | base |
| 6 | 534.117 | 536.925 | 2.808 | 2 | esporadico |

| Minuto | Solicitudes | req/s | Fallidas | p95 exitosas (ms) | Transiciones a unhealthy |
|---|---|---|---|---|---|
| 0 | 3001 | 50.0 | 93 | 1082.6 | 0 |
| 1 | 3000 | 50.0 | 1 | 153.1 | 0 |
| 2 | 3000 | 50.0 | 286 | 152.0 | 1 |
| 3 | 3000 | 50.0 | 0 | 168.0 | 0 |
| 4 | 3000 | 50.0 | 0 | 158.0 | 0 |
| 5 | 3000 | 50.0 | 0 | 357.0 | 0 |
| 6 | 3000 | 50.0 | 797 | 220.9 | 0 |
| 7 | 3000 | 50.0 | 0 | 467.0 | 0 |
| 8 | 3000 | 50.0 | 2 | 488.2 | 0 |
| 9 | 2999 | 50.0 | 0 | 152.0 | 0 |

| Evento | t (s) | Detalle |
|---|---|---|
| target_estado_inicial | -2.182 | instancia=e8a2d5a475a84d52873a501c77edab17;ip=10.0.0.27;estado=healthy;razon= |
| target_estado_inicial | -2.182 | instancia=a70980ed7d7945488c70f1ba568127b8;ip=10.0.0.26;estado=healthy;razon= |
| k6_inicio | -0.061 | config=M1-P1;rps=50;duracion=600;tipo_falla=hang |
| falla_computo | 120.281 | tipo=hang;instancia=a70980ed7d7945488c70f1ba568127b8;ip=10.0.0.26 |
| target_unhealthy | 131.557 | instancia=a70980ed7d7945488c70f1ba568127b8;ip=10.0.0.26;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 140.734 | instancia=1a8020cbfee04861b6cf02894753bace |
| ecs_target_registrado | 170.126 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 171.095 | instancia=1a8020cbfee04861b6cf02894753bace;ip=10.0.1.180;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 172.212 | instancia=1a8020cbfee04861b6cf02894753bace;ip=10.0.1.180;estado=healthy;razon= |
| ecs_evento | 179.178 | mensaje=(service expd03-originacion) (task a70980ed7d7945488c70f1ba568127b8) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 179.216 | instancia=a70980ed7d7945488c70f1ba568127b8 |
| ecs_target_registrado | 188.789 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 188.794 | mensaje=(service expd03-originacion, taskSet ecs-svc/2167165345548783105) has begun draining connections on 1 tasks. |
| target_draining | 189.389 | instancia=a70980ed7d7945488c70f1ba568127b8;ip=10.0.0.26;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 198.97 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 219.68 | instancia=a70980ed7d7945488c70f1ba568127b8;ip=10.0.0.26 |
| falla_base | 360.738 | metodo=reboot_force_failover |
| base_failover_inicio | 374.704 | mensaje=Multi-AZ instance failover started. |
| base_reiniciada | 391.601 | mensaje=DB instance restarted |
| rds_evento | 414.394 | mensaje=The user requested a failover of the DB instance. |
| base_failover_fin | 414.394 | mensaje=Multi-AZ instance failover completed |
| k6_fin | 600.889 | codigo_salida=0 |
