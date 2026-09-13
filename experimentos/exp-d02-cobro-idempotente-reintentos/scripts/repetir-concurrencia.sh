#!/usr/bin/env bash
# Repite el escenario de concurrencia dirigida (scripts/concurrencia) N
# veces, cada una contra una aceptación nueva, para confirmar si el
# diseño "ingenuo" (UNIQUE + idempotency_key, sin lock explícito) permite
# una duplicidad real del lado de la pasarela de forma consistente o si
# es ruido de una sola muestra — mismo espíritu que repetir-latencia.sh
# en EC-D01.
set -euo pipefail
cd "$(dirname "$0")/.."

REPETICIONES="${REPETICIONES:-10}"
GOROUTINES="${GOROUTINES:-10}"
BASE_URL="http://localhost:8280"

wait_healthy() {
  for i in $(seq 1 30); do
    curl -sf "$BASE_URL/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

mkdir -p resultados/raw
docker compose up -d --build --force-recreate originacion stub-pasarela db toxiproxy toxiproxy-init >/dev/null
wait_healthy
./scripts/toxiproxy.sh up

echo "[" > resultados/raw/concurrencia_repeticiones.json
for r in $(seq 1 "$REPETICIONES"); do
  echo "=== Repetición $r/$REPETICIONES ($GOROUTINES goroutines concurrentes) ==="
  salida=$(BASE_URL="$BASE_URL" GOROUTINES="$GOROUTINES" go run ./scripts/concurrencia)
  echo "$salida"
  echo "$salida" >> resultados/raw/concurrencia_repeticiones.json
  if [ "$r" -lt "$REPETICIONES" ]; then echo "," >> resultados/raw/concurrencia_repeticiones.json; fi
done
echo "]" >> resultados/raw/concurrencia_repeticiones.json

echo ""
echo "Resultados consolidados en resultados/raw/concurrencia_repeticiones.json"
