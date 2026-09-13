## Ejecución `M2-P2`

| Métrica | Valor |
|---|---|
| Solicitudes | 30001 |
| Fallidas | 1529 |
| Disponibilidad de la ejecución | 94.904 % |
| Minutos/mes equivalentes | 2201.7 |
| Ventana de error por falla de cómputo (s) | 32.2 |
| Detección por el monitor (s) | 26.1 |
| Reintegración (s) | 82.2 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 17.2 |
| Recuperación extremo a extremo tras failover (s) | 21.4 |
| Duración del failover de la base (s) | 17.5 |
| Retardo de reconexión del pool (s) | -3.6 |
| Errores esporádicos fuera de las ventanas de falla | 1 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 237.0 |
| p95 fase linea_base, solo exitosas (ms) | 237.0 |
| p95 fase una_instancia, todas (ms) | 5000.0 |
| p95 fase una_instancia, solo exitosas (ms) | 94.0 |
| p95 fase failover_base, todas (ms) | 3081.0 |
| p95 fase failover_base, solo exitosas (ms) | 94.0 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | no |
| (a2) sin falsos positivos del monitor | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **no** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 120.139 | 152.379 | 32.24 | 674 | computo |
| 2 | 364.838 | 381.991 | 17.153 | 854 | base |
| 3 | 573.058 | 573.145 | 0.087 | 1 | esporadico |

| Evento | t (s) | Detalle |
|---|---|---|
| ecs_evento | -8.428 | mensaje=(service expd03-originacion) (deployment ecs-svc/0254910055998611338) deployment completed. |
| servicio_estable | -8.427 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_estado_inicial | -2.608 | instancia=c06f12805a2846aa9964ec46477ae169;ip=10.0.1.115;estado=healthy;razon= |
| target_estado_inicial | -2.607 | instancia=66066f5cae104f0fae66c804d885fae7;ip=10.0.0.168;estado=healthy;razon= |
| k6_inicio | -0.052 | config=M2-P2;rps=50;duracion=600;tipo_falla=colgar |
| falla_computo | 120.197 | tipo=colgar;instancia=66066f5cae104f0fae66c804d885fae7;ip=10.0.0.168 |
| target_unhealthy | 146.343 | instancia=66066f5cae104f0fae66c804d885fae7;ip=10.0.0.168;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 181.843 | instancia=62a07d59e4cf4124bab726b4b7a1ca3a |
| ecs_target_registrado | 200.43 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 201.307 | instancia=62a07d59e4cf4124bab726b4b7a1ca3a;ip=10.0.1.88;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 202.418 | instancia=62a07d59e4cf4124bab726b4b7a1ca3a;ip=10.0.1.88;estado=healthy;razon= |
| ecs_evento | 209.471 | mensaje=(service expd03-originacion) (task 66066f5cae104f0fae66c804d885fae7) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 209.508 | instancia=66066f5cae104f0fae66c804d885fae7 |
| ecs_target_registrado | 219.107 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 219.112 | mensaje=(service expd03-originacion, taskSet ecs-svc/0254910055998611338) has begun draining connections on 1 tasks. |
| target_draining | 220.237 | instancia=66066f5cae104f0fae66c804d885fae7;ip=10.0.0.168;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 229.082 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 250.923 | instancia=66066f5cae104f0fae66c804d885fae7;ip=10.0.0.168 |
| falla_base | 360.609 | metodo=reboot_force_failover |
| base_failover_inicio | 368.088 | mensaje=Multi-AZ instance failover started. |
| base_reiniciada | 385.576 | mensaje=DB instance restarted |
| base_failover_fin | 418.012 | mensaje=Multi-AZ instance failover completed |
| rds_evento | 418.012 | mensaje=The user requested a failover of the DB instance. |
| k6_fin | 600.76 | codigo_salida=0 |
