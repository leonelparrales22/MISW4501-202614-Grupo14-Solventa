## Ejecución `M1-P2`

| Métrica | Valor |
|---|---|
| Solicitudes | 30001 |
| Fallidas | 1257 |
| Disponibilidad de la ejecución | 95.810 % |
| Minutos/mes equivalentes | 1810.0 |
| Ventana de error por falla de cómputo (s) | 18.1 |
| Detección por el monitor (s) | 11.7 |
| Reintegración (s) | 68.6 |
| Falsos positivos del monitor | 0 |
| Ventana de error por failover de base (s) | 18.7 |
| Recuperación extremo a extremo tras failover (s) | 26.2 |
| Duración del failover de la base (s) | 17.4 |
| Retardo de reconexión del pool (s) | -3.8 |
| Errores esporádicos fuera de las ventanas de falla | 0 |
| Aceptadas perdidas | 0 |
| Resultados ambiguos | 0 |
| p95 fase linea_base, todas (ms) | 236.6 |
| p95 fase linea_base, solo exitosas (ms) | 236.0 |
| p95 fase una_instancia, todas (ms) | 99.0 |
| p95 fase una_instancia, solo exitosas (ms) | 95.0 |
| p95 fase failover_base, todas (ms) | 3084.0 |
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
| 1 | 120.137 | 138.238 | 18.101 | 328 | computo |
| 2 | 362.818 | 362.908 | 0.09 | 1 | base |
| 3 | 368.577 | 387.214 | 18.637 | 928 | base |

| Evento | t (s) | Detalle |
|---|---|---|
| ecs_evento | -11.383 | mensaje=(service expd03-originacion) (deployment ecs-svc/6042458078041624456) deployment completed. |
| servicio_estable | -11.382 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_estado_inicial | -2.605 | instancia=2de5e75801894f2db760fe6ed8ecc52e;ip=10.0.0.162;estado=healthy;razon= |
| target_estado_inicial | -2.604 | instancia=0c4b51ee993948a5b3387d9d245df4d4;ip=10.0.1.221;estado=healthy;razon= |
| k6_inicio | -0.054 | config=M1-P2;rps=50;duracion=600;tipo_falla=colgar |
| falla_computo | 120.188 | tipo=colgar;instancia=0c4b51ee993948a5b3387d9d245df4d4;ip=10.0.1.221 |
| target_unhealthy | 131.849 | instancia=0c4b51ee993948a5b3387d9d245df4d4;ip=10.0.1.221;estado=unhealthy;razon=Target.Timeout |
| tarea_nueva | 168.083 | instancia=346523bebbf44c4ba9474ca60376b984 |
| ecs_target_registrado | 187.166 | mensaje=(service expd03-originacion) registered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438:t |
| target_estado_inicial | 187.656 | instancia=346523bebbf44c4ba9474ca60376b984;ip=10.0.0.137;estado=initial;razon=Elb.RegistrationInProgress |
| target_healthy | 188.761 | instancia=346523bebbf44c4ba9474ca60376b984;ip=10.0.0.137;estado=healthy;razon= |
| ecs_evento | 196.3 | mensaje=(service expd03-originacion) (task 0c4b51ee993948a5b3387d9d245df4d4) (port 8080) is unhealthy in (target-group arn:aws:e |
| tarea_detenida | 196.334 | instancia=0c4b51ee993948a5b3387d9d245df4d4 |
| ecs_target_registrado | 205.838 | mensaje=(service expd03-originacion) deregistered 1 targets in (target-group arn:aws:elasticloadbalancing:us-east-1:366451245438 |
| ecs_evento | 205.842 | mensaje=(service expd03-originacion, taskSet ecs-svc/6042458078041624456) has begun draining connections on 1 tasks. |
| target_draining | 206.021 | instancia=0c4b51ee993948a5b3387d9d245df4d4;ip=10.0.1.221;estado=draining;razon=Target.DeregistrationInProgress |
| servicio_estable | 215.52 | mensaje=(service expd03-originacion) has reached a steady state. |
| target_retirado | 236.845 | instancia=0c4b51ee993948a5b3387d9d245df4d4;ip=10.0.1.221 |
| falla_base | 361.045 | metodo=reboot_force_failover |
| base_failover_inicio | 373.545 | mensaje=Multi-AZ instance failover started. |
| base_reiniciada | 390.974 | mensaje=DB instance restarted |
| rds_evento | 393.42 | mensaje=The user requested a failover of the DB instance. |
| base_failover_fin | 393.42 | mensaje=Multi-AZ instance failover completed |
| k6_fin | 603.196 | codigo_salida=0 |
| observador_error | 604.333 | detalle=EndpointConnectionError: Could not connect to the endpoint URL: "https://rds.us-east-1.amazonaws.com/" |
