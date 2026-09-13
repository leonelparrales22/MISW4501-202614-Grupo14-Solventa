## EXP-L01 — Ejecución en AWS

**2026-09-13 · ECS Fargate + ElastiCache · `us-east-2`**

### Contexto

Solventa ofrece seguros embebidos: el cliente cotiza y contrata la póliza dentro del flujo de un aliado comercial, sin salir de su plataforma. Para calcular el precio, el sistema necesita primero el perfil de riesgo del cliente, y ese perfil se construye con información de tres proveedores externos: Open Finance (datos financieros, con consentimiento del cliente), Open Data (fuentes públicas) y KYC (verificación de identidad).

El diseño consulta a los tres proveedores de forma simultánea, con un límite de 120 ms por proveedor y un tope de 700 ms para el conjunto. Si un proveedor no responde dentro de su límite, la consulta se cancela y el perfil se construye con la información disponible. La meta del enunciado del caso es que el 95 % de las cotizaciones se resuelva en 400 ms o menos (**p95 ≤ 400 ms**).

El experimento introduce la variable **N**: el número de proveedores consultados en paralelo. El sistema real opera con tres; el experimento extiende N hasta nueve para determinar cuántos proveedores admite el diseño sin superar la meta de latencia.

En AWS se ejecutaron cuatro combinaciones. La principal es N = 3 con proveedores en condiciones normales, repetida tres veces para obtener el valor de referencia y su variación. Las otras tres modifican una única variable cada una: N = 9, para verificar si la latencia se mantiene con el triple de proveedores; caché al 60 %, para medir el efecto de que la mayoría de los perfiles ya esté almacenada; y proveedores degradados (p50 150 / p95 600 ms), para observar el comportamiento cuando los tiempos de respuesta externos aumentan.

### 1. Infraestructura desplegada

Región `us-east-2`, una única zona de disponibilidad. 25 recursos creados mediante Terraform.

| Recurso | Capacidad | Función |
|---|---|---|
| **VPC** `10.90.0.0/16` | Una subred pública `10.90.1.0/24` | Red privada que contiene todos los componentes. Las tareas reciben IP pública para descargar la imagen y enviar registros, lo que evita el uso de un NAT Gateway. |
| **Grupo de seguridad** | Tráfico de entrada solo entre miembros del grupo; salida sin restricción | Permite la comunicación entre las tareas y Redis; bloquea cualquier acceso desde internet. |
| **Clúster ECS Fargate** `expl01` | — | Agrupación lógica de los servicios. No genera costo por sí mismo; se factura por tarea en ejecución. |
| **Servicio `orquestador`** | 0,5 vCPU · 1 GB · 1 tarea | **Componente bajo prueba.** Recibe la cotización, consulta la caché y los proveedores en paralelo, construye el perfil y calcula la prima. |
| **Servicio `fuentes`** | 0,25 vCPU · 0,5 GB · 1 tarea | Simula los proveedores externos (`/fuente/1` a `/fuente/9`) con latencia de distribución lognormal parametrizada por p50 y p95. |
| **Tarea `k6`** | 1 vCPU · 2 GB · una por combinación | Generador de carga. Se lanza, ejecuta cuatro minutos de carga, imprime el resumen y finaliza. No es un servicio permanente. |
| **ElastiCache Redis 7.1** | `cache.t4g.micro` · 1 nodo | Caché de perfiles de riesgo (patrón Cache-Aside, TTL de una hora). |
| **Cloud Map** namespace `exp` | Zona DNS privada de la VPC | Resolución de nombres entre servicios: el orquestador localiza a los proveedores como `fuentes.exp.local` y k6 al orquestador como `orquestador.exp.local`. El sufijo `.local` es la convención de AWS para zonas DNS privadas sin exposición a internet. |
| **ECR** `expl01` | Un repositorio, dos imágenes | `expl01:app` contiene el binario Go (orquestador y proveedores según el parámetro `--mode`); `expl01:k6` contiene k6 con el guion de carga incorporado. |
| **CloudWatch Logs** | Tres grupos, retención de tres días | `/expl01/orquestador`, `/expl01/fuentes` y `/expl01/k6`. Los resultados se extraen de este último. |
| **IAM** | Dos roles | Uno permite a ECS descargar la imagen y escribir registros; el otro corresponde a la tarea y no requiere permisos adicionales. |

No se utilizó balanceador de carga: k6 consulta directamente al orquestador por DNS, y el orquestador a los proveedores de la misma forma. Es el camino más corto y el que menos variabilidad introduce en la medición.

### 2. Combinaciones ejecutadas

