# EXP-D03 — Failover de infraestructura (cómputo y base de datos)

Experimento de arquitectura del proyecto Solventa sobre el atributo de disponibilidad. El diseño del experimento está en la wiki del proyecto; los resultados, el análisis y el ADR, en [ANALISIS.md](ANALISIS.md). Este documento resume qué se prueba, las decisiones tomadas antes de ejecutar, la estructura de la carpeta y cómo ejecutar el experimento.

## Qué se prueba

**Hipótesis (wiki, EXP-D03).** Con dos tareas Fargate en dos zonas de disponibilidad detrás del ALB y RDS PostgreSQL Multi-AZ, ajustando únicamente configuración existe una combinación de monitor de salud (intervalo × umbral), draining y pool de conexiones con la que (a) la muerte súbita de una tarea produce una ventana de error ≤ 30 s, sin falsos positivos y sin perder solicitudes aceptadas, y (b) un failover forzado de la base se recupera de extremo a extremo en ≤ 120 s.

**Matriz.** Tres configuraciones del monitor del ALB por dos políticas del pool; cada ejecución se nombra `Mx-Py`.

| Monitor | Intervalo | Timeout | Umbral unhealthy | Umbral healthy |
|---|---|---|---|---|
| M1, agresivo | 5 s | 3 s | 2 | 2 |
| M2, medio | 10 s | 5 s | 2 | 2 |
| M3, valores por defecto de AWS | 30 s | 5 s | 3 | 2 |

| Pool de conexiones | Vida máxima | Inactividad máxima |
|---|---|---|
| P1, valores por defecto | ilimitada | ilimitada |
| P2, con reciclaje | 30 s | 10 s |

Cada ejecución dura 10 minutos a 50 solicitudes/s: en el segundo 120 se inyecta la falla de cómputo y en el 360 se fuerza el failover de RDS. Además de la matriz se ejecutan las complementarias C1 (retiro planeado con `stop-task`), C2 (crash) y C3 (health check profundo), dos repeticiones de la configuración ganadora y la extensión C4 (tercera tarea), C5 (health check de contenedor en ECS) y C6 (carga en rampa hasta saturación, con M1 y con M2).

## Decisiones tomadas antes de ejecutar

| Decisión | Opción adoptada | Por qué |
|---|---|---|
| Falla de cómputo de la matriz | Hang: proceso vivo que no responde | Es la única falla que el monitor detecta por intervalo × umbral; el crash lo detecta el orquestador y el stop-task es un retiro planeado, por eso van como complementarias |
| Profundidad del health check | Superficial en la matriz; profundo solo en C3 | El chequeo profundo acopla el monitor a la base y se prueba aparte para medir su riesgo |
| Entrada al staging | API Gateway REST con API key, VPC Link V2 y ALB interno | Misma puerta de entrada que el modelo de despliegue |
| Red del staging | Subredes públicas con IP pública en las tareas, sin NAT Gateway ni VPC endpoints | Ahorro de costo y acceso directo a una tarea para inyectar la falla; no cambia lo que se mide |
| Generación de carga | k6 desde el equipo propio, en modelo abierto | Costo cero; la ventana de error no depende de la latencia absoluta, y cada ejecución trae su propia línea base |
| Base y cómputo | RDS `db.t4g.micro` Multi-AZ y tareas de 0,25 vCPU en `us-east-1` | Mínimo costo con la misma topología del diseño |
| Infraestructura | Terraform, staging efímero destruido al final de cada sesión, presupuesto de US$ 15 con alertas | Control de costo sobre una cuenta personal |
| Medición | k6 con identificador por solicitud, timeline de eventos del ALB, ECS y RDS, e identificadores persistidos en la base | Permite cruzar por marca de tiempo y comprobar que ninguna solicitud aceptada se pierde |
| Evidencia versionada | Resúmenes, series, timelines y gráficas; los CSV crudos de k6 quedan fuera | Tamaño del repositorio |
| Muestras | Una ejecución por configuración, tres de la ganadora | Dispersión de la decisión final sin repetir configuraciones que ya fallan |

## Estructura de carpetas

```
servicio-originacion/   Servicio de originación mínimo en Go que se pone bajo prueba: POST /oferta
                        escribe una fila en PostgreSQL, GET /health responde al monitor del ALB y
                        /admin/* permite inyectar fallas y leer evidencia (solo en staging).
carga/                  Script de carga de k6: 50 solicitudes/s en modelo abierto, con perfil
                        constante o en rampa.
ambiente-local/         Ejecución sin costo con Docker Compose: HAProxy como monitor, dos instancias
                        del servicio y PostgreSQL. Scripts ejecucion.ps1 (Windows) y ejecucion.sh
                        (Linux o macOS).
ambiente-aws/           Ejecución en AWS.
  infraestructura/        Terraform del staging efímero: red, RDS Multi-AZ, Secrets Manager, ALB
                          interno, ECS Fargate, API Gateway REST con VPC Link V2 y presupuesto.
                          configs/ contiene un archivo de variables por configuración de la matriz.
  ejecuciones/            Scripts de Python que ejecutan las pruebas: ejecucion.py (una),
                          matriz.py (varias), observador.py (timeline de eventos), staging.py
                          (estado del staging) y verificar_limpieza.py (recursos vivos y costo).
  publicar_imagen.ps1     Construye la imagen del servicio y la publica en ECR.
analisis/               Analizador que convierte la evidencia de cada ejecución en métricas,
                        veredicto por criterio y gráficas.
resultados/             Evidencia por ejecución: resumen.md, resumen.json, serie_por_segundo.csv,
                        timeline.csv, meta.json y gráficas. matriz.md consolida todas las ejecuciones
                        y figuras/ contiene las figuras del análisis.
```

