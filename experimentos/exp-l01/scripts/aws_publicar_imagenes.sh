#!/usr/bin/env bash
# Construye las dos imágenes (app y k6) y las sube a ECR.
# Hay que correrlo después del primer terraform apply, que es el que crea el repo.
#
# Uso: ./scripts/aws_publicar_imagenes.sh

set -euo pipefail
cd "$(dirname "$0")/.."

REGION="${AWS_REGION:-us-east-2}"
ECR_URL="$(terraform -chdir=deploy/terraform output -raw ecr_url)"
REGISTRO="${ECR_URL%%/*}"

echo "=== login en ECR ($REGISTRO) ==="
aws ecr get-login-password --region "$REGION" | docker login --username AWS --password-stdin "$REGISTRO"

echo "=== construir imagen app (orquestador + fuentes) ==="
docker build -f deploy/Dockerfile -t "$ECR_URL:app" .

echo "=== construir imagen k6 ==="
docker build -f deploy/Dockerfile.k6 -t "$ECR_URL:k6" .

echo "=== subir ==="
docker push "$ECR_URL:app"
docker push "$ECR_URL:k6"

echo
echo "Listo. Imágenes en $ECR_URL con tags app y k6."
echo "Si los servicios ya estaban corriendo, hay que forzar el redespliegue:"
echo "  aws ecs update-service --cluster expl01 --service orquestador --force-new-deployment --region $REGION"
echo "  aws ecs update-service --cluster expl01 --service fuentes --force-new-deployment --region $REGION"
