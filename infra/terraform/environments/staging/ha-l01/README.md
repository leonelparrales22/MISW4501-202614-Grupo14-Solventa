# Composición efímera `staging/ha-l01` — ventana de medición de HA-L01

Levanta, solo durante una ventana de medición, lo necesario para evidenciar en AWS los criterios que no se pueden medir en
local: **CA-1** (p95 ≤ 400 ms y p99 ≤ 800 ms bajo la rampa de EXP-L01), **CA-4** (caché del gateway con aislamiento por socio)
y **CA-5** (rate limiting por socio). Se aplica y se destruye **en la misma sesión**.

> **Estado:** **aplicada y destruida el 2026-10-11** (fase sin costo 15:45–15:48, fase que cobra 15:51–16:20, `us-east-2`, 62
> recursos; estado remoto en el bucket de T4). Resultados en
> [`tests/load-k6/ha-l01/resultados/2026-10-11-staging.md`](../../../../../tests/load-k6/ha-l01/resultados/2026-10-11-staging.md).
> Cualquier ventana nueva exige otra vez la autorización (estimación de costo escrita y aprobada) y destruirla en la misma sesión.

## Topología (opción A de la decisión D-8, alineada con ADR-02 sin NAT)

```
tarea k6 ──(internet)──▶ API Gateway REST (caché 0,5 GB + usage plans por socio)
   ──VPC Link V2──▶ ALB interno (health check de ADR-02) ──▶ tarea origination (+ Redis como contenedor)
   ──Cloud Map (fuentes.ha-l01.local)──▶ tarea fuentes: stubs Open Finance, Open Data, KYC detrás de Toxiproxy
```

- VPC `10.40.0.0/16` con dos subredes públicas (el ALB exige dos AZ; las tareas corren en la primera), Internet Gateway y
  **sin NAT Gateway**: las tareas tienen IP pública solo para descargar imágenes.
- Fargate: `origination` 0,5 vCPU / 1 GB (EXP-L01), `fuentes` 0,5 vCPU / 1 GB, `k6` 1 vCPU / 2 GB. Una tarea de cada una.
- Toxiproxy aplica el perfil de latencia de EXP-L01 elegido en `source_latency_profile` (`normal` o `degradado`).
- ElastiCache no se usa: la caché de perfil no mueve el p95 (EXP-L01 §4); Redis va como contenedor junto a `origination`.
- Logs en CloudWatch con retención de 1 día. Etiquetas por defecto: `Project=solventa`, `Owner=nicolas`, `Env=staging`,
  `Story=HA-L01`, `TTL=<var.ttl>`. Región fija `us-east-2` (validada).
- **Estado remoto** en el bucket del proyecto `solventa-tfstate-368083581678` (tarea T4), clave `staging/ha-l01/terraform.tfstate`,
  con bloqueo nativo de S3 (`use_lockfile`, Terraform ≥ 1.11) y cifrado. **No queda estado en disco** (solo la configuración del
  backend en `.terraform/`, ignorada por git), así que la ventana se puede destruir desde cualquier terminal si la sesión se corta.
  Las versiones anteriores del objeto de estado (contienen las API keys de prueba, inútiles tras el destroy) las borra a los 30
  días la regla de ciclo de vida del bucket. El `.terraform.lock.hcl` fija `hashicorp/aws` 6.68.0.

## Variables

| Variable | Defecto | Descripción |
|---|---|---|
| `ttl` | — (obligatoria) | Fecha `AAAA-MM-DD` límite; etiqueta `TTL` |
| `services_enabled` | `false` | Corre las tareas `origination` y `fuentes`. Primero `false` (crea los repositorios ECR), se suben las imágenes y luego `true` |
| `k6_enabled` | `false` | Corre la tarea k6 (con `services_enabled = true`) |
| `k6_sequence` | `"peak cache ratelimit"` | Pasos de la tarea k6 después de su compuerta, en orden: `peak` (CA-1), `cache` (CA-4), `ratelimit` (CA-5), separados por espacios; permite repetir un paso suelto |
| `k6_peak_runs` | `3` | Corridas de `peak.js` en el paso `peak` (1 a 3) |
| `open_finance_required` | `true` | `OPEN_FINANCE_REQUIRED` del servicio (D-2) |
| `source_latency_profile` | `normal` | Perfil de Toxiproxy: `normal` (85 ± 55 ms) o `degradado` (150 ± 120 ms) |
| `cache_ttl_seconds` | `60` | TTL del `GET` cacheado (D-6) |
| `partners` | `socio-a`, `socio-b` (100 req/s, ráfaga 200) | Socios de prueba (D-7) |
| `image_tag` | `ha-l01` | Etiqueta de las imágenes en ECR |
| `name`, `vpc_cidr`, `region` | `solventa-stg-ha-l01`, `10.40.0.0/16`, `us-east-2` | Nombres, red y región |

