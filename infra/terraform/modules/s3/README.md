# Módulo `s3` — bucket privado (T4)

Bucket sin acceso público (las cuatro opciones de *public access block*), sin ACL (`BucketOwnerEnforced`), cifrado SSE-S3,
versionado, solo TLS (política que niega `aws:SecureTransport = false`) y versiones anteriores que expiran a los 30 días.

| Variable | Defecto | Descripción |
|---|---|---|
| `bucket` | — | Nombre global (p. ej. `solventa-stg-documents-<cuenta>`) |
| `force_destroy` | `false` | Solo ambientes efímeros |
| `noncurrent_version_expiration_days` | `30` | — |

Salidas: `bucket` y `arn`.

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform test
```

Usan un proveedor simulado: no necesitan credenciales y no crean nada.
