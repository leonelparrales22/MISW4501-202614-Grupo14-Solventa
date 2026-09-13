# EXP-D03 — Failover de infraestructura: análisis de resultados y decisión de arquitectura

> Estado: completo. Matriz principal (6 ejecuciones), complementarias C1 a C3, dos repeticiones de la configuración ganadora y extensión C4 a C6, todas en AWS, más la validación local. Tabla consolidada en `resultados/matriz.md`.

## 0. Preámbulo: qué se quería probar y cómo se hizo

Nuestro modelo de despliegue promete que la plataforma resiste la caída de una instancia de un servicio y el failover de su base de datos sin romper el requisito RC-02: disponibilidad del 99,9 % mensual, que equivale a un presupuesto de 43 minutos de indisponibilidad al mes, y un RTO de 10 minutos. Para eso el diseño usa dos mecanismos: redundancia activa de cómputo, dos tareas Fargate por servicio repartidas en dos zonas de disponibilidad detrás de un Application Load Balancer (ALB), y redundancia pasiva de datos, RDS PostgreSQL Multi-AZ con una réplica síncrona en la otra zona que se promueve a primaria cuando la primaria falla.

Lo que nadie había medido es cuánto tiempo ven errores los clientes mientras esos mecanismos actúan. Ese tiempo no lo fija AWS: lo fijan parámetros de configuración que el equipo dejó abiertos en el diagrama de despliegue con la nota "EXP-D03: health checks (intervalo × umbral) + draining". Este experimento cierra esa nota con números.

**Los tres mecanismos que se midieron**

- **El monitor de salud del ALB.** El balanceador le pregunta a cada tarea, cada cierto intervalo, si está activa, llamando a su interfaz de control `GET /health`. Si la tarea no responde antes del **timeout** del chequeo, ese chequeo falla. Cuando acumula tantos fallos seguidos como diga el **umbral unhealthy**, el ALB declara la tarea unhealthy y deja de enviarle tráfico. Por tanto, el tiempo que el ALB tarda en darse cuenta de que una tarea murió es, aproximadamente, intervalo × umbral, más los timeouts. Un monitor agresivo detecta rápido, pero corre el riesgo de retirar tareas sanas que simplemente respondieron lento: eso es un falso positivo, y es el trade-off central del experimento.
- **El reemplazo por ECS.** Cuando el ALB retira una tarea, el orquestador ECS la reemplaza: lanza una tarea nueva, que descarga la imagen, lee la credencial de la base en Secrets Manager, arranca y pasa el chequeo del ALB. Ese proceso completo es la **reintegración**. Mientras dura, el servicio opera con una sola tarea.
- **El failover de RDS Multi-AZ.** Cuando la primaria falla, RDS promueve la standby síncrona y cambia el nombre DNS del endpoint para que apunte a ella. Las conexiones abiertas contra la primaria vieja mueren, y el pool de conexiones de la aplicación, el conjunto de conexiones abiertas que reutiliza para no abrir una por solicitud, tiene que descartarlas y abrir nuevas contra la nueva primaria. Si no lo hace bien, la base vuelve pero la aplicación sigue fallando.

**Cómo se ejecutó.** Se construyó un set de pruebas con un servicio de originación mínimo en Go, que reproduce el journey crítico con una escritura real en PostgreSQL por solicitud, y se desplegó en un staging efímero en AWS con Terraform, réplica reducida de cómo está plasmado en el modelo de despliegue: API Gateway REST con API key, VPC Link V2, ALB interno, dos tareas Fargate y RDS Multi-AZ. Cada ejecución duró 10 minutos con 50 solicitudes por segundo generadas con k6 desde un equipo local. En el segundo 120 se provocó una falla de cómputo en una de las tareas y en el segundo 360 se forzó el **failover** de la base. Se repitió ese protocolo para cada combinación de la matriz: tres configuraciones del monitor por dos políticas del pool, y luego ejecuciones complementarias que aíslan mecanismos concretos. Antes de gastar en AWS se validó todo el flujo en un ambiente local con Docker Compose.

**Qué se midió.** Para cada ejecución se cruzaron tres fuentes por marca de tiempo: lo que vio cada solicitud de k6, los eventos de la infraestructura (transiciones de salud del ALB, eventos de ECS y de RDS) y las filas que quedaron en la base. De ahí salen la **ventana de error**, el tiempo entre el primer y el último error visible al cliente; la **detección**, cuánto tardó el ALB en retirar la tarea; la **reintegración**; los **falsos positivos**; las **solicitudes aceptadas perdidas**, respuestas 201 sin fila en la base; y la **recuperación tras el failover**, cuánto tardó la aplicación en volver a escribir después de ordenar el failover.

