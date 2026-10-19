# Módulo `ecr` — repositorios de imágenes (T4)

Repositorios `<prefix>/<imagen>` con etiquetas inmutables, escaneo al publicar, cifrado AES256 y política de ciclo de vida
(imágenes sin etiqueta a los 7 días; se conservan las 30 más recientes). Lo usa una sola vez
[`bootstrap/cd-ecr`](../../bootstrap/cd-ecr/README.md): los repositorios son permanentes y los comparten todos los ambientes, que
solo los leen.

| Variable | Defecto | Descripción |
|---|---|---|
| `images` | — | Nombres de imagen |
| `prefix` | `"solventa"` | Espacio de nombres |
| `image_tag_mutability` | `"IMMUTABLE"` | Un SHA siempre apunta a la misma imagen |
| `force_delete` | `false` | Solo para repositorios efímeros |
| `untagged_expiry_days` / `images_to_keep` | `7` / `30` | Ciclo de vida |

Salidas: `repository_names`, `repository_urls` y `repository_arns`.

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform test
```

Usan un proveedor simulado: no necesitan credenciales y no crean nada.
