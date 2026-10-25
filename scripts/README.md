# `scripts`

Utilidades de desarrollo y CI. Cada script debe documentarse aquí al agregarse.

| Script | Uso |
|---|---|
| [`ci/changed-images.sh`](ci/changed-images.sh) | `changed-images.sh <sha-base> <sha-head>`: imprime como arreglo JSON las imágenes que `backend-cd` debe construir. Reconstruye la imagen cuyo directorio cambió y **todas** si cambió algo compartido (`pkg/`, `go.work`, `.dockerignore`, el CD) o si no hay base utilizable. Requiere `git` y `jq` |
| [`ci/environment-status.sh`](ci/environment-status.sh) | `environment-status.sh <cluster>`: imprime `up` si el cluster existe y todos sus servicios tienen tareas, o `off: <motivo>`. `backend-cd` solo despliega con `up`. Requiere `aws` |
| [`ci/staging-vars.sh`](ci/staging-vars.sh) | `staging-vars.sh <cluster>`: imprime como archivo de variables JSON de Terraform el `ttl` y los `extra_tags` con los que `up.sh` encendió `staging`, leídos de las etiquetas del cluster. Requiere `aws` y `jq` |
| [`ci/check-deploy-plan.sh`](ci/check-deploy-plan.sh) | `terraform show -json tfplan \| check-deploy-plan.sh`: falla si el plan cambia algo que no sea una *task definition* o un servicio de ECS, o si un servicio pasa de 0 tareas a alguna. Requiere `jq` |
| [`ci/verify-ecs-deploy.sh`](ci/verify-ecs-deploy.sh) | `verify-ecs-deploy.sh <cluster> <etiqueta>`: espera a que los servicios estén estables y falla si alguno no corre la etiqueta nueva (por ejemplo, tras un *rollback* del *circuit breaker*). Requiere `aws` |
| [`environment/up.sh`](environment/up.sh) | `up.sh <staging\|production-demo> [--only <fases>] [--from <fase>] [--skip <fases>] [--skip-front] [--dry-run] [--yes] [--confirm-production-demo]`: enciende un ambiente completo en orden (plataforma, base de datos, portal web, servicios, front y humo). Ver «Encender y apagar un ambiente» |
| [`environment/down.sh`](environment/down.sh) | `down.sh <staging\|production-demo> [--dry-run] [--yes] [--confirm-production-demo]`: apaga un ambiente; staging entero, production-demo entero salvo el sitio y el pool permanente |
| [`environment/lib.sh`](environment/lib.sh) | Funciones comunes de `up.sh` y `down.sh` (argumentos, guardas, etiquetas de imagen, Terraform); no se ejecuta solo |

## Encender y apagar un ambiente (T5)