**Qué se encontró.** La hipótesis se confirma: con un monitor de 5 s por 2 fallos, una tarea que muere produce entre 17 y 19 s de errores y un failover de la base entre 21 y 32 s, ambos dentro de lo prometido, sin falsos positivos y sin perder ninguna solicitud aceptada; los valores por defecto de AWS, en cambio, dejan a los clientes más de 90 s en error.

## 1. Hipótesis y criterios de aceptación

**Hipótesis de diseño (wiki, EXP-D03).** Con dos tareas Fargate en dos zonas de disponibilidad detrás del ALB y RDS PostgreSQL Multi-AZ, ajustando únicamente configuración existe una combinación de monitor de salud (intervalo × umbral), draining y pool de conexiones con la que (a) la muerte súbita de una tarea produce una ventana de error ≤ 30 s, sin falsos positivos del monitor y sin perder solicitudes aceptadas, y (b) un failover forzado de la base se recupera de extremo a extremo en ≤ 120 s.

| Criterio | Qué mide | Umbral |
|---|---|---|
| (a1) Ventana de error ante la falla de una tarea | Segundos entre el primer y el último error visible al cliente atribuibles a la falla de cómputo | ≤ 30 s |
| (a2) Sin falsos positivos del monitor | Veces que el ALB declara unhealthy a una tarea que no tiene la falla | 0 |
| (a3) Sin solicitudes aceptadas perdidas | Respuestas 201 cuya fila no existe en la base al final | 0 |
| (b) Recuperación tras el failover de la base | Segundos entre la orden de failover y el último error visible al cliente | ≤ 120 s |

Una configuración cumple solo si satisface los cuatro criterios.

## 2. Diseño de la ejecución

### 2.1 Las configuraciones de la matriz

| Configuración del monitor | Intervalo | Timeout | Umbral unhealthy | Umbral healthy | Detección teórica |
|---|---|---|---|---|---|
| M1, agresivo | 5 s | 3 s | 2 | 2 | 10 a 16 s |
| M2, medio | 10 s | 5 s | 2 | 2 | 20 a 30 s |
| M3, valores por defecto de AWS | 30 s | 5 s | 3 | 2 | 90 a 105 s |

| Política del pool | `ConnMaxLifetime` | `ConnMaxIdleTime` |
|---|---|---|
| P1, valores por defecto de `database/sql` | ilimitada | ilimitada |
| P2, con reciclaje | 30 s | 10 s |

### 2.2 Las fallas que inyecto

- **Hang**: la tarea sigue viva y acepta conexiones TCP, pero no responde ninguna petición, ni siquiera `/health`. Es la falla de la matriz porque es la única que el monitor tiene que descubrir por sí mismo, chequeo a chequeo.
- **Crash**: el proceso termina. Las conexiones se rechazan de inmediato y tanto el ALB como ECS se enteran sin esperar.
- **Stop-task**: un retiro planeado del servicio, como el de un despliegue. ECS avisa al ALB antes de apagar la tarea y el ALB drena las solicitudes en vuelo durante el **deregistration delay**.
- **Failover forzado de RDS**: `reboot-db-instance --force-failover`, el mismo mecanismo que AWS usa para las pruebas de Multi-AZ.

Las inyecto desde la propia interfaz de administración del stub, que existe solo en staging, con el mismo papel que Toxiproxy cumple para las dependencias externas en la restricción RT-02.

### 2.3 Cómo mido

El generador de carga trabaja en modelo abierto (open model): envía 50 solicitudes por segundo pase lo que pase, sin esperar respuestas, para que la ventana de error se cuente completa y no se acorte porque el generador se frenó. Cada solicitud lleva un identificador único generado por el cliente, que la base guarda como clave primaria; eso me permite cruzar al final lo que k6 reportó como aceptado contra lo que realmente existe en la base. Un observador registra en un timeline, con la marca de tiempo del evento, cada transición de salud de los targets del ALB, cada evento del servicio ECS y cada evento de RDS. Un analizador determinista convierte esas tres fuentes en las métricas y en el veredicto por criterio; cualquiera que lo ejecute sobre los mismos archivos obtiene los mismos números. Cuando un dato de k6 no cuadra, lo contrasto con las métricas del ALB y de RDS en CloudWatch, que miden dentro de AWS.

## 3. Validación del set de pruebas en el ambiente local

Antes de aprovisionar AWS ejecuté el protocolo completo en Docker Compose, con HAProxy como monitor, dos instancias del servicio y un PostgreSQL que termino abruptamente con SIGKILL y vuelvo a arrancar. Ese ambiente no mide la arquitectura real, pero confirmó que la inyección de fallas, la reconexión del pool y el analizador funcionan, y anticipó dos lecciones que AWS después confirmó.