Salidas: `invoke_url`, `api_key_ids`, `api_key_values` (sensible), `ecr_repositories`, `image_tag`, `ecs_cluster`,
`log_group`, `internal_alb_dns`.

## Procedimiento de la ventana (no ejecutado)

Requisitos previos: estimación de costo escrita y autorizada; credenciales de la cuenta del proyecto (MFA opcional); el bucket
del estado remoto (T4); las cinco imágenes construidas desde el commit final con el árbol limpio y probadas en local
(`tests/load-k6/ha-l01/run.sh` y la imagen k6 contra el compose).

**Secretos.** Las API keys de prueba de los socios quedan en el estado (remoto) y en cualquier plan guardado:

- Los planes guardados (`-out`) van **fuera del repositorio** (un directorio temporal): `.gitignore` no ignora `*.tfplan`. Se
  borran después del `apply`.
- Nunca `terraform output -json`, `terraform show -json`, `terraform output api_key_values`,
  `aws apigateway get-api-key(s) --include-value(s)`, `aws ecs describe-task-definition` de la tarea k6 (lleva las claves en su
  entorno), `TF_LOG` ni `set -x`.

**Fases** (`D=infra/terraform/environments/staging/ha-l01`, `TAG=$(git rev-parse --short=12 HEAD)`, `TTL=<fecha>`, `<tmp>` = un
directorio temporal fuera del repositorio). Primero todo lo que no cobra por hora; el ALB y la caché del stage se encienden solo
cuando las imágenes ya están en ECR.

0. `terraform -chdir=$D init -input=false` (backend S3) y `terraform -chdir=$D state list` → vacío.
1. **P0 (no cobra por hora):** `terraform -chdir=$D plan -input=false -var ttl=$TTL -var image_tag=$TAG -out=<tmp>/p0.tfplan` con
   `-target=aws_route_table_association.public -target=aws_apigatewayv2_vpc_link.this -target=aws_ecr_repository.this
   -target=aws_iam_role_policy_attachment.execution -target=aws_cloudwatch_log_group.this -target=aws_ecs_cluster.this
   -target=aws_service_discovery_service.sources -target=aws_ecs_task_definition.sources` (≈ 21 recursos: red, VPC Link V2, cinco
   repositorios ECR, rol de ejecución, log group, clúster, Cloud Map con su zona privada y la definición de tarea `sources`; ni
   ALB, ni API Gateway, ni servicios ECS) → `terraform -chdir=$D apply -input=false <tmp>/p0.tfplan`. Comprobar el VPC Link
   `AVAILABLE` (`aws apigatewayv2 get-vpc-links`) y los cinco repositorios.
2. **Imágenes:** `aws ecr get-login-password --region us-east-2 | docker login --username AWS --password-stdin <registro>` (el token
   no se imprime); `docker tag` y `docker push` de `origination`, `openfinance`, `opendata`, `kyc` (Dockerfiles del repositorio,
   contexto en la raíz) y `k6` (`tests/load-k6/ha-l01/Dockerfile`) a `<registro>/solventa-stg-ha-l01/<imagen>:$TAG`; comprobar con
   `aws ecr describe-images`; `docker logout <registro>`.
3. **P1 (empieza a cobrar):** `plan … -var services_enabled=true -var k6_enabled=false -out=<tmp>/p1.tfplan` (solo recursos a crear)
   → `apply <tmp>/p1.tfplan`. Esperar `aws ecs wait services-stable --cluster solventa-stg-ha-l01 --services origination sources`,
   el target del ALB `healthy` (`aws elbv2 describe-target-health`) y la caché del stage `AVAILABLE` (`aws apigateway get-stage`);
   revisar en los logs el arranque de `origination` (configuración del circuito) y que `toxiproxy-init` terminó con código 0.
