## Ejecución `C6a-rampa-M1`

| Métrica | Valor |
|---|---|
| Solicitudes | 84589 |
| Fallidas | 81188 |
| Disponibilidad de la ejecución | 4.021 % |
| Minutos/mes equivalentes | 41463.1 |
| Ventana de error por falla de cómputo (s) | 0 |
| Detección por el monitor (s) | n/d |
| Reintegración (s) | n/d |
| Falsos positivos del monitor | 9 |
| Ventana de error por failover de base (s) | 0 |
| Recuperación extremo a extremo tras failover (s) | n/d |
| Duración del failover de la base (s) | n/d |
| Retardo de reconexión del pool (s) | n/d |
| Errores esporádicos fuera de las ventanas de falla | 81188 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 17 |
| p95 fase linea_base, todas (ms) | 5001.0 |
| p95 fase linea_base, solo exitosas (ms) | 459.0 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | n/d |
| (a2) sin falsos positivos del monitor | no |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | n/d |
| **Configuración cumple** | **no** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 62.301 | 604.96 | 542.659 | 81188 | esporadico |

| Minuto | Solicitudes | req/s | Fallidas | p95 exitosas (ms) | Transiciones a unhealthy |
|---|---|---|---|---|---|
| 0 | 3000 | 50.0 | 0 | 293.0 | 0 |
| 1 | 6201 | 103.3 | 5993 | 2608.0 | 2 |
| 2 | 6780 | 113.0 | 6691 | 1007.2 | 2 |
| 3 | 10440 | 174.0 | 10440 | n/d | 0 |
| 4 | 10810 | 180.2 | 10794 | 1987.5 | 0 |
| 5 | 10808 | 180.1 | 10793 | 1840.8 | 2 |
| 6 | 10794 | 179.9 | 10794 | n/d | 0 |
| 7 | 10834 | 180.6 | 10802 | 2398.2 | 1 |
| 8 | 10797 | 179.9 | 10797 | n/d | 1 |
| 9 | 4125 | 68.8 | 4084 | 1399.0 | 0 |