| Ejecución local | Monitor | Detección | Ventana cómputo | Reintegración | Falsos positivos | Base caída | Recuperación app | Retardo pool | Perdidas | Ambiguas |
|---|---|---|---|---|---|---|---|---|---|---|
| `smoke-local` (2 min) | 5 s × 2, timeout 3 s | 11,4 s | 16,4 s | 34,4 s | 0 | 20,8 s | 22,2 s | 1,4 s | 0 | 1 |
| `local-M2-P1` (10 min) | 10 s × 2, timeout 5 s | 26,0 s | 31,2 s | 62,1 s | 0 | 46,0 s | 46,7 s | 0,7 s | 0 | 0 |

1. **La ventana de error es detección más timeout del cliente.** Las solicitudes que el balanceador envió a la tarea en hang justo antes de retirarla siguen esperando hasta que vence el timeout del cliente, 5 s. Con 10 s × 2 la detección llegó a 26 s y la ventana visible a 31,2 s.
2. **El pool reconecta solo cuando la base vuelve.** La aplicación se recuperó en menos de 1,5 s después de que la base estuvo disponible.

El único **resultado ambiguo** apareció aquí: una solicitud cuya fila sí quedó en la base pero cuyo cliente recibió error, porque la conexión se cortó justo al terminar la base. El cliente no puede saber si su oferta existe; es el caso que justifica los reintentos idempotentes en el borde.

## 4. Resultados en AWS

### 4.1 Matriz principal

Todas las ejecuciones: 10 min, 50 req/s, hang en el segundo 120, failover forzado en el segundo 360, health check superficial, deregistration delay 30 s, tareas de 0,25 vCPU y 0,5 GB, RDS `db.t4g.micro` Multi-AZ.

| Ejecución | Monitor | Pool | Detección | Ventana cómputo | Reintegración | Falsos positivos | Recuperación base | Retardo pool | Perdidas | Disponibilidad de la ejecución | Cumple |
|---|---|---|---|---|---|---|---|---|---|---|---|
| M1-P1 | 5 s × 2, 3 s | ilimitado | 13,3 s | **19,2 s** | 64,3 s | 0 | 24,5 s | 0 | 0 | 96,28 % | **sí** |
| M1-P2 | 5 s × 2, 3 s | 30 s / 10 s | 11,7 s | **18,1 s** | 68,6 s | 0 | 26,2 s | 0 | 0 | 95,81 % | **sí** |
| M2-P1 | 10 s × 2, 5 s | ilimitado | 25,4 s | 31,2 s | 83,3 s | 0 | 25,5 s | 0,3 s | 0 | 95,55 % | no (a1) |
| M2-P2 | 10 s × 2, 5 s | 30 s / 10 s | 26,1 s | 32,2 s | 82,2 s | 0 | 21,4 s | 0 | 0 | 94,90 % | no (a1) |
| M3-P1 | 30 s × 3, 5 s | ilimitado | 92,7 s | 99,5 s | 120,2 s | 0 | 29,2 s | 0 | 0 | 89,64 % | no (a1) |
| M3-P2 | 30 s × 3, 5 s | 30 s / 10 s | 89,2 s | 95,0 s | 131,7 s | 0 | 71,3 s ¹ | 45,5 s ¹ | 0 | 86,69 % | no (a1) |

¹ Contaminado por el trayecto de red desde mi portátil; ver 4.5.

La disponibilidad de la ejecución es la fracción de solicitudes exitosas en los 10 minutos con dos fallas inyectadas; sirve para comparar configuraciones entre sí, no como estimación mensual. La evidencia de cada ejecución está en `resultados/<ejecución>/`: `resumen.md`, `timeline.csv`, `grafica_errores.png` y `grafica_p95.png`.

### 4.2 Lectura por criterio

**(a1) Ventana de error ≤ 30 s. Solo M1 cumple.** La ventana tiene dos componentes que el experimento separa con claridad. El primero es la detección del monitor, que coincide con la teoría intervalo × umbral más timeouts: M1 debía tardar entre 10 y 16 s y tardó 11,7 y 13,3 s; M2, entre 20 y 30 s, y tardó 25,4 y 26,1 s; M3, entre 90 y 105 s, y tardó 89,2 y 92,7 s. El segundo son los 5 s de timeout del cliente que pagan las solicitudes enrutadas a la tarea en hang justo antes del retiro. M2 falla por 1,2 y 2,2 s, así que con un timeout de cliente de 5 s la detección debe cerrar en unos 25 s. M3, la configuración que AWS trae por defecto, deja a los clientes entre 95 y 100 s de errores: dejar el monitor sin configurar no es una decisión válida para RC-02.

