#!/bin/sh
# Measurement sequence of the HA-L01 staging window, run by the k6 task of
# infra/terraform/environments/staging/ha-l01 (image built from ../Dockerfile).
# BASE_URL is the API Gateway stage; API_KEY_A and API_KEY_B are the test keys.
#   0. ready.js: readiness gate, up to READY_ATTEMPTS attempts (10) separated by
#      READY_INTERVAL_S seconds (30). If no attempt passes, no load is sent.
#   Then the steps of K6_SEQUENCE (default "peak cache ratelimit"), in order:
#      peak       peak.js PEAK_RUNS times (3, from 1 to 3) with STRICT_LATENCY=1 (CA-1)
#      cache      gateway-cache.js (CA-4)
#      ratelimit  rate-limit.js (CA-5)
# Every run prints its UTC start and end and its exit code, to cut the
# CloudWatch metrics per run. PROFILE and OPEN_FINANCE_REQUIRED (set by the
# task) label the peak summaries. The summaries go to the task logs
# (CloudWatch). Afterwards the container idles so the ECS service does not run
# the sequence again; the window is destroyed with terraform destroy.
cd /scripts || exit 1

K6_SEQUENCE="${K6_SEQUENCE:-peak cache ratelimit}"
PEAK_RUNS="${PEAK_RUNS:-3}"
READY_ATTEMPTS="${READY_ATTEMPTS:-10}"
READY_INTERVAL_S="${READY_INTERVAL_S:-30}"

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

idle() {
  while true; do sleep 3600; done
}

# run_step STEP RUN SCRIPT [k6 options...]: one k6 run with its time marks.
run_step() {
  _step=$1
  _run=$2
  _script=$3
  shift 3
  echo "== HA-L01 $_step run $_run start $(now)"
  k6 run --quiet "$@" "$_script"
  _code=$?
  echo "== HA-L01 $_step run $_run end $(now) exit $_code"
  return "$_code"
}

case "$PEAK_RUNS" in
  1 | 2 | 3) ;;
  *)
    echo "== HA-L01 invalid PEAK_RUNS '$PEAK_RUNS' (1 to 3): no load sent"
    idle
    ;;
esac
for step in $K6_SEQUENCE; do
  case "$step" in
    peak | cache | ratelimit) ;;
    *)
      echo "== HA-L01 invalid step '$step' in K6_SEQUENCE (peak, cache, ratelimit): no load sent"
      idle
      ;;
  esac
done

echo "== HA-L01 staging sequence: $K6_SEQUENCE (peak runs: $PEAK_RUNS)"

ready=0
attempt=1
while [ "$attempt" -le "$READY_ATTEMPTS" ]; do
  if run_step ready "$attempt" ready.js; then
    ready=1
    break
  fi
  if [ "$attempt" -lt "$READY_ATTEMPTS" ]; then
    sleep "$READY_INTERVAL_S"
  fi
  attempt=$((attempt + 1))
done
if [ "$ready" -ne 1 ]; then
  echo "== HA-L01 NOT READY: no load sent"
  idle
fi
echo "== HA-L01 READY (attempt $attempt of $READY_ATTEMPTS)"

status=0
for step in $K6_SEQUENCE; do
  case "$step" in
    peak)
      run=1
      while [ "$run" -le "$PEAK_RUNS" ]; do
        run_step peak "$run" peak.js -e STRICT_LATENCY=1 -e API_KEY="$API_KEY_A" \
          -e PROFILE="${PROFILE:-unlabeled} (staging run $run of $PEAK_RUNS)" \
          -e OPEN_FINANCE_REQUIRED="${OPEN_FINANCE_REQUIRED:-?}" || status=1
        run=$((run + 1))
      done
      ;;
    cache) run_step cache 1 gateway-cache.js || status=1 ;;
    ratelimit) run_step ratelimit 1 rate-limit.js || status=1 ;;
  esac
done

echo "== HA-L01 staging sequence finished (exit status $status; 0 = every threshold met)"
idle
