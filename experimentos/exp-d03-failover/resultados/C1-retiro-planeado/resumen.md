## Ejecución `C1-retiro-planeado`

| Métrica | Valor |
|---|---|
| Solicitudes | 30001 |
| Fallidas | 693 |
| Disponibilidad de la ejecución | 97.690 % |
| Minutos/mes equivalentes | 997.9 |
| Ventana de error por falla de cómputo (s) | 0 |
| Detección por el monitor (s) | n/d |
| Reintegración (s) | 34.5 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 15.3 |
| Recuperación extremo a extremo tras failover (s) | 20.5 |
| Duración del failover de la base (s) | 16.6 |
| Retardo de reconexión del pool (s) | -3.2 |
| Errores esporádicos fuera de las ventanas de falla | 2 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 233.0 |
| p95 fase linea_base, solo exitosas (ms) | 233.0 |
| p95 fase una_instancia, todas (ms) | 95.0 |
| p95 fase una_instancia, solo exitosas (ms) | 95.0 |
| p95 fase failover_base, todas (ms) | 1699.5 |
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
| 1 | 365.737 | 381.002 | 15.265 | 691 | base |
| 2 | 460.976 | 461.06 | 0.084 | 1 | esporadico |
| 3 | 511.958 | 512.042 | 0.084 | 1 | esporadico |

| Evento | t (s) | Detalle |
|---|---|---|
| ecs_evento | -9.953 | mensaje=(service expd03-originacion) (deployment ecs-svc/3498524227671422212) deployment completed. |
| servicio_estable | -9.952 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_estado_inicial | -2.58 | instancia=b16d7d8039ac48d3872ebf4ce4fcbbad;ip=10.0.1.73;estado=healthy;razon= |
| target_estado_inicial | -2.58 | instancia=b914c63506b343a497a95f12df293289;ip=10.0.0.191;estado=healthy;razon= |
| k6_inicio | -0.056 | config=M1-P1;rps=50;duracion=600;tipo_falla=stop-task |
| retiro_planeado | 120.135 | tipo=stop-task;instancia=b16d7d8039ac48d3872ebf4ce4fcbbad;ip=10.0.1.73 |
| ecs_target_registrado | 123.805 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 123.811 | mensaje=(service expd03-originacion, taskSet ecs-svc/3498524227671422212) has begun draining connections on 1 tasks. |
| target_draining | 124.213 | instancia=b16d7d8039ac48d3872ebf4ce4fcbbad;ip=10.0.1.73;estado=draining;razon=Target.DeregistrationInProgress |
| tarea_nueva | 124.709 | instancia=4465b18496bb4d01b8373a4ed5aee66d |
| ecs_target_registrado | 152.26 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 152.435 | instancia=4465b18496bb4d01b8373a4ed5aee66d;ip=10.0.1.79;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 154.676 | instancia=4465b18496bb4d01b8373a4ed5aee66d;ip=10.0.1.79;estado=healthy;razon= |
| target_retirado | 154.676 | instancia=b16d7d8039ac48d3872ebf4ce4fcbbad;ip=10.0.1.73 |
| servicio_estable | 170.532 | mensaje=(service expd03-originacion) has reached a steady state. |
| falla_base | 360.525 | metodo=reboot_force_failover |
| base_failover_inicio | 367.572 | mensaje=Multi-AZ instance failover started. |
| base_reiniciada | 384.192 | mensaje=DB instance restarted |
| base_failover_fin | 422.453 | mensaje=Multi-AZ instance failover completed |
| rds_evento | 422.453 | mensaje=The user requested a failover of the DB instance. |
| k6_fin | 600.678 | codigo_salida=0 |
