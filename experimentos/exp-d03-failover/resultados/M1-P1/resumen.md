## Ejecución `M1-P1`

| Métrica | Valor |
|---|---|
| Solicitudes | 30001 |
| Fallidas | 1117 |
| Disponibilidad de la ejecución | 96.277 % |
| Minutos/mes equivalentes | 1608.4 |
| Ventana de error por falla de cómputo (s) | 19.2 |
| Detección por el monitor (s) | 13.3 |
| Reintegración (s) | 64.3 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 18.1 |
| Recuperación extremo a extremo tras failover (s) | 24.5 |
| Duración del failover de la base (s) | 16.6 |
| Retardo de reconexión del pool (s) | -1.4 |
| Errores esporádicos fuera de las ventanas de falla | 0 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 234.0 |
| p95 fase linea_base, solo exitosas (ms) | 233.0 |
| p95 fase una_instancia, todas (ms) | 100.0 |
| p95 fase una_instancia, solo exitosas (ms) | 95.0 |
| p95 fase failover_base, todas (ms) | 3082.5 |
| p95 fase failover_base, solo exitosas (ms) | 95.0 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | sí |
| (a2) sin falsos positivos del monitor | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **sí** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 120.117 | 139.298 | 19.181 | 350 | computo |
| 2 | 366.918 | 385.06 | 18.142 | 767 | base |

| Minuto | Solicitudes | req/s | Fallidas | p95 exitosas (ms) | Transiciones a unhealthy |
|---|---|---|---|---|---|
| 0 | 3001 | 50.0 | 0 | 247.0 | 0 |
| 1 | 3000 | 50.0 | 0 | 95.0 | 0 |
| 2 | 3000 | 50.0 | 350 | 95.0 | 1 |
| 3 | 3000 | 50.0 | 0 | 94.0 | 0 |
| 4 | 3000 | 50.0 | 0 | 95.0 | 0 |
| 5 | 3000 | 50.0 | 0 | 94.0 | 0 |
| 6 | 3000 | 50.0 | 767 | 110.0 | 0 |
| 7 | 3000 | 50.0 | 0 | 94.0 | 0 |
| 8 | 3000 | 50.0 | 0 | 95.0 | 0 |
| 9 | 3000 | 50.0 | 0 | 95.0 | 0 |

| Evento | t (s) | Detalle |
|---|---|---|
| target_estado_inicial | -1.977 | instancia=fd1a68c931914973bf23224c9b93cef4;ip=10.0.0.153;estado=healthy;razon= |
| target_estado_inicial | -1.977 | instancia=6d0151e953db450286f89ad39f72a886;ip=10.0.0.78;estado=healthy;razon= |
| k6_inicio | -0.052 | config=M1-P1;rps=50;duracion=600;tipo_falla=colgar |
| falla_computo | 120.186 | tipo=colgar;instancia=6d0151e953db450286f89ad39f72a886;ip=10.0.0.78 |
| target_unhealthy | 133.509 | instancia=6d0151e953db450286f89ad39f72a886;ip=10.0.0.78;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 153.663 | instancia=fcf84fc8d1e74b61ac2d924e16e33654 |
| ecs_target_registrado | 181.737 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 182.245 | instancia=fcf84fc8d1e74b61ac2d924e16e33654;ip=10.0.1.223;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 184.456 | instancia=fcf84fc8d1e74b61ac2d924e16e33654;ip=10.0.1.223;estado=healthy;razon= |
| ecs_evento | 190.787 | mensaje=(service expd03-originacion) (task 6d0151e953db450286f89ad39f72a886) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 190.82 | instancia=6d0151e953db450286f89ad39f72a886 |
| ecs_target_registrado | 200.524 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 200.537 | mensaje=(service expd03-originacion, taskSet ecs-svc/4032575061370499891) has begun draining connections on 1 tasks. |
| target_draining | 200.857 | instancia=6d0151e953db450286f89ad39f72a886;ip=10.0.0.78;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 210.802 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 248.584 | instancia=6d0151e953db450286f89ad39f72a886;ip=10.0.0.78 |
| falla_base | 360.591 | metodo=reboot_force_failover |
| base_failover_inicio | 369.806 | mensaje=Multi-AZ instance failover started. |
| base_reiniciada | 386.419 | mensaje=DB instance restarted |
| base_failover_fin | 414.695 | mensaje=Multi-AZ instance failover completed |
| rds_evento | 414.695 | mensaje=The user requested a failover of the DB instance. |
| k6_fin | 600.747 | codigo_salida=0 |
