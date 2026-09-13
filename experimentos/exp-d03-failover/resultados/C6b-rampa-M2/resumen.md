## Ejecución `C6b-rampa-M2`

| Métrica | Valor |
|---|---|
| Solicitudes | 84560 |
| Fallidas | 81093 |
| Disponibilidad de la ejecución | 4.100 % |
| Minutos/mes equivalentes | 41428.8 |
| Ventana de error por falla de cómputo (s) | 0 |
| Detección por el monitor (s) | n/d |
| Reintegración (s) | n/d |
| Falsos positivos del monitor | 8 |
| Ventana de error por failover de base (s) | 0 |
| Recuperación extremo a extremo tras failover (s) | n/d |
| Duración del failover de la base (s) | n/d |
| Retardo de reconexión del pool (s) | n/d |
| Errores esporádicos fuera de las ventanas de falla | 81093 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 66 |
| p95 fase linea_base, todas (ms) | 5001.0 |
| p95 fase linea_base, solo exitosas (ms) | 515.1 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | n/d |
| (a2) sin falsos positivos del monitor | no |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | n/d |
| **Configuración cumple** | **no** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 62.929 | 604.939 | 542.01 | 81093 | esporadico |

| Minuto | Solicitudes | req/s | Fallidas | p95 exitosas (ms) | Transiciones a unhealthy |
|---|---|---|---|---|---|
| 0 | 3001 | 50.0 | 0 | 292.0 | 0 |
| 1 | 6200 | 103.3 | 5984 | 2582.8 | 2 |
| 2 | 6780 | 113.0 | 6755 | 714.4 | 1 |
| 3 | 10472 | 174.5 | 10458 | 1420.7 | 0 |
| 4 | 10827 | 180.4 | 10810 | 3100.6 | 2 |
| 5 | 10793 | 179.9 | 10793 | n/d | 0 |
| 6 | 10834 | 180.6 | 10804 | 2136.0 | 0 |
| 7 | 10795 | 179.9 | 10795 | n/d | 2 |
| 8 | 10796 | 179.9 | 10796 | n/d | 0 |
| 9 | 4062 | 67.7 | 3898 | 1668.7 | 1 |

