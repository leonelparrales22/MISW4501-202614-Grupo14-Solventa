# Módulo `s3` — bucket privado (T4)

Bucket sin acceso público (las cuatro opciones de *public access block*), sin ACL (`BucketOwnerEnforced`), cifrado SSE-S3,
versionado, solo TLS (política que niega `aws:SecureTransport = false`) y versiones anteriores que expiran a los 30 días.

| Variable | Defecto | Descripción |
|---|---|---|
| `bucket` | — | Nombre global (p. ej. `solventa-stg-documents-<cuenta>`) |
| `force_destroy` | `false` | Solo ambientes efímeros |
| `noncurrent_version_expiration_days` | `30` | — |
| `cors_allowed_origins` | `[]` | Orígenes del navegador que pueden subir (`PUT`) y leer (`GET`) con URL firmada (HU-W2.1-01: el portal sube los soportes del siniestro directo al bucket). Sin orígenes no hay CORS. Sin comodines |
| `extra_policy_json` | `null` | Política IAM (JSON) cuyas declaraciones se suman a la del bucket, p. ej. la lectura de una distribución de CloudFront del portal web (T5). Sin ella, la política es la de siempre: solo TLS |

Salidas: `bucket`, `arn` y `regional_domain_name` (origen de una distribución de CloudFront).

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform test
```

Usan un proveedor simulado: no necesitan credenciales y no crean nada.