**(a2) Sin falsos positivos. Cumple en las seis.** Ni siquiera M1, el monitor agresivo con tareas de 0,25 vCPU, retiró una tarea sana. El trade-off que motivó el experimento no se materializó a 50 req/s; la sección 4.6.3 lleva las tareas a saturación para comprobarlo.

**(a3) Sin solicitudes aceptadas perdidas. Cumple en las seis.** Toda respuesta 201 tiene su fila en la base, y en AWS no hubo resultados ambiguos.

**(b) Recuperación tras el failover ≤ 120 s. Cumple en las seis**, con 21 a 29 s en cinco de ellas. El failover real de RDS, desde el evento "Multi-AZ instance failover started" hasta "DB instance restarted", tomó entre 16 y 25 s, y la orden de reinicio tarda 6 a 8 s en producir el primer evento. El **retardo de reconexión del pool fue cero**: la aplicación volvió a escribir en el mismo segundo en que RDS reportó la nueva primaria. La razón es que el driver `pgx` v5 verifica cada conexión inactiva antes de reutilizarla y descarta las que murieron con la primaria vieja; por eso ni la vida máxima de la conexión (P1 frente a P2) ni un RDS Proxy cambian el resultado.

**Reintegración.** No es un criterio de la hipótesis, pero la mido porque define cuánto tiempo el servicio queda con una sola tarea. La gobierna ECS, no el ALB: en M1 el ALB retiró la tarea a los 12 s, ECS tardó entre 35 y 40 s más en lanzar el reemplazo, y el reemplazo tardó entre 20 y 30 s en arrancar y pasar el chequeo inicial. Un detalle útil para el ADR: un target recién registrado queda healthy con **un solo** chequeo exitoso, así que el umbral healthy solo aplica a targets que ya estuvieron unhealthy y no alarga la reintegración.

**Qué significa frente a RC-02.** Con M1, cada hang de una tarea cuesta unos 19 s de errores visibles y cada failover de la base unos 25 s. Frente a los 43 minutos de presupuesto mensual, el mecanismo de failover consume una fracción mínima, y el RTO de 10 minutos se cumple con holgura en los dos flancos.

### 4.3 Ejecuciones complementarias, sobre M1

| Ejecución | Falla de cómputo | Detección | Ventana cómputo | Fallidas en la ventana | Reintegración | Falsos positivos | Recuperación base | Perdidas | Cumple |
|---|---|---|---|---|---|---|---|---|---|
| C1 retiro planeado del servicio | `stop-task` | no aplica | **0 s** | 0 | 34,5 s | 0 | 20,5 s | 0 | sí |
| C2 crash | `crash` | 8,5 s | **9,6 s** | 228, todas 502 | 52,7 s | 0 | 24,6 s | 0 | sí |
| C3 health check profundo | `hang` | 13,5 s | 20,2 s | 376 | 77,6 s | **2** | 24,0 s | 0 | **no (a2)** |

- **C1 valida la táctica de retiro de operación.** Al detener la tarea con `stop-task`, ECS deregistró el target del ALB, el ALB dejó de enviarle solicitudes nuevas y drenó las que estaban en vuelo dentro del deregistration delay de 30 s. Ninguna solicitud falló. ECS lanzó el reemplazo medio segundo después del retiro, porque sabía de antemano, y lo reintegró en 34,5 s. Es la referencia para despliegues y mantenimiento: un retiro planeado no consume presupuesto de indisponibilidad.
- **C2 separa la detección por el orquestador de la detección por el monitor.** Ante un crash el proceso desaparece y las conexiones se rechazan de inmediato: el chequeo del ALB falla sin esperar su timeout, la detección baja a 8,5 s y los 228 errores son 502 devueltos por el ALB en vez de timeouts de 5 s, así que la ventana queda en 9,6 s. El crash es la falla benigna; el hang es la que exige un monitor bien parametrizado.
- **C3 muestra el riesgo de acoplar el health check a una dependencia compartida.** Con `/health` haciendo ping a la base, el failover de RDS hizo que el ALB declarara unhealthy a las dos tareas a los 15 y 18 s de la orden, con razón `Target.ResponseCodeMismatch`: dos falsos positivos sin ninguna falla de cómputo. El ALB entró en modo fail-open, es decir, ante todos sus targets unhealthy siguió enrutando a todos, y por eso la ventana de error fue solo la del failover. No hubo cascada de reemplazos únicamente porque la base volvió en 20 s, antes del tiempo de reacción de ECS de 35 a 40 s. Con un failover en el rango que AWS documenta, de 60 a 120 s, ECS habría retirado las dos tareas y sumado un minuto de indisponibilidad total. Descarto el health check profundo.

### 4.4 Repeticiones de la configuración ganadora

