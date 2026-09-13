## Ejecución `C2-crash`

| Métrica | Valor |
|---|---|
| Solicitudes | 30000 |
| Fallidas | 867 |
| Disponibilidad de la ejecución | 97.110 % |
| Minutos/mes equivalentes | 1248.5 |
| Ventana de error por falla de cómputo (s) | 9.6 |
| Detección por el monitor (s) | 8.5 |
| Reintegración (s) | 52.7 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 15.2 |
| Recuperación extremo a extremo tras failover (s) | 24.6 |
| Duración del failover de la base (s) | 16.2 |
| Retardo de reconexión del pool (s) | -1.6 |
| Errores esporádicos fuera de las ventanas de falla | 2 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 234.0 |
| p95 fase linea_base, solo exitosas (ms) | 234.0 |
| p95 fase una_instancia, todas (ms) | 94.0 |
| p95 fase una_instancia, solo exitosas (ms) | 94.0 |
| p95 fase failover_base, todas (ms) | 1628.2 |
| p95 fase failover_base, solo exitosas (ms) | 96.0 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | sí |
| (a2) sin falsos positivos del monitor | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **sí** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 120.316 | 129.949 | 9.633 | 228 | computo |
| 2 | 357.816 | 357.909 | 0.093 | 1 | esporadico |
| 3 | 370.018 | 385.168 | 15.15 | 637 | base |
| 4 | 555.276 | 555.358 | 0.082 | 1 | esporadico |

| Evento | t (s) | Detalle |
|---|---|---|
| target_estado_inicial | -2.595 | instancia=4465b18496bb4d01b8373a4ed5aee66d;ip=10.0.1.79;estado=healthy;razon= |
| target_estado_inicial | -2.594 | instancia=b914c63506b343a497a95f12df293289;ip=10.0.0.191;estado=healthy;razon= |
| k6_inicio | -0.057 | config=M1-P1;rps=50;duracion=600;tipo_falla=crash |
| falla_computo | 120.2 | tipo=crash;instancia=4465b18496bb4d01b8373a4ed5aee66d;ip=10.0.1.79 |
| target_unhealthy | 128.725 | instancia=4465b18496bb4d01b8373a4ed5aee66d;ip=10.0.1.79;estado=unhealthy;razon=Target.FailedHealthChecks |
| ecs_target_registrado | 131.1 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| target_draining | 131.655 | instancia=4465b18496bb4d01b8373a4ed5aee66d;ip=10.0.1.79;estado=draining;razon=Target.DeregistrationInProgress |
| ecs_evento | 140.521 | mensaje=(service expd03-originacion, taskSet ecs-svc/3498524227671422212) has begun draining connections on 1 tasks. |
| tarea_nueva | 141.32 | instancia=1314f5cfa7f34b3eb519723a56e536b4 |
| target_retirado | 162.205 | instancia=4465b18496bb4d01b8373a4ed5aee66d;ip=10.0.1.79 |
| ecs_target_registrado | 170.545 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 170.699 | instancia=1314f5cfa7f34b3eb519723a56e536b4;ip=10.0.1.231;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 172.927 | instancia=1314f5cfa7f34b3eb519723a56e536b4;ip=10.0.1.231;estado=healthy;razon= |
| servicio_estable | 179.418 | mensaje=(service expd03-originacion) has reached a steady state. |
| falla_base | 360.601 | metodo=reboot_force_failover |
| base_failover_inicio | 370.608 | mensaje=Multi-AZ instance failover started. |
| base_reiniciada | 386.801 | mensaje=DB instance restarted |
| rds_evento | 415.52 | mensaje=The user requested a failover of the DB instance. |
| base_failover_fin | 415.52 | mensaje=Multi-AZ instance failover completed |
| k6_fin | 600.762 | codigo_salida=0 |
