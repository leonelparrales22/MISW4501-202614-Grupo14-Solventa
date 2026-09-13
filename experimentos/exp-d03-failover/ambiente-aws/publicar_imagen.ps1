# Construye la imagen del stub y la publica en el repositorio ECR creado por Terraform.
# Uso: .\publicar_imagen.ps1 [-Tag v1] [-Repo <url del repositorio ECR>]
# Si no se indica -Repo, se lee de la salida de Terraform (requiere que no haya un apply en curso).

param(
    [string]$Tag = (Get-Date -Format "yyyyMMdd-HHmm"),
    [string]$Repo = ""
)

$ErrorActionPreference = "Stop"
$raiz = Split-Path -Parent $PSScriptRoot

Push-Location (Join-Path $PSScriptRoot "infraestructura")
try {
    $repo = if ($Repo) { $Repo } else { (terraform output -raw ecr_repositorio).Trim() }
    $region = $repo.Split(".")[3]
} finally {
    Pop-Location
}

$registro = $repo.Split("/")[0]
cmd /c "aws ecr get-login-password --region $region | docker login --username AWS --password-stdin $registro"
docker build --platform linux/amd64 -t "${repo}:${Tag}" -t "${repo}:latest" (Join-Path $raiz "servicio-originacion")
docker push "${repo}:${Tag}"
docker push "${repo}:latest"
Write-Host "Imagen publicada: ${repo}:${Tag} (y latest)"