| Ejecución | Detección | Ventana cómputo | Reintegración | Falsos positivos | Recuperación base | Retardo pool | Perdidas | Cumple |
|---|---|---|---|---|---|---|---|---|
| M1-P1 | 13,3 s | 19,2 s | 64,3 s | 0 | 24,5 s | 0 | 0 | sí |
| M1-P1, repetición 1 | 12,5 s | 18,7 s | 77,3 s | 0 | 26,1 s | 0 | 0 | sí |
| M1-P1, repetición 2 | 11,3 s | 17,0 s | 51,9 s | 0 | 28,9 s | 0 | 0 | sí |
| **Rango** | 11,3 a 13,3 s | **17,0 a 19,2 s** | 51,9 a 77,3 s | 0 | **24,5 a 28,9 s** | 0 | 0 | **3 de 3** |

Las tres muestras caben con margen: la ventana de error queda 11 s por debajo de los 30 s y la recuperación tras el failover, 90 s por debajo de los 120 s. La dispersión mayor está en la reintegración, que depende del tiempo de reacción de ECS y del arranque de la tarea nueva.

### 4.5 Limitaciones que reconozco

- **Generé la carga desde mi portátil, fuera de AWS.** Lo decidí por costo antes de ejecutar. La consecuencia es que la latencia de extremo a extremo incluye Internet y API Gateway: la línea base es de 90 a 95 ms de p95 contra 3 a 4 ms de tiempo de respuesta medido por el ALB. Dos ejecuciones sufrieron degradación de mi trayecto de red y las contrasté con CloudWatch. En M3-P2 la latencia que vio k6 subió a 500 ms en las dos tareas por igual desde el segundo 90, antes de cualquier falla, mientras el tiempo de respuesta del ALB se mantuvo en 3 a 4 ms, la latencia de escritura de RDS en 1 a 2 ms y la CPU de la base por debajo del 10 %. En C4, en el minuto siguiente al failover, el ALB recibió 1 934 solicitudes en lugar de 3 000 con tiempo de respuesta de 3 ms: las 1 077 que k6 registró como timeout nunca llegaron a AWS. Durante la sesión mi proveedor tuvo además un corte que interrumpió una ejecución, que repetí, y que cambió mi IP pública, por lo que hice que el orquestador la verifique antes de hablar con las tareas. Las métricas de ventana de error, detección y reintegración no dependen de la latencia absoluta y no se vieron afectadas; en las dos ejecuciones contaminadas reporto la recuperación tras el failover con el valor del intervalo de errores 503, que sí ocurre dentro de AWS. Para experimentos futuros el generador debe correr dentro de la VPC, como ya preveía nuestra estrategia de pruebas.
- **La matriz corrió con carga moderada.** 50 req/s sobre tareas de 0,25 vCPU consumen el 3 % de la CPU. Por eso agregué la sección 4.6.3, que lleva las tareas a saturación.

### 4.6 Ejecuciones de extensión

La matriz dejó dos incertidumbres: si el tiempo de reacción de ECS se puede reducir y si el monitor agresivo produce falsos positivos cuando la tarea está saturada. Las cerré con cuatro ejecuciones más, todas sobre M1.

#### 4.6.1 C4, tercera tarea

Igual que M1-P1 con `desired_count = 3`.

| Métrica | M1-P1, 2 tareas, 3 muestras | C4, 3 tareas |
|---|---|---|
| Detección | 11,3 a 13,3 s | 13,2 s |
| Ventana de error | 17,0 a 19,2 s | 19,3 s |
| Solicitudes fallidas en la ventana | 328 a 350 | **230** |
| Fracción del tráfico afectada durante la detección | 1/2 | **1/3** |
| Reintegración | 51,9 a 77,3 s | 48,8 s |
| Recuperación tras failover | 24,5 a 28,9 s | 32 s ² |

² Valor del intervalo de errores 503; el minuto siguiente está contaminado por mi trayecto de red (4.5).

La tercera tarea no acorta la ventana de error, que sigue gobernada por la detección del monitor más el timeout del cliente, pero reduce en un tercio las solicitudes que caen en ella y deja dos tareas sanas durante el minuto que ECS tarda en reintegrar, en lugar de una. Es una decisión de capacidad y costo, no de detección: aporta si quiero que un incidente afecte a menos clientes o que el servicio no quede en instancia única mientras ECS reacciona.

#### 4.6.2 C5, health check de contenedor

Igual que M1-P1, agregando en la definición de tarea de ECS un health check de contenedor: un comando que ECS ejecuta dentro del contenedor cada 5 s, con timeout de 3 s, 2 reintentos y un startPeriod de 15 s. La idea era que ECS detectara el hang por sí mismo, sin esperar a enterarse por el ALB, y reemplazara antes.