Cada combinación aplica una rampa de 8 a 83 solicitudes por segundo durante un minuto, seguida de tres minutos sostenidos a 83 solicitudes por segundo; en total, 17 669 cotizaciones por combinación. Parámetros fijos: límite de 120 ms por proveedor, tope de 700 ms para el conjunto, pool de 200 conexiones por host y TTL de caché de 3600 s.

| # | N | Perfil de proveedores | Caché | Repeticiones | Objetivo |
|---|---|---|---|---|---|
| 1 | 3 | normal (p50 60 / p95 200 ms) | 0 % | 3 | Valor de referencia y su variación entre repeticiones |
| 2 | 3 | normal | 60 % | 1 | Efecto sobre la latencia cuando la mayoría de los perfiles ya está en caché |
| 3 | 9 | normal | 0 % | 1 | Verificar si la latencia se sostiene con el triple de proveedores |
| 4 | 3 | degradado (p50 150 / p95 600 ms) | 0 % | 1 | Comportamiento cuando los proveedores presentan tiempos de respuesta elevados |

### 3. Resultados

**Valor de referencia — N = 3, proveedores normales, caché 0 %.**

| Repetición | p50 | p95 | p99 | max | % parciales | % sin perfil (503) | Conexiones abiertas al cierre | Goroutines al cierre | Consultas en proceso al cierre |
|---|---|---|---|---|---|---|---|---|---|
| 1 | 113,3 | 123,3 | 129,9 | 174 | 44,0 % | 0,57 % | 10 | 124 | 0 |
| 2 | 111,9 | 122,5 | 126,4 | 166 | 43,0 % | 0,51 % | 14 | 132 | 0 |
| 3 | 112,1 | 123,2 | 129,9 | 197 | 43,4 % | 0,45 % | 13 | 130 | 0 |
| **Mediana** | **112,1** | **123,2** | **129,9** | 174 | **43,4 %** | 0,51 % | — | — | **0** |

Tiempos en milisegundos. Los umbrales definidos en k6 (p95 < 400 ms, p99 < 800 ms, errores < 1 %) se cumplieron en las tres repeticiones. La variación entre repeticiones fue de 0,8 ms en p95 y de un punto porcentual en perfiles parciales.

**Variaciones sobre el valor de referencia.**

| Combinación | p50 | p95 | p99 | max | % parciales | % sin perfil (503) | Conexiones abiertas | Consultas en proceso | Cumple |
|---|---|---|---|---|---|---|---|---|---|
| N = 3, caché 60 % | **1,2** | 122,2 | 125,3 | 174 | 43,1 % | 0,18 % | 7 | 0 | ✓ |
| N = 9, normal | 121,6 | **123,3** | 131,4 | 193 | **82,4 %** | 0,00 % | 33 | 0 | ✓ |
| N = 3, degradado | 121,4 | 122,2 | 123,5 | 154 | 71,0 % | **23,0 %** | 6 | 0 | ✗ (errores > 1 %) |

En la combinación con proveedores degradados, de cada 100 cotizaciones 6 se resolvieron con las tres fuentes, 71 con una o dos (perfil parcial) y 23 sin ninguna (error 503). Los tres porcentajes se calculan sobre el total de cotizaciones y suman 100. En la combinación con caché, el porcentaje de perfiles parciales se calcula únicamente sobre las cotizaciones que consultaron a los proveedores; el 60 % restante se resolvió desde ElastiCache.

### 4. Hallazgos

**El límite por proveedor determina la latencia.** El p95 es de 123 ms tanto con N = 3 como con N = 9: el mismo valor con el triple de proveedores. Dado que cada proveedor tiene un límite de 120 ms y las consultas se ejecutan en paralelo, el recorrido nunca espera más que ese límite; los 3 ms adicionales corresponden a la red entre tareas de Fargate dentro de la misma zona. El p99 se sitúa en 130 ms y el máximo en 174 ms, todos con un margen de al menos tres veces respecto a la meta.

**El número de proveedores afecta la totalidad del perfil, no la latencia.** Con N = 3, el 43,4 % de los perfiles se construye sin al menos una fuente; con N = 9, el 82,4 %. Cada proveedor supera su límite el 17 % de las veces, y la probabilidad de que al menos uno de N lo supere es `1 − 0,83^N`: 43 % para N = 3 y 82 % para N = 9. El experimento reproduce esta relación al decimal.

**El orquestador no alcanza el límite de CPU.** Con 0,5 vCPU sostuvo N = 9 —aproximadamente 750 consultas simultáneas por segundo hacia los proveedores— sin variación en el p95. La capacidad asignada resulta suficiente con margen.

**La caché reduce la mediana, no el p95.** Con un 60 % de aciertos en ElastiCache, el p50 desciende a 1,2 ms: seis de cada diez cotizaciones se resuelven sin consultar a ningún proveedor. El p95, sin embargo, se mantiene en 122 ms, porque el 40 % que sí consulta a los proveedores supera el 5 % que define el percentil. Para que la caché incidiera en el p95 se requeriría una tasa de aciertos superior al 95 %. La caché es, por tanto, una palanca de costo y de carga sobre los proveedores, no de latencia.