| Evento | t (s) | Detalle |
|---|---|---|
| ecs_evento | -11.892 | mensaje=(service expd03-originacion) (deployment ecs-svc/3559820735141730004) deployment completed. |
| servicio_estable | -11.89 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_estado_inicial | -2.595 | instancia=5c138df09477451f8973151f7575e345;ip=10.0.1.87;estado=healthy;razon= |
| target_estado_inicial | -2.594 | instancia=4f5e949bbb7a4a56b3146c161b6c7b40;ip=10.0.0.31;estado=healthy;razon= |
| k6_inicio | -0.078 | config=C6a-rampa-M1;rps=50;duracion=600;tipo_falla=hang |
| target_unhealthy | 81.133 | instancia=5c138df09477451f8973151f7575e345;ip=10.0.1.87;estado=unhealthy;razon=Target.Timeout |
| target_unhealthy | 81.133 | instancia=4f5e949bbb7a4a56b3146c161b6c7b40;ip=10.0.0.31;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 126.538 | instancia=2b8b3ded392844b7b2a6837ef9b7fd1a |
| ecs_target_registrado | 145.827 | mensaje=(service expd03-originacion) registered 2 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 147.014 | instancia=2b8b3ded392844b7b2a6837ef9b7fd1a;ip=10.0.0.161;estado=initial;razon=Elb.RegistrationInProgress |
| target_estado_inicial | 147.014 | instancia=2df5d0a693764a13b373510566b22ff5;ip=10.0.1.177;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 148.132 | instancia=2b8b3ded392844b7b2a6837ef9b7fd1a;ip=10.0.0.161;estado=healthy;razon= |
| target_healthy | 148.132 | instancia=2df5d0a693764a13b373510566b22ff5;ip=10.0.1.177;estado=healthy;razon= |
| ecs_evento | 155.274 | mensaje=(service expd03-originacion) (task 5c138df09477451f8973151f7575e345) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 155.339 | instancia=4f5e949bbb7a4a56b3146c161b6c7b40 |
| ecs_target_registrado | 164.715 | mensaje=(service expd03-originacion) deregistered 2 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 164.72 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 2 tasks. |
| target_unhealthy | 165.926 | instancia=2b8b3ded392844b7b2a6837ef9b7fd1a;ip=10.0.0.161;estado=unhealthy;razon=Target.Timeout |
| target_unhealthy | 165.927 | instancia=2df5d0a693764a13b373510566b22ff5;ip=10.0.1.177;estado=unhealthy;razon=Target.Timeout |
| target_draining | 165.927 | instancia=5c138df09477451f8973151f7575e345;ip=10.0.1.87;estado=draining;razon=Target.DeregistrationInProgress |
| target_draining | 165.927 | instancia=4f5e949bbb7a4a56b3146c161b6c7b40;ip=10.0.0.31;estado=draining;razon=Target.DeregistrationInProgress |
| target_retirado | 195.317 | instancia=5c138df09477451f8973151f7575e345;ip=10.0.1.87 |
| target_retirado | 195.318 | instancia=4f5e949bbb7a4a56b3146c161b6c7b40;ip=10.0.0.31 |
| tarea_nueva | 269.816 | instancia=f435d6b659be416a819cbaebafaeb307 |
| tarea_nueva | 280.101 | instancia=ce2fce7c528f4098b65c396d24843403 |
| ecs_target_registrado | 289.741 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 290.535 | instancia=f435d6b659be416a819cbaebafaeb307;ip=10.0.0.56;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 292.896 | instancia=f435d6b659be416a819cbaebafaeb307;ip=10.0.0.56;estado=healthy;razon= |
| ecs_evento | 298.283 | mensaje=(service expd03-originacion) (task 2b8b3ded392844b7b2a6837ef9b7fd1a) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 298.316 | instancia=2b8b3ded392844b7b2a6837ef9b7fd1a |
| target_estado_inicial | 298.676 | instancia=ce2fce7c528f4098b65c396d24843403;ip=10.0.1.6;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 300.904 | instancia=ce2fce7c528f4098b65c396d24843403;ip=10.0.1.6;estado=healthy;razon= |
| target_unhealthy | 305.012 | instancia=f435d6b659be416a819cbaebafaeb307;ip=10.0.0.56;estado=unhealthy;razon=Target.Timeout |
| ecs_target_registrado | 307.721 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 307.726 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 308.345 | instancia=2b8b3ded392844b7b2a6837ef9b7fd1a;ip=10.0.0.161;estado=draining;razon=Target.DeregistrationInProgress |
| ecs_evento | 316.535 | mensaje=(service expd03-originacion) (task 2df5d0a693764a13b373510566b22ff5) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 316.574 | instancia=2df5d0a693764a13b373510566b22ff5 |
| target_unhealthy | 318.668 | instancia=ce2fce7c528f4098b65c396d24843403;ip=10.0.1.6;estado=unhealthy;razon=Target.Timeout |
| ecs_target_registrado | 325.549 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 325.554 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 326.135 | instancia=2df5d0a693764a13b373510566b22ff5;ip=10.0.1.177;estado=draining;razon=Target.DeregistrationInProgress |
| target_retirado | 338.691 | instancia=2b8b3ded392844b7b2a6837ef9b7fd1a;ip=10.0.0.161 |
| target_retirado | 356.476 | instancia=2df5d0a693764a13b373510566b22ff5;ip=10.0.1.177 |
| tarea_nueva | 422.054 | instancia=3d78f555903e4ba6864a3078eee1e4ad |
| ecs_target_registrado | 440.863 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 441.942 | instancia=3d78f555903e4ba6864a3078eee1e4ad;ip=10.0.1.178;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 443.058 | instancia=3d78f555903e4ba6864a3078eee1e4ad;ip=10.0.1.178;estado=healthy;razon= |
| tarea_nueva | 450.737 | instancia=e54e5207ab974d9f894ca3f6104a5ab6 |
| ecs_evento | 450.764 | mensaje=(service expd03-originacion) (task ce2fce7c528f4098b65c396d24843403) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 450.798 | instancia=ce2fce7c528f4098b65c396d24843403 |
| target_unhealthy | 457.67 | instancia=3d78f555903e4ba6864a3078eee1e4ad;ip=10.0.1.178;estado=unhealthy;razon=Target.Timeout |
| ecs_target_registrado | 459.341 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 459.346 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 459.889 | instancia=ce2fce7c528f4098b65c396d24843403;ip=10.0.1.6;estado=draining;razon=Target.DeregistrationInProgress |
| ecs_target_registrado | 468.996 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 470.301 | instancia=e54e5207ab974d9f894ca3f6104a5ab6;ip=10.0.0.214;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 471.42 | instancia=e54e5207ab974d9f894ca3f6104a5ab6;ip=10.0.0.214;estado=healthy;razon= |
| ecs_evento | 477.819 | mensaje=(service expd03-originacion) (task f435d6b659be416a819cbaebafaeb307) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 477.851 | instancia=f435d6b659be416a819cbaebafaeb307 |
| target_unhealthy | 484.49 | instancia=e54e5207ab974d9f894ca3f6104a5ab6;ip=10.0.0.214;estado=unhealthy;razon=Target.Timeout |
| ecs_target_registrado | 487.814 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 487.82 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 489.68 | instancia=f435d6b659be416a819cbaebafaeb307;ip=10.0.0.56;estado=draining;razon=Target.DeregistrationInProgress |
| target_retirado | 489.68 | instancia=ce2fce7c528f4098b65c396d24843403;ip=10.0.1.6 |
| target_retirado | 518.383 | instancia=f435d6b659be416a819cbaebafaeb307;ip=10.0.0.56 |
| tarea_nueva | 555.694 | instancia=945d7c1506b8428d8c2db9e209431cba |
| ecs_target_registrado | 583.561 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 584.586 | instancia=945d7c1506b8428d8c2db9e209431cba;ip=10.0.1.65;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 585.694 | instancia=945d7c1506b8428d8c2db9e209431cba;ip=10.0.1.65;estado=healthy;razon= |
| ecs_evento | 592.746 | mensaje=(service expd03-originacion) (task 3d78f555903e4ba6864a3078eee1e4ad) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 592.781 | instancia=3d78f555903e4ba6864a3078eee1e4ad |
| ecs_target_registrado | 602.051 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 602.057 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 602.482 | instancia=3d78f555903e4ba6864a3078eee1e4ad;ip=10.0.1.178;estado=draining;razon=Target.DeregistrationInProgress |
| target_unhealthy | 603.589 | instancia=945d7c1506b8428d8c2db9e209431cba;ip=10.0.1.65;estado=unhealthy;razon=Target.Timeout |
| k6_fin | 605.901 | codigo_salida=0 |
| tarea_nueva | 612.449 | instancia=35c0fc2c94d34e469c1df4fb085e85dc |
| target_healthy | 612.878 | instancia=945d7c1506b8428d8c2db9e209431cba;ip=10.0.1.65;estado=healthy;razon= |
| target_retirado | 632.946 | instancia=3d78f555903e4ba6864a3078eee1e4ad;ip=10.0.1.178 |
| ecs_target_registrado | 640.092 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 640.35 | instancia=35c0fc2c94d34e469c1df4fb085e85dc;ip=10.0.0.126;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 642.575 | instancia=35c0fc2c94d34e469c1df4fb085e85dc;ip=10.0.0.126;estado=healthy;razon= |
| ecs_evento | 649.188 | mensaje=(service expd03-originacion) (task e54e5207ab974d9f894ca3f6104a5ab6) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 649.321 | instancia=e54e5207ab974d9f894ca3f6104a5ab6 |
| ecs_target_registrado | 658.897 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 658.902 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 659.282 | instancia=e54e5207ab974d9f894ca3f6104a5ab6;ip=10.0.0.214;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 668.38 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 689.446 | instancia=e54e5207ab974d9f894ca3f6104a5ab6;ip=10.0.0.214 |