| Métrica | M1-P1, 3 muestras | C5, con health check de contenedor |
|---|---|---|
| Detección por el ALB | 11,3 a 13,3 s | 13,9 s |
| Ventana de error | 17,0 a 19,2 s | 19,6 s |
| ECS lanza el reemplazo | 48 a 55 s después de la falla | 46 s después de la falla |
| ECS registra la tarea nueva en el ALB | 28 a 30 s después de lanzarla | **60 s** después de lanzarla |
| Reintegración | 51,9 a 77,3 s | **108,6 s** |
| Falsos positivos | 0 | 0 |
| Recuperación tras failover | 24,5 a 28,9 s | 27,8 s |

El health check de contenedor adelantó muy poco el lanzamiento del reemplazo, de 48 a 55 s a 46 s, porque ECS evalúa la salud de los contenedores con su propia cadencia y no de inmediato. En cambio, retrasó el registro de la tarea nueva en el ALB: con un health check de contenedor definido, ECS no registra el target hasta que ese chequeo lo declara HEALTHY, y eso suma el startPeriod de 15 s más los ciclos del chequeo. El resultado neto es una reintegración de 108,6 s, entre 30 y 55 s peor que sin él. La ventana de error y los falsos positivos no cambian, porque el monitor del ALB sigue mandando en el retiro. Concluyo que el tiempo de reacción de ECS no se reduce por configuración; la palanca efectiva contra la instancia única es la tercera tarea de C4.

#### 4.6.3 C6, carga en rampa sin fallas: falsos positivos bajo saturación

Para probar el trade-off que motivó el experimento necesitaba tareas saturadas, y a 50 req/s el stub usa el 3 % de su CPU. Por eso le agregué un costo de CPU configurable por solicitud, que emula el trabajo de perfilamiento y rating del servicio real, y lo fijé en 10 ms. Con tareas de 0,25 vCPU, eso da una capacidad teórica de 25 solicitudes por segundo por tarea, 50 para las dos. Sin inyectar ninguna falla, k6 sube la carga en escalones de dos minutos: 50, 113, 225, 338 y 450 req/s. Cada solicitud que se vence a los 5 s retiene un VU de k6, así que con 900 VUs el generador quedó limitado a unas 180 req/s efectivas; fue suficiente, porque la saturación se alcanza en el primer escalón.

**C6a, con M1 (timeout 3 s).**

| Minuto | req/s recibidas | Fallidas | p95 exitosas | Transiciones a unhealthy | CPU media de las tareas |
|---|---|---|---|---|---|
| 0 | 50 | 0 | 293 ms | 0 | 52 % |
| 1 | 103 | 5 993 | 2 608 ms | **2** | 99 % |
| 2 | 113 | 6 691 | 1 007 ms | 2 | 54 % |
| 3 a 8 | 174 a 181 | 10 440 a 10 802 por minuto | 1 800 a 2 400 ms | 4 en total | 75 a 100 % |
| 9 | 69 | 4 084 | 1 399 ms | 0 | 75 % |

A 50 req/s, la capacidad exacta de las dos tareas, no hubo errores ni falsos positivos. En el primer escalón por encima, 103 req/s, las dos tareas llegaron al 100 % de CPU, el health check tardó más de los 3 s de timeout y a los 81 s el ALB declaró unhealthy a las dos a la vez, con razón `Target.Timeout`, sin que ninguna tuviera falla. A partir de ahí se encadenó una **tormenta de reemplazos**: ECS retiró las dos tareas y lanzó otras dos, que quedaron saturadas en cuanto entraron al balanceador y fueron retiradas de nuevo; el ciclo se repitió seis veces en nueve minutos, con nueve falsos positivos en total. El ALB, en modo fail-open, siguió enrutando a targets unhealthy, pero el tiempo de respuesta medido por el ALB subió a más de 9 s, casi su idle timeout de 10 s, y el 95 % de las 84 589 solicitudes venció por timeout. Ninguna solicitud aceptada se perdió: las 3 401 respuestas 201 tienen su fila en la base.

Lo que este resultado dice es preciso: el monitor de M1 distingue perfectamente una tarea muerta de una viva, pero no distingue una tarea viva de una tarea viva y saturada, porque un health check que corre en el mismo proceso compite por la CPU con las solicitudes. Bajo una sobrecarga sostenida sin autoescalado, retirar la tarea saturada no ayuda a nadie: la que la reemplaza hereda la misma sobrecarga. El mecanismo de protección se convierte en la causa de la indisponibilidad.

**C6b, con M2 (timeout 5 s).** Misma rampa con el monitor medio, para saber si un timeout más largo tolera la saturación.