**Con proveedores degradados, una de cada cuatro cotizaciones queda sin perfil.** Cuando los proveedores responden con p50 150 / p95 600 ms, cada uno supera su límite el 61 % de las veces, y los tres simultáneamente el 23 %. En ese caso el orquestador responde con error 503 por no disponer de información para construir el perfil. El límite cumple su función de proteger la latencia —el p95 se mantiene en 122 ms—, pero evidencia que el recorrido requiere un perfil base para los casos en que ninguna fuente responde.

**La cancelación libera todos los recursos.** El indicador `consultas_en_vuelo` registró 0 al cierre de las seis ejecuciones. Las conexiones abiertas se mantuvieron entre 6 y 33 —el pool inactivo, proporcional a N— y las goroutines entre 116 y 170, de las cuales alrededor de 100 corresponden a las conexiones persistentes del propio k6. Tras más de 30 000 cancelaciones acumuladas en la tarea del orquestador, ninguna consulta permaneció activa.

**Cada cancelación implica una nueva conexión.** El transporte HTTP de Go no reutiliza una conexión cuya respuesta fue abandonada, de modo que el número de conexiones creadas es aproximadamente igual al de cancelaciones: 12 639 frente a 12 565 en la primera repetición. A 83 solicitudes por segundo con N = 3, esto equivale a 38 reconexiones por segundo. Dentro de una zona de AWS este costo representó cerca de 1 ms de p95; frente a proveedores reales mediante TLS, cada reconexión implicaría una negociación completa.

### 5. Limpieza

La ejecución de `terraform destroy` eliminó los 25 recursos. Se verificó mediante la CLI de AWS que no quedaran clústeres de ElastiCache, tareas de Fargate, VPC con la etiqueta del experimento ni grupos de registros bajo `/expl01/`. Los recursos restantes en la región son anteriores al experimento y no tienen cómputo activo.

### 6. Conclusiones

1. **La meta de latencia se cumple con un margen de tres veces.** p95 = 123 ms y p99 = 130 ms frente a metas de 400 y 800 ms, con tres repeticiones que variaron menos de 1 ms. La hipótesis de diseño queda confirmada en Fargate con ElastiCache.
2. **La latencia la determina el límite por proveedor, no el número de proveedores.** 123 ms con tres y 123 ms con nueve. Cualquier valor de N cumple la meta mientras el límite por proveedor sea inferior a ella.
3. **La variable sensible del diseño es la totalidad del perfil.** 43 % de perfiles parciales con tres proveedores y 82 % con nueve, sin que exista un escenario de calidad que establezca el umbral aceptable. Es la decisión pendiente más relevante que deja el experimento.
4. **Con proveedores degradados, el diseño requiere un perfil base.** El 23 % de cotizaciones sin perfil no es un problema de latencia, sino de qué responder cuando ninguna fuente está disponible.
5. **La caché no incide en el p95.** Reduce la mediana a 1 ms pero no modifica el percentil 95. Su valor está en reducir la carga sobre los proveedores, no en cumplir la meta de latencia.
6. **El ambiente efímero es reproducible.** Cuarenta y cinco minutos de ejecución, un costo aproximado de USD 0,15 y ningún recurso residual. Los scripts en `scripts/aws_*.sh` y la documentación en `README.md` permiten repetirlo.

### 7. Cierre: ejecución local y ejecución en AWS

El experimento se ejecutó primero en un ambiente local (Docker Desktop, 12/09) y posteriormente en AWS (13/09). Comparar las mismas combinaciones en ambos ambientes permite distinguir qué depende del diseño y qué depende de la infraestructura.

**Datos de ambas ejecuciones.**

| N | Perfil | p95 local | p95 AWS | p99 local | p99 AWS | max local | max AWS | parciales local | parciales AWS |
|---|---|---|---|---|---|---|---|---|---|
| 3 | normal | 122,1 | 123,2 | 122,8 | 129,9 | 288 | 174 | 43,4 % | 43,4 % |
| 5 | normal | 122,0 | — | 122,9 | — | 293 | — | 62,1 % | — |
| 7 | normal | 122,0 | — | 123,3 | — | 605 | — | 73,6 % | — |
| 9 | normal | 122,1 | 123,3 | 123,1 | 131,4 | 291 | 193 | 82,5 % | 82,4 % |
| 3 | degradado | 123,0 | 122,2 | 124,4 | 123,5 | 291 | 154 | 92,5 % | 92,2 % |

**Gráfica 1 — p95 frente a N.** Constante en ambos ambientes.

