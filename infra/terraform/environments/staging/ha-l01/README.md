# Composición efímera `staging/ha-l01` — ventana de medición de HA-L01

Levanta, solo durante una ventana de medición, lo necesario para evidenciar en AWS los criterios que no se pueden medir en
local: **CA-1** (p95 ≤ 400 ms y p99 ≤ 800 ms bajo la rampa de EXP-L01), **CA-4** (caché del gateway con aislamiento por socio)
y **CA-5** (rate limiting por socio). Se aplica y se destruye **en la misma sesión**.

> **Estado:** código validado (`terraform fmt`, `validate` y `plan` de solo lectura: 62 recursos). **Nunca se ha aplicado.**
> Aplicarlo exige la autorización de la ventana (estimación de costo escrita y aprobada) y destruirlo en la misma sesión.

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
- Estado local y efímero (no hay estado remoto del equipo todavía). El `.terraform.lock.hcl` fija el proveedor `hashicorp/aws`.

## Variables

| Variable | Defecto | Descripción |
|---|---|---|
| `ttl` | — (obligatoria) | Fecha `AAAA-MM-DD` límite; etiqueta `TTL` |
| `services_enabled` | `false` | Corre las tareas `origination` y `fuentes`. Primero `false` (crea los repositorios ECR), se suben las imágenes y luego `true` |
| `k6_enabled` | `false` | Corre la tarea k6 (con `services_enabled = true`) |
| `open_finance_required` | `true` | `OPEN_FINANCE_REQUIRED` del servicio (D-2) |
| `source_latency_profile` | `normal` | Perfil de Toxiproxy: `normal` (85 ± 55 ms) o `degradado` (150 ± 120 ms) |
| `cache_ttl_seconds` | `60` | TTL del `GET` cacheado (D-6) |
| `partners` | `socio-a`, `socio-b` (100 req/s, ráfaga 200) | Socios de prueba (D-7) |
| `image_tag` | `ha-l01` | Etiqueta de las imágenes en ECR |
| `name`, `vpc_cidr`, `region` | `solventa-stg-ha-l01`, `10.40.0.0/16`, `us-east-2` | Nombres, red y región |

Salidas: `invoke_url`, `api_key_ids`, `api_key_values` (sensible), `ecr_repositories`, `image_tag`, `ecs_cluster`,
`log_group`, `internal_alb_dns`.

## Procedimiento de la ventana (bloque C del plan de HA-L01; no ejecutado)

Requisitos previos: estimación de costo escrita y autorizada, credenciales de la cuenta del proyecto con MFA, y decisión sobre
cómo llegan las imágenes a ECR (`docker push` desde la máquina o el pipeline de T5).

1. `terraform init` y `terraform apply -var ttl=<fecha>` (con `services_enabled = false`): red, ALB, VPC link, API Gateway y ECR.
2. Construir y subir a cada repositorio de `ecr_repositories` las imágenes `origination`, `openfinance`, `opendata`, `kyc`
   (Dockerfiles del repositorio, contexto en la raíz) y `k6` (`tests/load-k6/ha-l01/Dockerfile`), con la etiqueta `image_tag`.
3. `terraform apply -var ttl=<fecha> -var services_enabled=true`; esperar a que el target group del ALB quede sano.
4. `terraform apply … -var services_enabled=true -var k6_enabled=true`: la tarea k6 corre la secuencia
   (`peak.js` ×3 con `STRICT_LATENCY=1`, `gateway-cache.js`, `rate-limit.js`) y deja los resúmenes en `log_group`.
5. Leer los resúmenes y las métricas `CacheHitCount` / `CacheMissCount` / `4XXError` del stage (solo lectura).
6. `terraform destroy -var ttl=<fecha>` en la misma sesión y comprobar `terraform state list` vacío.

Costo estimado de la ventana (plan de HA-L01 §4.3): ≈ US$ 0,66 + 30 % de margen = **US$ 0,86** por ~2 h; lo domina el número de
peticiones al gateway. Encendida por olvido cuesta ≈ US$ 0,21/h (caché del stage, ALB, Fargate e IPv4 públicas).