| Minuto | req/s recibidas | Fallidas | p95 exitosas | Transiciones a unhealthy | CPU media de las tareas |
|---|---|---|---|---|---|
| 0 | 50 | 0 | 292 ms | 0 | 88 % |
| 1 | 103 | 5 984 | 2 583 ms | **2** | 65 % |
| 2 | 113 | 6 755 | 714 ms | 1 | 86 % |
| 3 a 8 | 175 a 181 | 10 458 a 10 810 por minuto | 1 400 a 3 100 ms | 4 en total | 50 a 100 % |
| 9 | 68 | 3 898 | 1 669 ms | 1 | 56 % |

El resultado es el mismo con diez segundos de diferencia: la primera pareja de falsos positivos llegó a los 91 y 95 s en vez de a los 81 s, ECS lanzó ocho tareas de reemplazo en vez de seis, y el 96 % de las 84 560 solicitudes venció por timeout. Con las tareas saturadas, el tiempo de respuesta medido por el ALB se fue a 9 s, muy por encima de los 5 s de timeout de M2; subir el timeout de 3 a 5 s solo retrasa el primer falso positivo.

**Qué concluyo de C6.** El riesgo de falsos positivos que motivaba el experimento existe, pero no lo gobierna el intervalo ni el timeout del monitor: lo gobierna la capacidad. Mientras la CPU de las tareas tiene margen, ni M1 ni M2 producen un solo falso positivo, como muestran las dieciséis ejecuciones a 50 req/s y el primer minuto de las dos rampas. Cuando la carga supera la capacidad de forma sostenida, cualquier monitor razonable retira las tareas saturadas y ECS las reemplaza por otras que se saturan igual. El monitor no es una señal de capacidad y no debe usarse como tal; el mecanismo que evita este escenario es el autoescalado por CPU, que el escenario EC-E03 ya exige con reacción en 60 s o menos, disparado antes de que la CPU llegue al punto en que el health check deja de responder a tiempo. Por eso mantengo M1, que gana en detección, y agrego el autoescalado como prerrequisito explícito del ADR.

## 5. Qué significa para la arquitectura

### 5.1 Decisiones que confirmo

- **Redundancia activa de dos tareas por servicio en dos AZ, con failover por el monitor del ALB (HA-D01, HA-D02, RC-02).** Con M1, un hang produce entre 17 y 19 s de errores y la tarea sobreviviente absorbe el 100 % del tráfico sin degradar el p95.
- **RDS PostgreSQL Multi-AZ con failover automático.** Recuperación de extremo a extremo entre 21 y 32 s en las diez ejecuciones con failover válidas; la promoción de la standby tomó entre 16 y 25 s.
- **Health Check API superficial con detección por monitor en esquema ping/echo.** Detección igual a la teórica y cero falsos positivos en todas las ejecuciones con health superficial.
- **Retiro de operación con draining.** C1: cero errores en un retiro planeado con deregistration delay de 30 s.
- **Pool de conexiones sin RDS Proxy.** El pool reconecta sin intervención. La fila "la base se recupera pero la aplicación sigue fallando" de la tabla de interpretación de la wiki no ocurrió, así que no incorporo RDS Proxy al despliegue.

### 5.2 Decisiones que corrijo o preciso en el modelo de despliegue

- **Convierto la nota del ALB en una decisión.** Reemplazo "EXP-D03: health checks (intervalo × umbral) + draining" por los valores del ADR de la sección 6. Dejar los valores por defecto de AWS incumple RC-02 por un factor de tres.
- **Fijo la profundidad del health check en superficial.** El diagrama no la declaraba. C3 demuestra que el health profundo convierte un failover de la base en dos falsos positivos del monitor y, con failovers largos, en una cascada de reemplazos.
- **Acoto el timeout en el borde.** La ventana visible es detección más timeout del cliente. API Gateway REST trae 29 s de timeout de integración; para que un socio no espere más que el presupuesto, fijo el timeout de integración de las rutas de cotización en el orden de 5 s y recomiendo a los socios un timeout equivalente.
- **Agrego reintentos idempotentes en el borde.** Los 17 a 19 s de errores de M1 son, en su mayoría, solicitudes que un reintento inmediato con la misma clave de idempotencia habría resuelto en la tarea sana. API Gateway no reintenta, así que el reintento debe vivir en el SDK del socio y en los clientes web y móvil, con la clave de idempotencia que ya exige HA-D02. Lo propongo como historia de arquitectura para la etapa 2.
- **Preciso el tipo de API Gateway y la versión del VPC Link.** El diagrama no los declaraba. Las funciones de HA-01, API keys, usage plans y caché, exigen API REST, y con VPC Link V2 la API REST se integra directamente al ALB. Así construí el staging y funcionó.
- **Hago interno el ALB.** Un ALB público en subred pública es una segunda puerta que salta el rate limiting y la autenticación por socio. Con VPC Link V2, el security group del ALB admite únicamente al VPC Link, como quedó en el staging.
- **Agrego la salida a Internet de las subredes privadas.** El diagrama pone las tareas en subredes privadas sin NAT Gateway ni VPC endpoints. Sin esa salida, la tarea de reemplazo no puede descargar la imagen ni leer la credencial, y la reintegración que medí aquí nunca ocurriría; el servicio de notificaciones tampoco alcanzaría a Firebase. Propongo un NAT Gateway por AZ y el gateway endpoint de S3.
- **Devuelvo la generación de carga al staging.** Nuestra estrategia de pruebas preveía k6 dentro de AWS; lo corrí desde fuera por costo, y la limitación 4.5 muestra por qué la previsión original era la correcta.

