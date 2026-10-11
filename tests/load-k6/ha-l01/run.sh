#!/usr/bin/env bash
# Runs the HA-L01 local smoke scenario end to end with Docker. For each value
# of OPEN_FINANCE_REQUIRED (true, then false; decision D-2):
#   1. normal     latency profile of EXP-L01 on the three sources
#   2. degradado  degraded latency profile on the three sources
#   3. caida      Open Data down, the others with the normal profile
# and prints the circuit-breaker transitions of each phase (decision D-3 c).
# Then the contract test (Schemathesis) and the environment is destroyed
# (docker compose down -v).
#
# Locally the latency is only a smoke signal: the run fails when a request is
# lost or an answer is malformed. The latency that counts is measured in
# staging (CA-1).
#
# Usage: ./run.sh   (requires docker compose)
#   SMOKE_RAMP_UP / SMOKE_SUSTAIN  ramp and hold of each phase (default 10s / 20s;
#                                  the full ramp of EXP-L01 is 1m / 3m)
#   MODES                          values of OPEN_FINANCE_REQUIRED (default "true false")
#   BREAKER_*                      circuit-breaker parameters of docker-compose.yml (defaults of
#                                  the service: ratio 0.8, 20 calls, 5000 ms window, 30 in a row)
set -euo pipefail
cd "$(dirname "$0")"
# Git Bash on Windows rewrites container paths such as /scripts/peak.js.
export MSYS_NO_PATHCONV=1

COMPOSE="docker compose -p ha-l01 -f docker-compose.yml"
SMOKE_RAMP_UP="${SMOKE_RAMP_UP:-10s}"
SMOKE_SUSTAIN="${SMOKE_SUSTAIN:-20s}"
MODES="${MODES:-true false}"

cleanup() { $COMPOSE --profile tools down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

k6() { # profile open_finance_required
  $COMPOSE --profile tools run --rm \
    -e PROFILE="$1" -e OPEN_FINANCE_REQUIRED="$2" \
    -e RAMP_UP="$SMOKE_RAMP_UP" -e SUSTAIN="$SMOKE_SUSTAIN" \
    k6 run --quiet /scripts/peak.js
}

# Circuit-breaker state changes logged by origination (decision D-3 c). The
# log is counted by lines, not by time, so the clock of the Docker VM does not
# matter. With the normal profile no circuit should open.
transitions_logged() {
  $COMPOSE logs --no-log-prefix origination 2>/dev/null | grep "circuit breaker state change" || true
}
transitions_since() { # lines already printed
  local lines
  lines=$(transitions_logged | tail -n +"$(($1 + 1))")
  if [ -z "$lines" ]; then
    echo "   circuit transitions: none"
  else
    echo "   circuit transitions:"
    echo "$lines" | sed 's/^/     /'
  fi
}

echo "== Building and starting the environment"
OPEN_FINANCE_REQUIRED=true $COMPOSE up -d --build --wait origination

for required in $MODES; do
  echo "== OPEN_FINANCE_REQUIRED=$required (origination recreated, circuits closed)"
  OPEN_FINANCE_REQUIRED="$required" $COMPOSE up -d --wait --no-deps --force-recreate origination
  $COMPOSE logs --no-log-prefix origination 2>/dev/null | grep "circuit breaker per source" | sed 's/^/   /' || true
  for profile in normal degradado caida; do
    ./toxiproxy.sh up
    ./toxiproxy.sh "$profile"
    before=$(transitions_logged | wc -l)
    k6 "$profile" "$required"
    transitions_since "$before"
  done
  ./toxiproxy.sh up
done

echo "== Contract test (Schemathesis)"
$COMPOSE --profile tools run --rm schemathesis

echo "== HA-L01 smoke scenario passed"
