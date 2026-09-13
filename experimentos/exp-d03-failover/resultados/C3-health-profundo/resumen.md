## Ejecución `C3-health-profundo`

| Métrica | Valor |
|---|---|
| Solicitudes | 30001 |
| Fallidas | 1254 |
| Disponibilidad de la ejecución | 95.820 % |
| Minutos/mes equivalentes | 1805.7 |
| Ventana de error por falla de cómputo (s) | 20.2 |
| Detección por el monitor (s) | 13.5 |
| Reintegración (s) | 77.6 |
| Falsos positivos del monitor | 2 |
| Ventana de error por failover de base (s) | 19.5 |
| Recuperación extremo a extremo tras failover (s) | 24.0 |
| Duración del failover de la base (s) | 17.8 |
| Retardo de reconexión del pool (s) | 1.6 |
| Errores esporádicos fuera de las ventanas de falla | 73 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 348.0 |
| p95 fase linea_base, solo exitosas (ms) | 330.0 |
| p95 fase una_instancia, todas (ms) | 183.0 |
| p95 fase una_instancia, solo exitosas (ms) | 153.0 |
| p95 fase failover_base, todas (ms) | 3103.0 |
| p95 fase failover_base, solo exitosas (ms) | 149.0 |

| Criterio | Cumple |
|---|---|
| (a1) ventana de cómputo ≤ 30 s | sí |
| (a2) sin falsos positivos del monitor | no |
| (a3) sin aceptadas perdidas | sí |
| (b) recuperación tras failover ≤ 120 s | sí |
| **Configuración cumple** | **no** |

| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |
|---|---|---|---|---|---|
| 1 | 0.057 | 10.978 | 10.921 | 60 | esporadico |
| 2 | 10.117 | 20.857 | 10.74 | 9 | esporadico |
| 3 | 21.657 | 26.657 | 5.0 | 1 | esporadico |
| 4 | 26.296 | 31.297 | 5.001 | 1 | esporadico |
| 5 | 120.157 | 140.397 | 20.24 | 376 | computo |
| 6 | 292.176 | 292.284 | 0.108 | 1 | esporadico |
| 7 | 365.117 | 384.654 | 19.537 | 805 | base |
| 8 | 556.037 | 556.168 | 0.131 | 1 | esporadico |

| Minuto | Solicitudes | req/s | Fallidas | p95 exitosas (ms) | Transiciones a unhealthy |
|---|---|---|---|---|---|
| 0 | 3001 | 50.0 | 71 | 364.5 | 0 |
| 1 | 3000 | 50.0 | 0 | 154.0 | 0 |
| 2 | 3000 | 50.0 | 376 | 166.8 | 1 |
| 3 | 3000 | 50.0 | 0 | 154.0 | 0 |
| 4 | 3000 | 50.0 | 1 | 142.0 | 0 |
| 5 | 3000 | 50.0 | 0 | 146.0 | 0 |
| 6 | 3000 | 50.0 | 805 | 934.0 | 2 |
| 7 | 3000 | 50.0 | 0 | 143.0 | 0 |
| 8 | 3000 | 50.0 | 0 | 139.0 | 0 |
| 9 | 3000 | 50.0 | 1 | 139.0 | 0 |

| Evento | t (s) | Detalle |
|---|---|---|
| ecs_evento | -12.68 | mensaje=(service expd03-originacion) (deployment ecs-svc/9880664583963965843) deployment completed. |
| servicio_estable | -12.679 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_estado_inicial | -2.492 | instancia=7da02381ff504e19acbbf874c565e3f2;ip=10.0.0.137;estado=healthy;razon= |
| target_estado_inicial | -2.491 | instancia=56fe4a617a30489a8f9c54383b86f600;ip=10.0.1.190;estado=healthy;razon= |
| k6_inicio | -0.056 | config=C3-profundo;rps=50;duracion=600;tipo_falla=hang |
| falla_computo | 120.256 | tipo=hang;instancia=56fe4a617a30489a8f9c54383b86f600;ip=10.0.1.190 |
| target_unhealthy | 133.776 | instancia=56fe4a617a30489a8f9c54383b86f600;ip=10.0.1.190;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 177.399 | instancia=ae5d8e6ff40f4f17b32724b76ccd222b |
| ecs_target_registrado | 196.154 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 196.753 | instancia=ae5d8e6ff40f4f17b32724b76ccd222b;ip=10.0.0.80;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 197.882 | instancia=ae5d8e6ff40f4f17b32724b76ccd222b;ip=10.0.0.80;estado=healthy;razon= |
| ecs_evento | 205.623 | mensaje=(service expd03-originacion) (task 56fe4a617a30489a8f9c54383b86f600) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 205.653 | instancia=56fe4a617a30489a8f9c54383b86f600 |
| ecs_target_registrado | 214.754 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 214.76 | mensaje=(service expd03-originacion, taskSet ecs-svc/9880664583963965843) has begun draining connections on 1 tasks. |
| target_draining | 216.108 | instancia=56fe4a617a30489a8f9c54383b86f600;ip=10.0.1.190;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 223.933 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 246.954 | instancia=56fe4a617a30489a8f9c54383b86f600;ip=10.0.1.190 |
| falla_base | 360.679 | metodo=reboot_force_failover |
| base_failover_inicio | 365.293 | mensaje=Multi-AZ instance failover started. |
| target_unhealthy | 375.422 | instancia=ae5d8e6ff40f4f17b32724b76ccd222b;ip=10.0.0.80;estado=unhealthy;razon=Target.ResponseCodeMismatch |
| target_unhealthy | 378.58 | instancia=7da02381ff504e19acbbf874c565e3f2;ip=10.0.0.137;estado=unhealthy;razon=Target.ResponseCodeMismatch |
| base_reiniciada | 383.043 | mensaje=DB instance restarted |
| target_healthy | 394.172 | instancia=7da02381ff504e19acbbf874c565e3f2;ip=10.0.0.137;estado=healthy;razon= |
| target_healthy | 394.172 | instancia=ae5d8e6ff40f4f17b32724b76ccd222b;ip=10.0.0.80;estado=healthy;razon= |
| rds_evento | 410.195 | mensaje=The user requested a failover of the DB instance. |
| base_failover_fin | 410.195 | mensaje=Multi-AZ instance failover completed |
| k6_fin | 600.827 | codigo_salida=0 |