| Evento | t (s) | Detalle |
|---|---|---|
| target_estado_inicial | -2.628 | instancia=945d7c1506b8428d8c2db9e209431cba;ip=10.0.1.65;estado=healthy;razon= |
| target_estado_inicial | -2.628 | instancia=35c0fc2c94d34e469c1df4fb085e85dc;ip=10.0.0.126;estado=healthy;razon= |
| k6_inicio | -0.095 | config=C6b-rampa-M2;rps=50;duracion=600;tipo_falla=hang |
| target_unhealthy | 90.767 | instancia=35c0fc2c94d34e469c1df4fb085e85dc;ip=10.0.0.126;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 93.692 | instancia=0a9f038bdee84623859b3fd16ef050bb |
| target_unhealthy | 94.802 | instancia=945d7c1506b8428d8c2db9e209431cba;ip=10.0.1.65;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 103.035 | instancia=46443b4e15484a5aa27fc491b7dfe7f4 |
| ecs_target_registrado | 122.755 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 123.995 | instancia=0a9f038bdee84623859b3fd16ef050bb;ip=10.0.0.152;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 125.12 | instancia=0a9f038bdee84623859b3fd16ef050bb;ip=10.0.0.152;estado=healthy;razon= |
| ecs_evento | 141.317 | mensaje=(service expd03-originacion) (task 35c0fc2c94d34e469c1df4fb085e85dc) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 141.349 | instancia=35c0fc2c94d34e469c1df4fb085e85dc |
| target_unhealthy | 149.783 | instancia=0a9f038bdee84623859b3fd16ef050bb;ip=10.0.0.152;estado=unhealthy;razon=Target.Timeout |
| ecs_target_registrado | 151.325 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 151.35 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 152.736 | instancia=35c0fc2c94d34e469c1df4fb085e85dc;ip=10.0.0.126;estado=draining;razon=Target.DeregistrationInProgress |
| ecs_evento | 161.249 | mensaje=(service expd03-originacion) was unable to place a task. Reason: CannotPullContainerError: ref pull has been retried 1 t |
| target_retirado | 182.51 | instancia=35c0fc2c94d34e469c1df4fb085e85dc;ip=10.0.0.126 |
| tarea_nueva | 201.122 | instancia=6330d966dbde4bee9d2335832f759553 |
| ecs_target_registrado | 228.522 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 229.057 | instancia=6330d966dbde4bee9d2335832f759553;ip=10.0.1.179;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 231.286 | instancia=6330d966dbde4bee9d2335832f759553;ip=10.0.1.179;estado=healthy;razon= |
| tarea_nueva | 239.307 | instancia=1eece718e99048ff9095d55dba77efbd |
| ecs_evento | 239.329 | mensaje=(service expd03-originacion) (task 945d7c1506b8428d8c2db9e209431cba) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 239.361 | instancia=945d7c1506b8428d8c2db9e209431cba |
| ecs_target_registrado | 247.727 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 247.732 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 249.654 | instancia=945d7c1506b8428d8c2db9e209431cba;ip=10.0.1.65;estado=draining;razon=Target.DeregistrationInProgress |
| target_unhealthy | 256.515 | instancia=6330d966dbde4bee9d2335832f759553;ip=10.0.1.179;estado=unhealthy;razon=Target.Timeout |
| ecs_target_registrado | 258.109 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 258.748 | instancia=1eece718e99048ff9095d55dba77efbd;ip=10.0.0.220;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 261.694 | instancia=1eece718e99048ff9095d55dba77efbd;ip=10.0.0.220;estado=healthy;razon= |
| ecs_evento | 266.683 | mensaje=(service expd03-originacion) (task 0a9f038bdee84623859b3fd16ef050bb) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 266.717 | instancia=0a9f038bdee84623859b3fd16ef050bb |
| ecs_target_registrado | 276.885 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 276.89 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 277.688 | instancia=0a9f038bdee84623859b3fd16ef050bb;ip=10.0.0.152;estado=draining;razon=Target.DeregistrationInProgress |
| target_retirado | 278.813 | instancia=945d7c1506b8428d8c2db9e209431cba;ip=10.0.1.65 |
| target_unhealthy | 287.109 | instancia=1eece718e99048ff9095d55dba77efbd;ip=10.0.0.220;estado=unhealthy;razon=Target.Timeout |
| target_retirado | 307.15 | instancia=0a9f038bdee84623859b3fd16ef050bb;ip=10.0.0.152 |
| tarea_nueva | 372.581 | instancia=aa6332dbaeb644c293b3548a5205f2a1 |
| tarea_nueva | 391.635 | instancia=900fe73b39a649d6b81656599d191c20 |
| ecs_target_registrado | 400.63 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 401.44 | instancia=aa6332dbaeb644c293b3548a5205f2a1;ip=10.0.0.46;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 402.559 | instancia=aa6332dbaeb644c293b3548a5205f2a1;ip=10.0.0.46;estado=healthy;razon= |
| ecs_evento | 410.087 | mensaje=(service expd03-originacion) (task 1eece718e99048ff9095d55dba77efbd) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 410.121 | instancia=1eece718e99048ff9095d55dba77efbd |
| target_estado_inicial | 411.058 | instancia=900fe73b39a649d6b81656599d191c20;ip=10.0.1.206;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 412.162 | instancia=900fe73b39a649d6b81656599d191c20;ip=10.0.1.206;estado=healthy;razon= |
| ecs_target_registrado | 419.703 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 419.708 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 420.344 | instancia=1eece718e99048ff9095d55dba77efbd;ip=10.0.0.220;estado=draining;razon=Target.DeregistrationInProgress |
| target_unhealthy | 428.848 | instancia=aa6332dbaeb644c293b3548a5205f2a1;ip=10.0.0.46;estado=unhealthy;razon=Target.Timeout |
| ecs_evento | 429.147 | mensaje=(service expd03-originacion) (task 6330d966dbde4bee9d2335832f759553) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 429.183 | instancia=6330d966dbde4bee9d2335832f759553 |
| target_unhealthy | 437.397 | instancia=900fe73b39a649d6b81656599d191c20;ip=10.0.1.206;estado=unhealthy;razon=Target.Timeout |
| ecs_target_registrado | 438.843 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 438.848 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 439.229 | instancia=6330d966dbde4bee9d2335832f759553;ip=10.0.1.179;estado=draining;razon=Target.DeregistrationInProgress |
| target_retirado | 449.964 | instancia=1eece718e99048ff9095d55dba77efbd;ip=10.0.0.220 |
| target_retirado | 470.698 | instancia=6330d966dbde4bee9d2335832f759553;ip=10.0.1.179 |
| tarea_nueva | 534.071 | instancia=a03364e6fa344781bded28814a2d3bb2 |
| ecs_target_registrado | 562.382 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| tarea_nueva | 563.042 | instancia=e2fd9c926fa54c14b892c67628895020 |
| target_estado_inicial | 563.691 | instancia=a03364e6fa344781bded28814a2d3bb2;ip=10.0.0.37;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 564.812 | instancia=a03364e6fa344781bded28814a2d3bb2;ip=10.0.0.37;estado=healthy;razon= |
| ecs_evento | 571.665 | mensaje=(service expd03-originacion) (task aa6332dbaeb644c293b3548a5205f2a1) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 571.696 | instancia=aa6332dbaeb644c293b3548a5205f2a1 |
| ecs_target_registrado | 582.474 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| ecs_evento | 582.533 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 583.775 | instancia=aa6332dbaeb644c293b3548a5205f2a1;ip=10.0.0.46;estado=draining;razon=Target.DeregistrationInProgress |
| target_estado_inicial | 583.776 | instancia=e2fd9c926fa54c14b892c67628895020;ip=10.0.1.66;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 584.903 | instancia=e2fd9c926fa54c14b892c67628895020;ip=10.0.1.66;estado=healthy;razon= |
| target_unhealthy | 590.111 | instancia=a03364e6fa344781bded28814a2d3bb2;ip=10.0.0.37;estado=unhealthy;razon=Target.Timeout |
| ecs_evento | 590.948 | mensaje=(service expd03-originacion) (task 900fe73b39a649d6b81656599d191c20) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 590.984 | instancia=900fe73b39a649d6b81656599d191c20 |
| ecs_target_registrado | 601.111 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 601.117 | mensaje=(service expd03-originacion, taskSet ecs-svc/3559820735141730004) has begun draining connections on 1 tasks. |
| target_draining | 601.65 | instancia=900fe73b39a649d6b81656599d191c20;ip=10.0.1.206;estado=draining;razon=Target.DeregistrationInProgress |
| k6_fin | 605.791 | codigo_salida=0 |
| target_retirado | 612.766 | instancia=aa6332dbaeb644c293b3548a5205f2a1;ip=10.0.0.46 |
| target_healthy | 614.992 | instancia=a03364e6fa344781bded28814a2d3bb2;ip=10.0.0.37;estado=healthy;razon= |
| servicio_estable | 620.906 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 631.632 | instancia=900fe73b39a649d6b81656599d191c20;ip=10.0.1.206 |