Un comando enciende y otro apaga cada ambiente (decisiones D-16 y D-18 de T5, ajustadas con #68 y #69). Viven aquí y no en
`scripts/ci/` porque no son del CD: cambiarlos no reconstruye imágenes (solo `scripts/ci/**` cuenta como cambio compartido).

### Fases de `up.sh`

| Fase | staging | production-demo |
|---|---|---|
| `preflight` | Herramientas (`terraform` ≥ 1.11, `aws`, `git`, `curl`; `gh` si toca `front`), cuenta `368083581678`, estado de `bootstrap/identity` (pool permanente) y que `staging/platform` y `production-demo/platform` lo lean (P1). Si falta, se detiene con «falta P1» (con `--dry-run`, solo avisa) | Igual; `node` ≥ 24 y `npm` si `front` va a construir el front |
| `images` | Por cada una de las 10 imágenes, la etiqueta más reciente de 12 caracteres hexadecimales cuyo commit está en `origin/develop` (nunca `prueba-*`); se pasan las 10 en `image_tag_overrides`, así ninguna queda en «la más reciente» | La etiqueta del parámetro SSM **`/solventa/production-demo/image-tag`**, que escribe el CD de `main` (pipeline de Maycol) con el SHA del *merge* tras construir todas las imágenes. T5 solo lo lee. Valor inicial `none` (SSM no admite vacío): con `none`, vacío o sin parámetro se detiene con «falta correr el CD de main»; con algo que no sea un SHA de 12 caracteres hexadecimales, error. Nunca busca «la más reciente» |
| `platform-base` | `staging/platform` con `services_enabled=false`, `ttl` y `extra_tags`; se detiene si el plan destruye algo | `production-demo/platform` con `image_tag` y `services_enabled=false` |
| `db-init` | Corre el comando literal de la salida `db_init_command`, espera `tasks-stopped` y exige código 0 y «database <svc> ready» ×5 en el log | Igual |
| `site` | `staging/web` (bucket, CloudFront, `config.json`, SSM) con `ttl`, `extra_tags` y `cd_branches = develop + CD_EXTRA_BRANCHES`; imprime la URL | `production-demo/web` |
| `platform` | La plataforma con `services_enabled=true` (la URL del portal llega sola por SSM), espera los 10 servicios ECS estables y comprueba que originación ya enlaza al portal | Igual, con `image_tag` |
| `front` | `gh workflow run cd-portal.yml --ref ${FRONT_REF:-develop} -f ambiente=staging` y `gh run watch`. Solo existe después de integrar el PR del front | Con `FRONT_DIR` (*checkout* limpio del front en `origin/main`): `npm ci`, *build* de producción y `scripts/publicar-portal.sh production-demo` con las credenciales de quien corre el comando. Sin `FRONT_DIR` o con `--skip-front`, se omite y avisa |
| `smoke` | [`tests/e2e/portal-web/smoke.sh`](../tests/e2e/portal-web/README.md) (fases de `SMOKE_PHASES`, por defecto todas) | Igual |

- **Sin `--yes`**, corre las fases hasta la primera de Terraform, la planifica, guarda el plan en `STATE_DIR` y se detiene para revisarlo.
  `db-init` y `front` también piden `--yes`. **Con `--yes`** aplica el plan guardado de cada fase (o uno nuevo), lo borra apenas lo aplica
  (lleva la key del socio, sensible) y sigue. Las etiquetas resueltas en la corrida sin `--yes` se reutilizan, así se aplica lo revisado.
- **`--dry-run`** solo lee: STS, ECR, SSM, `git fetch` y `terraform plan -lock=false` (sin guardar planes); omite `db-init`, `front` y
  `smoke`. Si la plataforma no existe todavía, planifica el sitio con `api_enabled=false`.
- `--only`, `--from`, `--skip` y `--skip-front` eligen fases. Al final imprime la URL, los usuarios (solo correos), el costo por hora y el
  comando de apagado.
- **Variables que pasan los comandos en production-demo (#69):** solo `image_tag` y `services_enabled` por fase, y en `down.sh` además
  `deletion_protection=false` y `ephemeral=true`. **Nunca** `nat_mode`, `rds_multi_az` ni `redis_multi_az`: quedan los valores por defecto
  de la composición, los mismos que usa el CD de `main`. Ninguna variable de identidad: las composiciones leen siempre el pool permanente.

### `down.sh`

- **staging:** `destroy` de `staging/platform` (primero, porque es lo que cobra por hora) y luego de `staging/web`; exige `state list`
  vacío en las dos y lee lo que podría quedar (RDS, *snapshots*, copias automáticas, secretos `solventa-stg/*` incluso en borrado
  programado, ALB, buckets, VPC `10.50.0.0/16`, distribución, parámetros `/solventa/staging/web`, rol del CD). El pool permanente no es
  de ninguna de las dos composiciones y queda.
- **production-demo (D-18):** toma la etiqueta del parámetro SSM (o, si dice `none`, la que corre la plataforma, para no cambiar las
  definiciones de tarea), aplica `deletion_protection=false`, `ephemeral=true` y `services_enabled=false` (candados abajo; secretos sin
  ventana de recuperación, buckets con sus objetos, RDS sin *snapshot* final), destruye `production-demo/platform` con las mismas variables
  y verifica que no quede nada. **Conserva** `production-demo/web` (≈ US$ 0/h) y `bootstrap/identity`. Volver a encender antes de 7 días
  no choca con los nombres de los secretos; puede haber unos segundos de consistencia eventual (si `platform-base` falla por eso, se
  repite la fase).

### Variables de entorno

| Variable | Para qué |
|---|---|
| `STATE_DIR` | Planes y etiquetas entre llamadas (por defecto `${TMPDIR:-/tmp}/solventa-environment-<ambiente>`, privado) |
| `TTL` | staging: etiqueta `TTL` (`AAAA-MM-DD`, por defecto hoy; fíjela si la ventana cruza la medianoche) |
| `EXTRA_TAGS` | staging: etiquetas extra, p. ej. `'{Owner="nicolas",Story="T5"}'` |
| `CD_EXTRA_BRANCHES` | staging: ramas que se suman a `develop` en el rol del CD (lista con comas; solo durante una ventana) |
| `FRONT_REF` | staging: rama del despacho de `cd-portal.yml` (por defecto `develop`) |
| `FRONT_DIR` | production-demo: *checkout* limpio del front en `origin/main` para construir y publicar |
| `SEED_USER_PASSWORD` | Contraseña de los usuarios permanentes para el humo (por defecto `SOLVENTA_IDENTITY_SEED_PASSWORD`); nunca se imprime |
| `SMOKE_PHASES` | Fases del humo (por defecto todas) |

### production-demo: encender y apagar (runbook)

**El harness de agentes nunca lo corre** (R8): lo corre una persona desde su terminal, con credenciales de la cuenta `368083581678`
(también para subir al bucket del sitio e invalidar CloudFront: production-demo no tiene rol de CD, #68).

```
Prerrequisitos (una vez): bootstrap/identity aplicado (pool permanente) · develop → main integrado en backend y front ·
  el CD de main terminó: imágenes publicadas y /solventa/production-demo/image-tag con el SHA (no "none") ·
  un checkout limpio del front en origin/main (FRONT_DIR), con Node 24 y npm, si se va a publicar el front con el comando
Encender (unas dos horas antes de la demo; la primera vez, con más margen):
  FRONT_DIR=<checkout del front> scripts/environment/up.sh production-demo --confirm-production-demo          (planifica y se detiene)
  FRONT_DIR=<checkout del front> scripts/environment/up.sh production-demo --confirm-production-demo --yes    (aplica)
      etiqueta del parámetro SSM → plataforma sin tareas → db-init → sitio → plataforma con tareas y la URL del portal →
      build local de main + publicar-portal.sh → humo
  (con --skip-front, el front lo publica el pipeline de main; el humo se corre después de que publique)
Apagar (al terminar la demo, el mismo día):
  scripts/environment/down.sh production-demo --confirm-production-demo --yes
```

**Coordinación con el CD de `main` (pipeline de Maycol, #68 y #69):** ese pipeline construye y publica las imágenes y el parámetro
de la etiqueta **siempre**; solo aplica Terraform si el ambiente está encendido; y su aprobación **no se da mientras corre `up.sh` o
`down.sh`** (los dos aplicarían el mismo estado: el bloqueo nativo del estado remoto impide dos `apply` a la vez, pero el segundo fallaría
a medias). Si ese pipeline aplica production-demo o publica el front, respeta el orden `site` → `platform` y el apagado de D-18.

Costo: production-demo ≈ US$ 0,33/h encendida (sin NAT ni Multi-AZ); staging ≈ US$ 0,26/h con los servicios y ≈ US$ 0,08/h con las
tareas en 0. El sitio (CloudFront y S3) ≈ US$ 0/h.

### Verificación local

```bash
bash -n scripts/environment/up.sh scripts/environment/down.sh scripts/environment/lib.sh
bash scripts/environment/up.sh --help
bash scripts/environment/up.sh staging --dry-run --only preflight,images      # solo lecturas
```
