#!/usr/bin/env bash
# Corre UNA combinación de la matriz en AWS:
#   1. reconfigura los servicios con terraform apply (N, latencias)
#   2. espera a que queden estables
#   3. lanza k6 como tarea de Fargate y espera a que termine
#   4. baja el log de CloudWatch y lo deja en resultados/ en el mismo formato
#      que las ejecuciones locales, para que resumir_resultados.py lo lea igual
#
# Uso:
#   ./scripts/aws_correr_combinacion.sh <N> <perfil> <cache_hit> <rep> [corte_dep_ms]
#   ./scripts/aws_correr_combinacion.sh 3 normal 0 1
#   ./scripts/aws_correr_combinacion.sh 3 degradado 0 1
#   ./scripts/aws_correr_combinacion.sh 3 corte250 0 1 250

set -euo pipefail
cd "$(dirname "$0")/.."

# Git Bash convierte cualquier argumento que empiece por / en ruta de Windows
# (/expl01/k6 → C:/Program Files/Git/expl01/k6). Esto lo apaga.
export MSYS_NO_PATHCONV=1
# La consola de Windows no imprime los ✓ de k6 y tumba al aws cli. Todo lo que
# venga de CloudWatch va a archivo, nunca a pantalla.
export PYTHONIOENCODING=utf-8 PYTHONUTF8=1

N="$1"; PERFIL="$2"; HIT="$3"; REP="$4"; CORTE="${5:-120}"
REGION="${AWS_REGION:-us-east-2}"
RPS_MAX="${RPS_MAX:-83}"
FECHA="$(date +%Y-%m-%d)"

case "$PERFIL" in
  normal|corte250) P50=60;  P95=200 ;;
  degradado)       P50=150; P95=600 ;;
  *) echo "perfil desconocido: $PERFIL (normal | degradado | corte250)" >&2; exit 1 ;;
esac

NOMBRE="${FECHA}_aws_N${N}_${PERFIL}_c${HIT}_r${REP}"
TF="terraform -chdir=deploy/terraform"

echo "=============================================================="
echo " AWS · Combinación: N=$N perfil=$PERFIL (p50=$P50 p95=$P95) corte=$CORTE cache_hit=$HIT rep=$REP"
echo " → resultados/${NOMBRE}.json"
echo "=============================================================="

# --- 1. reconfigurar servicios ---
echo "[1/4] terraform apply con los parámetros de la combinación"
$TF apply -auto-approve -input=false \
  -var="n_fuentes=$N" -var="corte_dep_ms=$CORTE" \
  -var="lat_p50_ms=$P50" -var="lat_p95_ms=$P95" >/dev/null

CLUSTER="$($TF output -raw cluster)"
SUBRED="$($TF output -raw subred)"
SG="$($TF output -raw grupo_seguridad)"
TAREA_K6="$($TF output -raw k6_task_definition)"
LOG_GROUP="$($TF output -raw log_group_k6)"

# --- 2. esperar servicios estables ---
echo "[2/4] esperando a que orquestador y fuentes queden estables (puede tardar 1-2 min)"
aws ecs wait services-stable --region "$REGION" --cluster "$CLUSTER" --services orquestador fuentes

# Que el orquestador haya arrancado con la N correcta, no con la anterior.
# El patrón de CloudWatch necesita comillas dobles para aceptar el ":".
ESPERADO="fuentes=$N "
ULTIMO=""
for i in $(seq 1 12); do
  ULTIMO="$(aws logs filter-log-events --region "$REGION" --log-group-name "/expl01/orquestador" \
    --filter-pattern '"orquestador: fuentes="' --query 'events[-1].message' --output text 2>/dev/null || true)"
  if [[ "$ULTIMO" == *"$ESPERADO"* ]]; then break; fi
  sleep 5
done
echo "     orquestador dice: $(echo "${ULTIMO:-?}" | cut -c21-90)"

