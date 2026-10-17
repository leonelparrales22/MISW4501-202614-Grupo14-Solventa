# Módulo `secrets` — Secrets Manager (T4)

Secretos `<prefix>/<clave>` con su valor (texto o JSON). Los nombres van aparte de los valores porque un valor sensible no puede
alimentar un `for_each`. Los valores quedan en el estado de Terraform, que vive en el bucket privado y cifrado del estado remoto.

| Variable | Defecto | Descripción |
|---|---|---|
| `prefix` | — | Prefijo de los nombres |
| `secrets` | — | Clave → descripción |
| `values` | — | Clave → valor (sensible) |
| `recovery_window_in_days` | `7` | `0` borra al instante (ambientes efímeros que se recrean con el mismo nombre) |

Salidas: `arns` y `names`.

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform test
```

Usan un proveedor simulado: no necesitan credenciales y no crean nada.
