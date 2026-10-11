#!/usr/bin/env bash
# Runs the HA-D01 outage scenario end to end with Docker:
#   1. baseline        -> normal answers, warms the profile cache
#   2. total outage    -> 100% provisional answers
#   3. restore         -> half-open probes, back to normal
#   4. 900 ms latency  -> 100% provisional answers (cut at 700 ms)
#   5. restore         -> back to normal
# Usage: ./run.sh   (requires docker compose)
set -euo pipefail
cd "$(dirname "$0")"

COMPOSE="docker compose -f docker-compose.yml"
OPEN_WINDOW_SECONDS=6 # > BREAKER_OPEN_TIMEOUT_MS (5 s)
API="${ORIGINATION_URL:-http://localhost:8180}"

cleanup() { $COMPOSE --profile tools down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

k6() { $COMPOSE --profile tools run --rm -e EXPECT_MODE="$1" -e DURATION="$2" k6 run /scripts/outage.js; }

# The circuit closes only after 3 consecutive successful probes, so they are
# sent one by one before asserting normal answers again.
recover() {
  ./toxiproxy.sh up
  sleep "$OPEN_WINDOW_SECONDS"
  for i in 1 2 3; do
    curl -sf -X POST "$API/v1/risk-profiles/resolve" -H 'Content-Type: application/json' \
      -d '{"customer_id":"probe","consent_active":true}' >/dev/null
  done
}

echo "== Starting the environment"
$COMPOSE up -d --build --wait origination

echo "== 1. Baseline (Open Finance healthy)"
k6 normal 10s

echo "== 2. Total outage"
./toxiproxy.sh down
k6 provisional 10s

echo "== 3. Recovery"
recover
k6 normal 5s

echo "== 4. Latency above the 700 ms cut"
./toxiproxy.sh latency
k6 provisional 15s

echo "== 5. Recovery"
recover
k6 normal 5s

echo "== HA-D01 scenario passed. Circuit transitions:"
$COMPOSE logs origination | grep "circuit breaker state change" || true