4. **P2 (k6):** `plan`/`apply` con `-var services_enabled=true -var k6_enabled=true`. La tarea k6 primero comprueba el recorrido
   (`k6/ready.js`, hasta 10 intentos cada 30 s) y, solo si pasa, corre los pasos de `k6_sequence` (`peak.js` ×`k6_peak_runs` con
   `STRICT_LATENCY=1`, `gateway-cache.js`, `rate-limit.js`), con marcas de hora UTC y código de salida por corrida
   ([secuencia](../../../../../tests/load-k6/ha-l01/README.md)); se sigue con
   `MSYS_NO_PATHCONV=1 aws logs tail /ecs/solventa-stg-ha-l01 --log-stream-name-prefix ha-l01/k6 --since 1h --format short` hasta
   `== HA-L01 staging sequence finished` o `== HA-L01 NOT READY: no load sent` (el recorrido está roto y no se envió carga). Al
   terminar, `apply` con `-var k6_enabled=false` (la tarea k6 deja de cobrar). Para repetir un solo paso: `k6_enabled=false`,
   luego `-var k6_sequence=<paso> -var k6_peak_runs=1 -var k6_enabled=true`.
5. **Evidencia antes del destroy:** el destroy borra el log group, así que antes se guardan fuera del repositorio los logs de k6 y de
   los servicios y las métricas del stage (`aws cloudwatch get-metric-statistics --namespace AWS/ApiGateway --dimensions
   Name=ApiName,Value=solventa-stg-ha-l01 Name=Stage,Value=staging`: `Count`, `Latency`, `IntegrationLatency`, `CacheHitCount`,
   `CacheMissCount`, `4XXError`, `5XXError`). Las métricas de CloudWatch persisten después del destroy.
6. **Destroy en la misma sesión:** `terraform -chdir=$D state list` (no vacío: confirma que se apunta al estado correcto) →
   `terraform -chdir=$D destroy -auto-approve -input=false -var ttl=$TTL -var image_tag=$TAG` (siempre con `-chdir`) →
   `terraform -chdir=$D state list` **vacío**. La caché del stage tarda unos 4 minutos en borrarse y el VPC Link en liberar sus ENI.
7. **Comprobación de limpieza (solo lectura):** `aws resourcegroupstaggingapi get-resources --tag-filters Key=Story,Values=HA-L01`
   → vacío salvo las definiciones de tarea `solventa-stg-ha-l01-*`, que quedan `INACTIVE` y no cobran (con `Key=Owner,Values=nicolas`
   solo aparece además el bucket del estado, permanente); sin VPC con `Story=HA-L01`, sin API REST `solventa-stg-*`, sin VPC Link
   `solventa-stg-ha-l01`, sin ALB `stg-ha-l01-internal`, clúster `INACTIVE` o ausente, sin repositorios `solventa-stg-ha-l01/*`, sin
   namespace ni zona `ha-l01.local` (la zona privada de Route 53 cobra el mes completo si vive más de 12 h) y sin log group
   `/ecs/solventa-stg-ha-l01`. Borrar los planes de `<tmp>` y, del Docker local, las etiquetas `<registro>/…`. Con el estado
   remoto no hay estado local que borrar.

Si la sesión se corta con la ventana arriba, se destruye desde cualquier terminal con credenciales del proyecto: `init` y el
`destroy` del paso 6 (el estado está en el bucket).

Validación local (sin AWS): `terraform -chdir=$D init -backend=false` y `terraform -chdir=$D validate`. Con credenciales de solo
lectura también se puede revisar el plan sin escribir nada en el bucket: `terraform -chdir=$D init -input=false` y
`terraform -chdir=$D plan -lock=false -var ttl=<fecha> …` (no aplica nada; un `plan` no escribe estado y `-lock=false` evita el
archivo de bloqueo).

**Costo** (plan de HA-L01, fase de staging): ≈ US$ 0,69 con la fase que cobra a su tope de 120 min (ALB y caché del stage cobran por
hora iniciada) + 30 % de margen = **US$ 0,90**; lo típico es ≈ US$ 0,55. Lo dominan las peticiones al gateway (≈ 86 000). Encendida
por olvido: ≈ US$ 0,156/h con todo arriba (≈ US$ 3,75 al día) o ≈ US$ 0,0425/h solo con el ALB y la caché (tareas en cero), más
US$ 0,50 si la zona de Route 53 pasa de 12 h. Destruir siempre en la misma sesión.
