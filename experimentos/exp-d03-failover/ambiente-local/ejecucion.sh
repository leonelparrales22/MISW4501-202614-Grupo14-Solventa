#!/usr/bin/env bash
# Ejecución local del experimento EXP-D03 (Docker Compose), versión para Linux/macOS.
# Equivalente a ejecucion.ps1. Todos los parámetros se pasan por variables de entorno:
#   CONFIG=M1-P1 HC_INTER=5s HC_FALL=2 HC_TIMEOUT=3s ./ejecucion.sh
#   CONFIG=smoke DURACION=120 T_FALLA_COMPUTO=30 T_FALLA_BASE=75 ./ejecucion.sh
set -euo pipefail

RUN_ID="${RUN_ID:-local-$(date +%Y%m%d-%H%M%S)}"
CONFIG="${CONFIG:-M2-P1}"
export HC_INTER="${HC_INTER:-10s}" HC_FALL="${HC_FALL:-2}" HC_RISE="${HC_RISE:-2}" HC_TIMEOUT="${HC_TIMEOUT:-5s}"
export POOL_MAX_LIFETIME="${POOL_MAX_LIFETIME:-0}" POOL_MAX_IDLE_TIME="${POOL_MAX_IDLE_TIME:-0}"
export HEALTH_MODE="${HEALTH_MODE:-superficial}"
DURACION="${DURACION:-600}"
T_FALLA_COMPUTO="${T_FALLA_COMPUTO:-120}"
T_FALLA_BASE="${T_FALLA_BASE:-360}"
RPS="${RPS:-50}"
TIPO_FALLA="${TIPO_FALLA:-hang}"
INSTANCIA="${INSTANCIA:-a}"
BASE_CAIDA_SEGUNDOS="${BASE_CAIDA_SEGUNDOS:-45}"
RETRASO_REEMPLAZO="${RETRASO_REEMPLAZO:-20}"
TOKEN="${ADMIN_TOKEN:-local-token}"
export RUN_ID CONFIG RPS
export DURACION="${DURACION}s"

AQUI="$(cd "$(dirname "$0")" && pwd)"
RESULTADOS="$AQUI/../resultados/$RUN_ID"
mkdir -p "$RESULTADOS"
TIMELINE="$RESULTADOS/timeline.csv"
echo "ts,evento,detalle" > "$TIMELINE"

marcar() {
  local ts; ts="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
  echo "$ts,$1,${2:-}" >> "$TIMELINE"
  echo "[$ts] $1 ${2:-}"
}

esperar_salud() {
  for _ in $(seq 1 120); do
    if curl -fsS -m 2 "http://localhost:$1/health" > /dev/null 2>&1; then return 0; fi
    sleep 1
  done
  echo "La instancia en el puerto $1 no respondió" >&2; exit 1
}

cd "$AQUI"
echo "== Configuración $CONFIG: health $HC_INTER x $HC_FALL (timeout $HC_TIMEOUT), pool lifetime=$POOL_MAX_LIFETIME idle=$POOL_MAX_IDLE_TIME, health $HEALTH_MODE =="
docker compose up -d --build --force-recreate db stub-a stub-b haproxy
esperar_salud 18081
esperar_salud 18082
sleep $(( ${HC_INTER%s} * HC_RISE + 3 ))

K6="$(docker compose --profile carga run -d k6 | tr -d '[:space:]')"
T0=$(date +%s)
DESDE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
marcar k6_inicio "config=$CONFIG;rps=$RPS;duracion=${DURACION%s}"

inyectada=0; detectada=0; reiniciada=0; base_caida=0; base_arriba=0
if [ "$INSTANCIA" = "a" ]; then PUERTO_FALLA=18081; PUERTO_LECTURA=18082; else PUERTO_FALLA=18082; PUERTO_LECTURA=18081; fi

while true; do
  t=$(( $(date +%s) - T0 ))
  if [ $inyectada -eq 0 ] && [ $t -ge "$T_FALLA_COMPUTO" ]; then
    curl -fsS -m 5 -X POST "http://localhost:$PUERTO_FALLA/admin/falla" -H "X-Admin-Token: $TOKEN" -H "Content-Type: application/json" -d "{\"tipo\":\"$TIPO_FALLA\"}" > /dev/null
    marcar falla_computo "tipo=$TIPO_FALLA;instancia=$INSTANCIA"; inyectada=1
  fi
  if [ $inyectada -eq 1 ] && [ $detectada -eq 0 ]; then
    if docker compose logs --no-log-prefix --since "$DESDE" haproxy 2>/dev/null | grep -q "Server originacion/$INSTANCIA is DOWN"; then
      detectada=$(date +%s); echo "HAProxy marcó $INSTANCIA como DOWN"
    fi
  fi
  if [ "$TIPO_FALLA" = "hang" ] && [ $detectada -ne 0 ] && [ $reiniciada -eq 0 ] && [ $(( $(date +%s) - detectada )) -ge "$RETRASO_REEMPLAZO" ]; then
    docker compose restart "stub-$INSTANCIA" > /dev/null
    marcar reemplazo_iniciado "instancia=$INSTANCIA;metodo=docker_restart"; reiniciada=1
  fi
  if [ $base_caida -eq 0 ] && [ $t -ge "$T_FALLA_BASE" ]; then
    docker compose kill db > /dev/null; base_caida=$(date +%s); marcar falla_base "metodo=docker_kill"
  fi
  if [ $base_caida -ne 0 ] && [ $base_arriba -eq 0 ] && [ $(( $(date +%s) - base_caida )) -ge "$BASE_CAIDA_SEGUNDOS" ]; then
    docker compose start db > /dev/null; marcar base_restablecida "metodo=docker_start"; base_arriba=1
  fi
  if [ $t -ge $(( ${DURACION%s} + 8 )) ]; then break; fi
  sleep 1
done

docker wait "$K6" > /dev/null
marcar k6_fin
docker logs "$K6" > "$RESULTADOS/k6_resumen.txt" 2>&1 || true
docker compose logs -t --no-log-prefix --since "$DESDE" haproxy > "$RESULTADOS/haproxy.log" 2>/dev/null || true
docker compose logs -t --no-log-prefix --since "$DESDE" stub-a stub-b > "$RESULTADOS/stubs.log" 2>/dev/null || true
docker rm "$K6" > /dev/null
curl -fsS -m 180 "http://localhost:$PUERTO_LECTURA/admin/ofertas?run_id=$RUN_ID" -H "X-Admin-Token: $TOKEN" > "$RESULTADOS/ids.json"

if [ -z "${SIN_ANALISIS:-}" ]; then
  docker compose --profile analisis run --rm --build analizador
fi
echo "== Evidencia en $RESULTADOS =="
