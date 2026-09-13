#!/usr/bin/env bash
# Las seis ejecuciones que acordamos para AWS, en el orden que menos redespliegues
# necesita (cada cambio de N o de perfil obliga a redesplegar un servicio).
#
#   N=3 normal caché 0 %   ×3   → el número oficial, con varianza
#   N=3 normal caché 60 %  ×1   → la única variable que no se midió en local
#   N=9 normal caché 0 %   ×1   → confirmar que la curva sigue plana
#   N=3 degradado caché 0 % ×1  → confirmar el 23 % de 503
#
# Uso: ./scripts/aws_correr_matriz.sh

set -euo pipefail
cd "$(dirname "$0")/.."

echo "############ Bloque 1: N=3 normal ############"
./scripts/aws_correr_combinacion.sh 3 normal 0   1
./scripts/aws_correr_combinacion.sh 3 normal 0   2
./scripts/aws_correr_combinacion.sh 3 normal 0   3
./scripts/aws_correr_combinacion.sh 3 normal 0.6 1

echo "############ Bloque 2: N=9 normal ############"
./scripts/aws_correr_combinacion.sh 9 normal 0 1

echo "############ Bloque 3: N=3 degradado ############"
./scripts/aws_correr_combinacion.sh 3 degradado 0 1

echo
echo "TERMINADO. Resumen:"
python scripts/resumir_resultados.py --mediana
echo
echo "No olvides destruir la infra:"
echo "  terraform -chdir=deploy/terraform destroy -auto-approve"
