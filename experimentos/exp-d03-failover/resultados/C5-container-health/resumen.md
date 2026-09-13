## Ejecución `C5-container-health`

| Métrica | Valor |
|---|---|
| Solicitudes | 30001 |
| Fallidas | 1064 |
| Disponibilidad de la ejecución | 96.453 % |
| Minutos/mes equivalentes | 1532.1 |
| Ventana de error por falla de cómputo (s) | 19.6 |
| Detección por el monitor (s) | 13.9 |
| Reintegración (s) | 108.6 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 18.1 |
| Recuperación extremo a extremo tras failover (s) | 27.8 |
| Duración del failover de la base (s) | 17.5 |
| Retardo de reconexión del pool (s) | -0.4 |
| Errores esporádicos fuera de las ventanas de falla | 2 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 233.0 |
| p95 fase linea_base, solo exitosas (ms) | 233.0 |
| p95 fase una_instancia, todas (ms) | 105.0 |
| p95 fase una_instancia, solo exitosas (ms) | 95.0 |
| p95 fase failover_base, todas (ms) | 3083.0 |
| p95 fase failover_base, solo exitosas (ms) | 98.0 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | sí |
| (a2) sin falsos positivos del monitor | sí |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **sí** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 120.717 | 140.358 | 19.641 | 360 | computo |
| 2 | 143.017 | 143.105 | 0.088 | 1 | esporadico |
| 3 | 370.338 | 388.486 | 18.148 | 702 | base |
| 4 | 499.257 | 499.345 | 0.088 | 1 | esporadico |

| Minuto | Solicitudes | req/s | Fallidas | p95 exitosas (ms) | Transiciones a unhealthy |
|---|---|---|---|---|---|
| 0 | 3001 | 50.0 | 0 | 246.0 | 0 |
| 1 | 3000 | 50.0 | 0 | 95.0 | 0 |
| 2 | 3000 | 50.0 | 361 | 97.0 | 1 |
| 3 | 3000 | 50.0 | 0 | 95.0 | 0 |
| 4 | 3000 | 50.0 | 0 | 95.0 | 0 |
| 5 | 3000 | 50.0 | 0 | 95.0 | 0 |
| 6 | 3000 | 50.0 | 702 | 502.8 | 0 |
| 7 | 3000 | 50.0 | 0 | 95.0 | 0 |
| 8 | 3000 | 50.0 | 1 | 94.0 | 0 |
| 9 | 3000 | 50.0 | 0 | 96.0 | 0 |

| Evento | t (s) | Detalle |
|---|---|---|
| ecs_evento | -10.698 | mensaje=(service expd03-originacion) (deployment ecs-svc/3660102271277155802) deployment completed. |
| servicio_estable | -10.697 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_estado_inicial | -2.056 | instancia=9615795d7c4f44deafe3d7f6768e9ff4;ip=10.0.0.160;estado=healthy;razon= |
| target_estado_inicial | -2.056 | instancia=158513352b4f4189867e32a612b3e6ea;ip=10.0.1.108;estado=healthy;razon= |
| k6_inicio | -0.054 | config=C5-container-health;rps=50;duracion=600;tipo_falla=hang |
| falla_computo | 120.789 | tipo=hang;instancia=158513352b4f4189867e32a612b3e6ea;ip=10.0.1.108 |
| target_unhealthy | 134.688 | instancia=158513352b4f4189867e32a612b3e6ea;ip=10.0.1.108;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 167.053 | instancia=a62723d1a0274fa58ef3421bec15fb25 |
| ecs_target_registrado | 226.834 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 228.322 | instancia=a62723d1a0274fa58ef3421bec15fb25;ip=10.0.0.61;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 229.438 | instancia=a62723d1a0274fa58ef3421bec15fb25;ip=10.0.0.61;estado=healthy;razon= |
| ecs_evento | 236.425 | mensaje=(service expd03-originacion) (task 158513352b4f4189867e32a612b3e6ea) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 236.458 | instancia=158513352b4f4189867e32a612b3e6ea |
| ecs_target_registrado | 246.242 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 246.262 | mensaje=(service expd03-originacion, taskSet ecs-svc/3660102271277155802) has begun draining connections on 1 tasks. |
| target_draining | 247.216 | instancia=158513352b4f4189867e32a612b3e6ea;ip=10.0.1.108;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 254.927 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 277.243 | instancia=158513352b4f4189867e32a612b3e6ea;ip=10.0.1.108 |
| falla_base | 360.681 | metodo=reboot_force_failover |
| base_failover_inicio | 371.358 | mensaje=Multi-AZ instance failover started. |
| base_reiniciada | 388.852 | mensaje=DB instance restarted |
| base_failover_fin | 401.137 | mensaje=Multi-AZ instance failover completed |
| rds_evento | 401.137 | mensaje=The user requested a failover of the DB instance. |
| k6_fin | 600.832 | codigo_salida=0 |