### 5.3 Incertidumbres que cierra la extensión

- **Tiempo de reacción de ECS: cerrada.** C5 muestra que no se reduce por configuración: el health check de contenedor adelanta apenas el lanzamiento del reemplazo y retrasa su registro en el ALB, con una reintegración neta peor. C4 muestra que la palanca real es la tercera tarea: no cambia la ventana de error, pero reduce en un tercio las solicitudes afectadas y evita la instancia única mientras ECS reacciona. Mantengo dos tareas por defecto y reservo la tercera para los servicios del camino crítico de cotización.
- **Falsos positivos bajo carga alta: cerrada.** C6 muestra que con margen de CPU no aparecen con ningún monitor, y que bajo saturación sostenida aparecen con cualquiera, porque el health check compite por la CPU con las solicitudes. La respuesta correcta no es relajar el monitor sino garantizar capacidad: autoescalado por CPU con reacción en 60 s o menos y margen de reserva, de modo que las tareas nunca lleguen al punto en que un chequeo tarda más que su timeout. Lo incorporo al ADR como prerrequisito de M1.

## 6. Registro de decisión ADR-02: parámetros del plano de despliegue para el failover de infraestructura

**Contexto.** RC-02 exige 99,9 % mensual y RTO ≤ 10 min. El modelo de despliegue v2.2 fija redundancia de dos tareas por AZ y RDS Multi-AZ, pero dejaba abiertos los parámetros que gobiernan la ventana de error.

**Decisión.**

| Parámetro | Valor | Evidencia |
|---|---|---|
| Health check del target group | intervalo 5 s, timeout 3 s, umbral unhealthy 2, umbral healthy 2, ruta `/health`, respuesta 200 | M1-P1, M1-P2 y dos repeticiones: detección 11,3 a 13,3 s, ventana 17 a 19 s, cero falsos positivos |
| Profundidad del health check | superficial: proceso vivo y listo, sin consultar la base | C3: el health profundo produce falsos positivos durante el failover |
| Deregistration delay | 30 s | C1: retiro planeado sin errores |
| Idle timeout del ALB | 10 s | Acota el tiempo que tarda en fallar una petición a una tarea en hang |
| Pool de conexiones de Go | `database/sql` + `pgx` v5, vida máxima ilimitada (valores por defecto) | P1 y P2 indistinguibles; retardo de reconexión 0 s |
| RDS Proxy | no se incorpora | Criterio (b) cumplido en todas las ejecuciones |
| Timeout de integración en API Gateway para rutas de cotización | 5 s | Ventana = detección + timeout del cliente |
| Tareas por servicio | 2 por defecto; 3 en los servicios del camino crítico de cotización si quiero evitar la instancia única durante la reacción de ECS | C4 |
| Health check de contenedor en ECS | no se agrega | C5: retrasa el registro de la tarea nueva en el ALB y empeora la reintegración |
| Autoescalado del servicio, prerrequisito del monitor | target tracking por CPU del servicio con objetivo del 60 % y reacción en 60 s o menos, como exige EC-E03 | C6a y C6b: bajo saturación sostenida cualquier monitor produce falsos positivos y una tormenta de reemplazos |

**Consecuencias.** La ventana de error ante un hang de una tarea queda entre 17 y 19 s, y ante un failover de la base entre 21 y 32 s, ambas dentro de la hipótesis. El servicio opera con una sola tarea entre 50 y 80 s por incidente, hasta que ECS reintegra el reemplazo. Los reintentos idempotentes en el borde reducen la ventana visible al socio sin tocar la infraestructura. El monitor M1 es seguro solo mientras las tareas conservan margen de CPU: el autoescalado no es una optimización de costo sino la condición que impide que el propio monitor cause la indisponibilidad.

## 7. Evidencia y reproducibilidad

Todo el set de pruebas está en esta carpeta y se describe en [README.md](README.md): el servicio bajo prueba, el script de carga, el ambiente local, la infraestructura del staging como código, los orquestadores de ejecución, el analizador y los resultados de cada ejecución.
