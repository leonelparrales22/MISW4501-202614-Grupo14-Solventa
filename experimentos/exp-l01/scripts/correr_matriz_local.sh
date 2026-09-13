#!/usr/bin/env bash
# Corre la matriz de EXP-L01 en local con Docker Compose, una combinación tras otra.
#
# Uso:
#   ./scripts/correr_matriz_local.sh                 # bloque primario: normal × caché 0 % × N en {3,5,7,9}
#   ./scripts/correr_matriz_local.sh completa        # las 16 combinaciones
#   REPETICIONES=3 ./scripts/correr_matriz_local.sh  # repetir cada una 3 veces
#
# Cada combinación deja su resultado en resultados/<fecha>_N<n>_<perfil>_c<hit>_r<rep>.json

set -euo pipefail
cd "$(dirname "$0")/.."

MODO="${1:-primaria}"
REPETICIONES="${REPETICIONES:-1}"
RPS_MAX="${RPS_MAX:-83}"
FECHA="$(date +%Y-%m-%d)"

# Si solo hay que repetir algunas: NS="5 7" ./scripts/correr_matriz_local.sh
read -r -a NS <<< "${NS:-3 5 7 9}"
declare -a PERFILES=("normal")
declare -a HITS=(0)

# La imagen se construye una sola vez acá y no en cada combinación. Cada build
# va a consultar el registro de la imagen base, y si la red anda mal se pierde
# la ejecución. Ya nos pasó.
docker compose -f deploy/docker-compose.yml build fuentes

if [[ "$MODO" == "completa" ]]; then
  PERFILES=("normal" "degradado")
  HITS=(0 0.6)
fi

correr_celda() {
  local n="$1" perfil="$2" hit="$3" rep="$4"
  local p50 p95
  case "$perfil" in
    normal)    p50=60;  p95=200 ;;
    degradado) p50=150; p95=600 ;;
    *) echo "perfil desconocido: $perfil" >&2; exit 1 ;;
  esac
  local nombre="${FECHA}_N${n}_${perfil}_c${hit}_r${rep}"
  echo "=============================================================="
  echo " Combinación: N=${n} perfil=${perfil} (p50=${p50} p95=${p95}) cache_hit=${hit} rep=${rep}"
  echo " → resultados/${nombre}.json"
  echo "=============================================================="

  N_FUENTES="$n" LAT_P50_MS="$p50" LAT_P95_MS="$p95" CACHE_HIT="$hit" \
  RPS_MAX="$RPS_MAX" RUN_NAME="$nombre" \
    docker compose -f deploy/docker-compose.yml --profile carga up \
      --no-build --abort-on-container-exit --exit-code-from k6 \
    2>&1 | tee "resultados/${nombre}.log" || true   # el 2>&1 es porque k6 manda el console.log por stderr

  docker compose -f deploy/docker-compose.yml --profile carga down -v >/dev/null 2>&1 || true
}

for rep in $(seq 1 "$REPETICIONES"); do
  for perfil in "${PERFILES[@]}"; do
    for hit in "${HITS[@]}"; do
      for n in "${NS[@]}"; do
        correr_celda "$n" "$perfil" "$hit" "$rep"
      done
    done
  done
done

echo
echo "Listo. Resúmenes en resultados/. Extraer p95/p99 con:"
echo "  python scripts/resumir_resultados.py --mediana"
