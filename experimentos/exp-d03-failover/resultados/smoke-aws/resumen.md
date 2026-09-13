## Ejecución `smoke-aws`

| Métrica | Valor |
|---|---|
| Solicitudes | 6000 |
| Fallidas | 1161 |
| Disponibilidad de la ejecución | 80.650 % |
| Minutos/mes equivalentes | 8359.2 |
| Ventana de error por falla de cómputo (s) | 18.7 |
| Detección por el monitor (s) | 12.2 |
| Reintegración (s) | 81.2 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 19.5 |
| Recuperación extremo a extremo tras failover (s) | 25.8 |
| Duración del failover de la base (s) | 16.3 |
| Retardo de reconexión del pool (s) | -0.8 |
| Errores esporádicos fuera de las ventanas de falla | 0 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 254.8 |
| p95 fase linea_base, solo exitosas (ms) | 254.0 |
| p95 fase una_instancia, todas (ms) | 5001.0 |
| p95 fase una_instancia, solo exitosas (ms) | 94.0 |
| p95 fase failover_base, todas (ms) | 3091.0 |
| p95 fase failover_base, solo exitosas (ms) | 1713.3 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | sí |
| (a2) sin falsos positivos del monitor | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **sí** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 30.036 | 48.777 | 18.741 | 341 | computo |
| 2 | 81.717 | 101.184 | 19.468 | 820 | base |

| Evento | t (s) | Detalle |
|---|---|---|
| target_estado_inicial | -2.689 | instancia=ce6a0e44aa01421c8fc80356f1ea1575;ip=10.0.1.65;estado=healthy;razon= |
| target_estado_inicial | -2.689 | instancia=fd1a68c931914973bf23224c9b93cef4;ip=10.0.0.153;estado=healthy;razon= |
| k6_inicio | -0.08 | config=M1-P1;rps=50;duracion=120;tipo_falla=colgar |
| falla_computo | 30.108 | tipo=colgar;instancia=ce6a0e44aa01421c8fc80356f1ea1575;ip=10.0.1.65 |
| target_unhealthy | 42.346 | instancia=ce6a0e44aa01421c8fc80356f1ea1575;ip=10.0.1.65;estado=unhealthy;razon=Target.Timeout |
| falla_base | 75.341 | metodo=reboot_force_failover |
| tarea_nueva | 81.056 | instancia=6d0151e953db450286f89ad39f72a886 |
| base_failover_inicio | 85.746 | mensaje=Multi-AZ instance failover started. |
| base_reiniciada | 102.003 | mensaje=DB instance restarted |
| ecs_target_registrado | 108.981 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 110.243 | instancia=6d0151e953db450286f89ad39f72a886;ip=10.0.0.78;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 111.351 | instancia=6d0151e953db450286f89ad39f72a886;ip=10.0.0.78;estado=healthy;razon= |
| ecs_evento | 117.841 | mensaje=(service expd03-originacion) (task ce6a0e44aa01421c8fc80356f1ea1575) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 117.873 | instancia=ce6a0e44aa01421c8fc80356f1ea1575 |
| k6_fin | 120.868 | codigo_salida=0 |
| ecs_target_registrado | 127.186 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 127.192 | mensaje=(service expd03-originacion, taskSet ecs-svc/4032575061370499891) has begun draining connections on 1 tasks. |
| target_draining | 127.333 | instancia=ce6a0e44aa01421c8fc80356f1ea1575;ip=10.0.1.65;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 136.751 | mensaje=(service expd03-originacion) has reached a steady state. |
| rds_evento | 141.292 | mensaje=The user requested a failover of the DB instance. |
| base_failover_fin | 141.292 | mensaje=Multi-AZ instance failover completed |
| target_retirado | 158.011 | instancia=ce6a0e44aa01421c8fc80356f1ea1575;ip=10.0.1.65 |
