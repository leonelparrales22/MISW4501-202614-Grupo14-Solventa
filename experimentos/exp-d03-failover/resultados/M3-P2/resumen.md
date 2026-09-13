## Ejecución `M3-P2`

| Métrica | Valor |
|---|---|
| Solicitudes | 30000 |
| Fallidas | 3993 |
| Disponibilidad de la ejecución | 86.690 % |
| Minutos/mes equivalentes | 5749.9 |
| Ventana de error por falla de cómputo (s) | 95.0 |
| Detección por el monitor (s) | 89.2 |
| Reintegración (s) | 131.7 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 54.5 |
| Recuperación extremo a extremo tras failover (s) | 71.3 |
| Duración del failover de la base (s) | 15.5 |
| Retardo de reconexión del pool (s) | 45.5 |
| Errores esporádicos fuera de las ventanas de falla | 0 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 245 |
| p95 fase linea_base, todas (ms) | 236.0 |
| p95 fase linea_base, solo exitosas (ms) | 236.0 |
| p95 fase una_instancia, todas (ms) | 5001.0 |
| p95 fase una_instancia, solo exitosas (ms) | 558.0 |
| p95 fase failover_base, todas (ms) | 5000.0 |
| p95 fase failover_base, solo exitosas (ms) | 557.0 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | no |
| (a2) sin falsos positivos del monitor | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **no** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 120.117 | 215.096 | 94.979 | 2231 | computo |
| 2 | 367.976 | 386.795 | 18.818 | 764 | base |
| 3 | 394.536 | 425.377 | 30.841 | 987 | base |
| 4 | 427.496 | 432.305 | 4.808 | 11 | base |

| Evento | t (s) | Detalle |
|---|---|---|
| ecs_evento | -8.201 | mensaje=(service expd03-originacion) (deployment ecs-svc/6777908225549351703) deployment completed. |
| servicio_estable | -8.2 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_estado_inicial | -2.531 | instancia=1ae2230590ae4bbcb3197357c011ec5b;ip=10.0.0.169;estado=healthy;razon= |
| target_estado_inicial | -2.531 | instancia=bb6b4e2d29e54f5fbe16fc749e98aa33;ip=10.0.1.87;estado=healthy;razon= |
| k6_inicio | -0.056 | config=M3-P2;rps=50;duracion=600;tipo_falla=colgar |
| falla_computo | 120.202 | tipo=colgar;instancia=1ae2230590ae4bbcb3197357c011ec5b;ip=10.0.0.169 |
| target_unhealthy | 209.366 | instancia=1ae2230590ae4bbcb3197357c011ec5b;ip=10.0.0.169;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 221.424 | instancia=dc01335b108549d38b702bc6611d0777 |
| ecs_target_registrado | 249.579 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_healthy | 251.931 | instancia=dc01335b108549d38b702bc6611d0777;ip=10.0.0.126;estado=healthy;razon= |
| ecs_evento | 267.869 | mensaje=(service expd03-originacion) (task 1ae2230590ae4bbcb3197357c011ec5b) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 267.896 | instancia=1ae2230590ae4bbcb3197357c011ec5b |
| ecs_target_registrado | 278.101 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 278.128 | mensaje=(service expd03-originacion, taskSet ecs-svc/6777908225549351703) has begun draining connections on 1 tasks. |
| target_draining | 279.659 | instancia=1ae2230590ae4bbcb3197357c011ec5b;ip=10.0.0.169;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 287.789 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 308.704 | instancia=1ae2230590ae4bbcb3197357c011ec5b;ip=10.0.0.169 |
| falla_base | 360.963 | metodo=reboot_force_failover |
| base_failover_inicio | 371.236 | mensaje=Multi-AZ instance failover started. |
| base_failover_fin | 386.102 | mensaje=Multi-AZ instance failover completed |
| rds_evento | 386.102 | mensaje=The user requested a failover of the DB instance. |
| base_reiniciada | 386.77 | mensaje=DB instance restarted |
| k6_fin | 602.118 | codigo_salida=0 |
