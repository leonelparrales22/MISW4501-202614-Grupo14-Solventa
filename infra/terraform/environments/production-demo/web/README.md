# Composición `production-demo/web` — portal web de la entrega evaluable (T5)

Portal web de production-demo con el módulo [`web-portal`](../../../modules/web-portal/README.md): bucket S3 privado, distribución
de CloudFront con el API Gateway de [`production-demo/platform`](../platform/README.md) en el mismo dominio y `config.json`.

> **Estado:** código validado con `terraform validate`, `terraform test` (proveedor simulado) y un `plan` de solo lectura
> (`-var api_enabled=false`: 13 por crear, ningún rol IAM). **No se ha aplicado en AWS**; el harness de agentes nunca lo aplica (R8).
> Lo crea la fase `site` de `scripts/environment/up.sh production-demo`, que corre una persona desde su terminal.

## Qué crea

| Recurso | Valor |
|---|---|
| Bucket del sitio | `solventa-demo-web-368083581678` (`force_destroy = false`) |
| Distribución | `d<id>.cloudfront.net`; `/v1/*` y `/pasarela/*` al stage `production-demo` del API, rutas de socio con la key de `portal-embebido` |
| `config.json` | URL del portal, URL de la pasarela, pool permanente de `bootstrap/identity` y su cliente `web` |
| SSM | `/solventa/production-demo/web/{bucket,distribution-id,url}`; `production-demo/platform` lee la `url` para los enlaces de los correos y para fijar CORS |
| Rol del CD | **Ninguno** (`cd = null`) |

Estado remoto: `production-demo/web/terraform.tfstate` en `solventa-tfstate-368083581678`. Etiquetas: `Project=solventa`,
`Env=production-demo`, `Task=T5`.

## Permanente (D-13)

El sitio cuesta ≈ US$ 0 por hora, así que **queda encendido**: la URL de la demo no cambia entre encendidos y `down.sh
production-demo` no lo toca (solo destruye la plataforma, D-18). Si la plataforma está apagada, el portal carga pero sus llamadas al
API fallan; `config.json` sigue apuntando al mismo dominio.

## Publicación del front

**La publicación continua desde `main` es del pipeline de `main` de Maycol (#68).** Si la necesita, puede activar la entrada `cd` del
módulo con `branches = ["main"]` (rol OIDC), o reutilizar `scripts/publicar-portal.sh production-demo` del repo del front con sus
propias credenciales; T5 no lo implementa.

Hoy publica la fase `front` de `up.sh production-demo`: con `FRONT_DIR` (un *checkout* limpio del front en `origin/main`) construye
`main` localmente (`npm ci` y *build* de producción) y corre `publicar-portal.sh production-demo` con las credenciales de quien ejecuta
el comando. Sin `FRONT_DIR`, o con `--skip-front`, la fase se omite y lo avisa. El *script* nunca toca `config.json`.

## Variables

| Variable | Defecto | Descripción |
|---|---|---|
| `api_enabled` | `true` | Lee `production-demo/platform` por estado remoto y publica `config.json`. `false` deja solo el sitio (un `plan` sin plataforma) |

Con `api_enabled = true`, si la plataforma no tiene las salidas `api_origin`, `cognito` y `portal_partner_api_key`, el `plan` se
detiene con un mensaje claro (*postcondition*).

## Procedimiento

Lo hace `scripts/environment/up.sh production-demo --confirm-production-demo` (ver el runbook en el README de
[`production-demo/platform`](../platform/README.md#encender-y-apagar-con-un-comando-t5) y en
[`scripts/README.md`](../../../../../scripts/README.md)). A mano:

```bash
export AWS_PROFILE=solventa          # cuenta 368083581678
cd infra/terraform/environments/production-demo/web
terraform init
terraform plan -out site.tfplan
terraform apply site.tfplan && rm site.tfplan     # el plan lleva la key del socio (sensible): se borra al aplicar
terraform output url
```

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform validate
terraform test
```

`tests/production-demo-web.tftest.hcl` sustituye el estado remoto de la plataforma y comprueba que el sitio toma el stage y el pool
permanente, que no existe rol de CD (`no_cd_role_in_production_demo`), que sin API no lee la plataforma y que una plataforma sin las
salidas de T5 falla con un mensaje claro.