# --- 3. lanzar k6 ---
echo "[3/4] lanzando k6 (4 min de carga + arranque)"
OVERRIDES="$(python - "$N" "$HIT" "$RPS_MAX" <<'EOF'
import json, sys
n, hit, rps = sys.argv[1:4]
print(json.dumps({"containerOverrides": [{"name": "k6", "environment": [
    {"name": "BASE_URL", "value": "http://orquestador.exp.local:8080"},
    {"name": "N_FUENTES", "value": n},
    {"name": "CACHE_HIT", "value": hit},
    {"name": "RPS_MAX", "value": rps},
]}]}))
EOF
)"

TASK_ARN="$(aws ecs run-task --region "$REGION" --cluster "$CLUSTER" \
  --task-definition "$TAREA_K6" --launch-type FARGATE --count 1 \
  --network-configuration "awsvpcConfiguration={subnets=[$SUBRED],securityGroups=[$SG],assignPublicIp=ENABLED}" \
  --overrides "$OVERRIDES" \
  --query 'tasks[0].taskArn' --output text)"
TASK_ID="${TASK_ARN##*/}"
echo "     tarea: $TASK_ID"

aws ecs wait tasks-stopped --region "$REGION" --cluster "$CLUSTER" --tasks "$TASK_ARN"
CODIGO="$(aws ecs describe-tasks --region "$REGION" --cluster "$CLUSTER" --tasks "$TASK_ARN" \
  --query 'tasks[0].containers[0].exitCode' --output text)"
echo "     k6 terminó con código $CODIGO (0 = cumplió umbrales, 99 = no cumplió)"

# --- 4. bajar el log y convertirlo ---
echo "[4/4] bajando log de CloudWatch"
STREAM="k6/k6/$TASK_ID"
sleep 5  # CloudWatch a veces tarda unos segundos en tener el último evento
aws logs get-log-events --region "$REGION" --log-group-name "$LOG_GROUP" \
  --log-stream-name "$STREAM" --start-from-head --output json \
  > "resultados/${NOMBRE}.cw.json"
python - "resultados/${NOMBRE}.cw.json" "resultados/${NOMBRE}.log" <<'EOF'
import json, sys
crudo, log = sys.argv[1], sys.argv[2]
eventos = json.load(open(crudo, encoding="utf-8"))["events"]
with open(log, "w", encoding="utf-8") as fh:
    for e in eventos:
        fh.write(e["message"] + "\n")
EOF
rm -f "resultados/${NOMBRE}.cw.json"

# El RESUMEN_JSON viene en el formato de handleSummary; lo pasamos al formato
# de --summary-export para que resumir_resultados.py lo lea sin cambios.
python - "resultados/${NOMBRE}.log" "resultados/${NOMBRE}.json" <<'EOF'
import json, sys
log, salida = sys.argv[1], sys.argv[2]
linea = next((l for l in open(log, encoding="utf-8") if "RESUMEN_JSON " in l), None)
if not linea:
    print("  !! no se encontró RESUMEN_JSON en el log", file=sys.stderr); sys.exit(1)
data = json.loads(linea.split("RESUMEN_JSON ", 1)[1])
metricas = {}
for nombre, m in data["metrics"].items():
    vals = dict(m.get("values", {}))
    if m.get("type") == "rate":
        vals["value"] = vals.pop("rate", None)
    if "thresholds" in m:
        vals["thresholds"] = {k: (not v.get("ok", False)) for k, v in m["thresholds"].items()}
    metricas[nombre] = vals
json.dump({"metrics": metricas}, open(salida, "w", encoding="utf-8"), indent=2)
EOF

python - "resultados/${NOMBRE}.log" <<'EOF'
import re, sys
for l in open(sys.argv[1], encoding="utf-8"):
    if "RESUMEN_CORTO" in l or "ESTADO_FINAL" in l:
        l = re.sub(r'^.*?(RESUMEN_CORTO|ESTADO_FINAL)', r'\1', l).replace('\\"', '"').rstrip()
        print("     " + l[:200].encode("ascii", "replace").decode())
EOF
echo "     → resultados/${NOMBRE}.json"