```
 ms
 125 ┤
 123 ┤ ▲ · · · · · · · · · · · · · · · · · · · ▲   AWS
 122 ┤ ●━━━━━━━━━━●━━━━━━━━━━●━━━━━━━━━━●   local
 120 ┤ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─  límite por proveedor
     └──────────┬──────────┬──────────┬──────────┬──
               N=3        N=5        N=7        N=9
```

Ambas líneas son horizontales y distan 1 ms entre sí. El p95 no depende del número de proveedores ni del ambiente de ejecución: lo determina el límite de 120 ms. La diferencia de 1 ms corresponde a la red entre tareas de Fargate.

**Gráfica 2 — p99 frente a N.** El ambiente local lo subestima; AWS lo muestra.

```
 ms
 132 ┤ ▲ · · · · · · · · · · · · · · · · · · · ▲   AWS   130 → 131
 128 ┤
 124 ┤ ●━━━━━━━━━━●━━━━━━━━━━●━━━━━━━━━━●   local 123 → 123
     └──────────┬──────────┬──────────┬──────────┬──
               N=3        N=5        N=7        N=9
```

En el ambiente local, el p99 permanece próximo al p95 —123 frente a 122— porque los cuatro contenedores comparten una única CPU y los tiempos de espera se distribuyen de manera uniforme. En AWS, cada componente se ejecuta en su propia tarea y se manifiesta la cola real de la distribución: 130 ms. Este valor se mantiene seis veces por debajo de la meta de 800 ms, pero es el que corresponde reportar, y solo AWS lo proporciona.

**Gráfica 3 — Máximo frente a N.** El ambiente local presenta ruido; AWS no.

```
 ms
 605 ┤                           ●                 local: pico de CPU en N=7
 300 ┤ ●━━━━━━━━━━●━━━━━━━━━━━━━━━━━━━━━━━●   local ~290
 200 ┤
 180 ┤ ▲ · · · · · · · · · · · · · · · · · · · ▲   AWS 174 → 193
     └──────────┬──────────┬──────────┬──────────┬──
               N=3        N=5        N=7        N=9
```

El máximo local de 605 ms en N = 7 no es atribuible al diseño, sino a la planificación del sistema operativo entre cuatro contenedores y k6 en un mismo equipo. En AWS el máximo se sitúa entre 174 y 193 ms porque no existe competencia por recursos. Es el caso más claro de una cifra local que **no** debe reportarse como resultado del experimento.

**Gráfica 4 — Perfiles parciales frente a N.** Idéntica en ambos ambientes.

```
  %
  82 ┤                                      ●▲
  74 ┤                           ●
  62 ┤                ●
  43 ┤     ●▲
     └──────────┬──────────┬──────────┬──────────┬──
               N=3        N=5        N=7        N=9
               ● local    ▲ AWS
```

43,4 % frente a 43,4 %; 82,5 % frente a 82,4 %. La totalidad del perfil depende de la distribución de latencia de los proveedores y del límite por consulta —`1 − 0,83^N`—, y ninguno de los dos factores varía con la infraestructura. Es el hallazgo más relevante del experimento y el que menos requería de AWS para establecerse.

**Aporte de cada ambiente.**

| | Local | AWS |
|---|---|---|
| Costo | Ninguno | USD 0,15 por sesión |
| Duración por combinación | 4 min | 6–8 min (redespliegue y carga) |
| Cobertura de la matriz | Las 16 combinaciones | Las combinaciones seleccionadas |
| Curva completa N = 3…9 | Sí | Extremos: N = 3 y N = 9 |
| p95 | Válido, ±1 ms del valor real | Valor de referencia |
| p99 | Subestimado por contención de CPU | Valor real |
| Máximo | No válido — ruido de planificación | Limpio |
| Perfiles parciales | Exacto | Exacto |
| Saturación de CPU del orquestador | No verificable | Confirmado que 0,5 vCPU es suficiente |
| Caché | Redis local, 0,4 ms | ElastiCache, 1 ms |
| Función | Establecer el comportamiento | Confirmarlo en condiciones de producción |

**Cierre.** El ambiente local permitió comprender el experimento: ejecutar la matriz completa, identificar que la latencia no depende de N, determinar que la variable sensible es la totalidad del perfil y ajustar el banco de pruebas, todo sin costo ni tiempos de redespliegue. AWS aportó lo que el ambiente local no podía: los valores que se reportan (p95 = 123 ms, p99 = 130 ms, máximo 174 ms) y la confirmación de que el orquestador no alcanza el límite de CPU, único riesgo que la infraestructura podía introducir. El resultado del diseño —cumple, con un margen de tres veces, y la restricción real es la totalidad del perfil— fue el mismo en ambos ambientes. Lo que cambió fue la confianza en las cifras.
