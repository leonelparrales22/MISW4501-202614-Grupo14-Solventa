#!/usr/bin/env bash
# Corre los 4 escenarios cualitativos de EC-D02 (no es una matriz numérica
# como EC-D01): normal, caída total, respuesta ambigua y ventana vencida.
# El escenario de concurrencia dirigida vive aparte, en
# scripts/repetir-concurrencia.sh, porque necesita repetirse varias veces
# para descartar ruido de una sola muestra.
#
# Uso: ./scripts/run-escenarios.sh {normal|caida|ambiguo|ventana|todos}
set -euo pipefail
cd "$(dirname "$0")/.."

BASE_URL="http://localhost:8280"
mkdir -p resultados/raw

wait_healthy() {
  for i in $(seq 1 30); do
    curl -sf "$BASE_URL/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

crear_aceptacion() {
  curl -sf -X POST "$BASE_URL/aceptaciones" -H 'Content-Type: application/json' \
    -d '{"cliente_id":"cliente-escenario","monto_centavos":15000}'
}

estado_de() {
  local aceptacion_id="$1"
  curl -sf "$BASE_URL/cobros/$aceptacion_id"
}

levantar() {
  docker compose up -d --build --force-recreate --no-deps originacion stub-pasarela db >/dev/null
  wait_healthy
}

escenario_normal() {
  echo "=== Escenario: normal (baseline) ==="
  levantar
  ./scripts/toxiproxy.sh up
  resp=$(crear_aceptacion)
  aceptacion_id=$(echo "$resp" | python3 -c 'import json,sys;print(json.load(sys.stdin)["aceptacion_id"])')
  sleep 1
  estado_de "$aceptacion_id" | tee resultados/raw/normal.json
  echo ""
}

escenario_caida() {
  echo "=== Escenario: caída total ==="
  levantar
  ./scripts/toxiproxy.sh up
  ./scripts/toxiproxy.sh down
  resp=$(crear_aceptacion)
  aceptacion_id=$(echo "$resp" | python3 -c 'import json,sys;print(json.load(sys.stdin)["aceptacion_id"])')
  echo "Aceptación $aceptacion_id creada con la pasarela caída. Reintentando manualmente cada 1s..."
  curl -sf -X POST "$BASE_URL/procesar-cobro/$aceptacion_id" >/dev/null || true
  sleep 2
  echo "Restaurando la pasarela y dejando que el worker reintente..."
  ./scripts/toxiproxy.sh up
  sleep 3
  estado_de "$aceptacion_id" | tee resultados/raw/caida_total.json
  echo ""
}

escenario_ambiguo() {
  echo "=== Escenario: respuesta ambigua ==="
  levantar
  ./scripts/toxiproxy.sh up
  ./scripts/toxiproxy.sh ambiguo
  resp=$(crear_aceptacion)
  aceptacion_id=$(echo "$resp" | python3 -c 'import json,sys;print(json.load(sys.stdin)["aceptacion_id"])')
  key=$(echo "$resp" | python3 -c 'import json,sys;print(json.load(sys.stdin)["idempotency_key"])')
  curl -sf -X POST "$BASE_URL/procesar-cobro/$aceptacion_id" >/dev/null || true
  sleep 2
  echo "Restaurando la pasarela y dejando que el worker reintente con la misma idempotency_key..."
  ./scripts/toxiproxy.sh up
  sleep 3
  estado_de "$aceptacion_id" | tee resultados/raw/ambiguo_bd.json
  echo "Conteo real del lado de la pasarela (clave=$key):"
  curl -sf http://localhost:9100/debug/cobros | tee resultados/raw/ambiguo_pasarela.json
  echo ""
}

escenario_ventana() {
  echo "=== Escenario: ventana de reintentos vencida ==="
  RETRY_WINDOW_SECONDS=8 BACKOFF_BASE_MS=500 BACKOFF_MAX_MS=1000 WORKER_POLL_MS=500 \
    docker compose up -d --build --force-recreate --no-deps originacion >/dev/null
  wait_healthy
  ./scripts/toxiproxy.sh down
  resp=$(crear_aceptacion)
  aceptacion_id=$(echo "$resp" | python3 -c 'import json,sys;print(json.load(sys.stdin)["aceptacion_id"])')
  echo "Aceptación $aceptacion_id creada con ventana de 8s y pasarela caída todo el tiempo..."
  sleep 12
  estado_de "$aceptacion_id" | tee resultados/raw/ventana_vencida.json
  ./scripts/toxiproxy.sh up
  echo ""
}

case "${1:-todos}" in
  normal) escenario_normal ;;
  caida) escenario_caida ;;
  ambiguo) escenario_ambiguo ;;
  ventana) escenario_ventana ;;
  todos)
    escenario_normal
    escenario_caida
    escenario_ambiguo
    escenario_ventana
    ;;
  *) echo "Uso: $0 {normal|caida|ambiguo|ventana|todos}" >&2; exit 1 ;;
esac
