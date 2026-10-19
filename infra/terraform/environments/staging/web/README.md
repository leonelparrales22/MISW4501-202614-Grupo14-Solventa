# Composición `staging/web` — portal web de staging (T5)

Portal web de staging con el módulo [`web-portal`](../../../modules/web-portal/README.md): bucket S3 privado, distribución de
CloudFront con el API Gateway de [`staging/platform`](../platform/README.md) en el mismo dominio, `config.json` y el rol OIDC con el
que `cd-portal.yml` (repo del front) publica. Como el resto de staging, **se enciende para una ventana y se destruye en la misma
sesión**.

> **Estado:** código validado con `terraform validate`, `terraform test` (proveedor simulado) y un `plan` de solo lectura
> (`-var api_enabled=false`: 15 por crear). No se ha aplicado en AWS; lo aplica la ventana de staging de T5 con
> `scripts/environment/up.sh staging`.

## Qué crea

| Recurso | Valor |
|---|---|
| Bucket del sitio | `solventa-stg-web-368083581678` (`force_destroy = true`: se borra con sus objetos) |
| Distribución | `d<id>.cloudfront.net`; `/v1/*` y `/pasarela/*` al stage `staging` del API, rutas de socio con la key de `portal-embebido` |
| `config.json` | URL del portal, URL de la pasarela (`<portal>/pasarela`), pool permanente de `bootstrap/identity` y su cliente `web` |
| SSM | `/solventa/staging/web/{bucket,distribution-id,url}`; `staging/platform` lee la `url` para los enlaces de los correos |
| Rol del CD | `solventa-github-actions-web-stg`: confía en `develop` del repo del front (variable `cd_branches`) |

Estado remoto: `staging/web/terraform.tfstate` en `solventa-tfstate-368083581678`. Etiquetas: `Project=solventa`, `Env=staging`,
`Task=T5`, `TTL=<var.ttl>` y las de `extra_tags` (`Owner`, `Story`).

## Variables

| Variable | Defecto | Descripción |
|---|---|---|
| `ttl` | — (obligatoria) | Fecha `AAAA-MM-DD` límite; etiqueta `TTL` |
| `api_enabled` | `true` | Lee `staging/platform` por estado remoto y publica `config.json`. `false` deja solo el sitio (un `plan` sin plataforma, o la prueba del despacho del workflow) |
| `cd_branches` | `["develop"]` | Ramas en las que confía el rol. Una ventana agrega la de su historia para el disparador temporal (D-9 b de T5); `main` está prohibida (#68) y el rol se destruye con la ventana |
| `extra_tags` | `{}` | Etiquetas adicionales; no reemplazan las cuatro por defecto |

Con `api_enabled = true`, si `staging/platform` no tiene las salidas `api_origin`, `cognito` y `portal_partner_api_key` (la
plataforma no está aplicada o es anterior a T5), el `plan` se detiene con un mensaje claro (*postcondition*).

## Encender y apagar

Lo hace `scripts/environment/up.sh staging` (fase `site`, después de `platform-base` y `db-init` y antes de `platform`, para que
originación arranque ya con la URL del portal) y `down.sh staging` (después de destruir la plataforma). Ver
[`scripts/README.md`](../../../../../scripts/README.md). A mano:

```bash
export AWS_PROFILE=solventa          # cuenta 368083581678
cd infra/terraform/environments/staging/web
terraform init
terraform plan -out site.tfplan -var ttl=<fecha> -var 'cd_branches=["develop"]'
terraform apply site.tfplan && rm site.tfplan     # el plan lleva la key del socio (sensible): se borra al aplicar
terraform output url
# ... ventana ...
terraform destroy -auto-approve -input=false -var ttl=<fecha>
terraform state list                              # vacío
```

## Costo

≈ US$ 0 por hora: CloudFront no cobra por hora y queda muy por debajo de su capa gratuita; S3 (unos MB), SSM estándar e IAM, centavos
o nada. Crear y borrar la distribución toma unos minutos cada vez.

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform validate
terraform test
```

`tests/staging-web.tftest.hcl` sustituye el estado remoto de la plataforma y comprueba que el sitio toma el stage, el pool y el cliente
`web`; que el rol confía solo en `develop` por defecto, admite la rama de una ventana por variable y nunca `main`; que sin API no lee la
plataforma; y que una plataforma sin las salidas de T5 falla con un mensaje claro.
