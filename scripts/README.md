# `scripts`

Utilidades de desarrollo y CI. Cada script debe documentarse aquí al agregarse.

| Script | Uso |
|---|---|
| [`ci/changed-images.sh`](ci/changed-images.sh) | `changed-images.sh <sha-base> <sha-head>`: imprime como arreglo JSON las imágenes que `backend-cd` debe construir. Reconstruye la imagen cuyo directorio cambió y **todas** si cambió algo compartido (`pkg/`, `go.work`, `.dockerignore`, el CD) o si no hay base utilizable. Requiere `git` y `jq` |