## Ejecución local

Requiere únicamente Docker Desktop. Puertos usados: 18080 (HAProxy), 18081 y 18082 (instancias a y b).

Desde `ambiente-local/`, en Windows:

```powershell
.\ejecucion.ps1 -Config M1 -HcInter 5s -HcFall 2 -HcTimeout 3s
```

En Linux o macOS:

```bash
CONFIG=M1 HC_INTER=5s HC_FALL=2 HC_TIMEOUT=3s ./ejecucion.sh
```

La ejecución dura 10 minutos: carga constante de 50 solicitudes/s, hang de la instancia `a` en el segundo 120 y terminación abrupta de la base en el segundo 360, que se reinicia 45 s después. Al terminar, la evidencia y el análisis quedan en `resultados/<RunId>/`. Para una prueba corta de 2 minutos:

```powershell
.\ejecucion.ps1 -Config smoke -Duracion 120 -TFallaComputo 30 -TFallaBase 75 -BaseCaidaSegundos 20
```

Parámetros: `-HcInter`, `-HcFall`, `-HcRise` y `-HcTimeout` configuran el monitor; `-PoolMaxLifetime` y `-PoolMaxIdleTime` el pool de conexiones; `-HealthMode` (`superficial` o `profundo`) la profundidad del health check; `-TipoFalla` (`hang` o `crash`) la falla de cómputo. En bash los mismos parámetros son variables de entorno con el nombre en mayúsculas.

Para detener el ambiente:

```powershell
docker compose --profile carga --profile analisis down -v
```

## Ejecución en AWS con una cuenta propia

Es posible reproducir el experimento en su propia cuenta de AWS. Requisitos: AWS CLI configurado con las credenciales de esa cuenta (`aws configure`, con un usuario IAM con permisos de administrador), Terraform, Docker, k6 y Python 3 con `pip install -r ambiente-aws/ejecuciones/requirements.txt`. El staging cuesta cerca de US$ 0,15 por hora en `us-east-1` y se destruye al final de cada sesión; el experimento completo costó menos de US$ 5.

Todos los comandos se ejecutan desde esta carpeta.

1. Crear el repositorio de imágenes y publicar el servicio:

   ```powershell
   terraform -chdir=ambiente-aws\infraestructura init
   terraform -chdir=ambiente-aws\infraestructura apply -target=aws_ecr_repository.stub
   .\ambiente-aws\publicar_imagen.ps1 -Tag v1
   ```

2. Aprovisionar el staging con la primera configuración de la matriz (unos 15 minutos, por RDS Multi-AZ):

   ```powershell
   terraform -chdir=ambiente-aws\infraestructura apply -var-file=configs/M1-P1.tfvars -var imagen_tag=v1
   ```

3. Ejecutar una prueba corta y luego la matriz completa. `matriz.py` aplica cada configuración con Terraform, espera a que el staging esté estable y ejecuta la prueba de 10 minutos:

   ```powershell
   python ambiente-aws\ejecuciones\ejecucion.py --config M1-P1 --run-id smoke-aws --duracion 120 --t-falla-computo 30 --t-falla-base 75
   python ambiente-aws\ejecuciones\matriz.py --imagen-tag v1
   ```

   Las ejecuciones complementarias y de extensión se lanzan con el mismo script, por ejemplo:

   ```powershell
   python ambiente-aws\ejecuciones\matriz.py --configs M1-P1 --imagen-tag v1 --tipo-falla stop-task --run-id C1-retiro-planeado
   python ambiente-aws\ejecuciones\matriz.py --configs C3-profundo --imagen-tag v1 --run-id C3-health-profundo
   python ambiente-aws\ejecuciones\matriz.py --configs C6a-rampa-M1 --imagen-tag v1 --run-id C6a-rampa-M1 --extra "--perfil rampa --rps-max 450 --sin-falla-computo --sin-failover"
   ```

4. Destruir el staging y comprobar que no quedó nada en ejecución:

   ```powershell
   terraform -chdir=ambiente-aws\infraestructura destroy
   python ambiente-aws\ejecuciones\verificar_limpieza.py
   ```

Para volver a analizar una ejecución o consolidar varias:

```powershell
python analisis\analizar.py resultados\M1-P1
python analisis\analizar.py --consolidar resultados\M1-P1 resultados\M1-P2 > resultados\matriz.md
```
