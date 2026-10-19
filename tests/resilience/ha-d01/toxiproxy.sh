#!/usr/bin/env bash
# Controls the fault injected on the "openfinance" Toxiproxy proxy through its
# HTTP API (published on localhost:8474 by docker-compose.yml).
#
# Usage: ./toxiproxy.sh {down|latency|up}
set -euo pipefail

API="${TOXIPROXY_API:-http://localhost:8474}"

case "${1:-}" in
  down)
    echo "Simulating a total Open Finance outage..."
    curl -sf -X POST "$API/proxies/openfinance" \
      -H 'Content-Type: application/json' -d '{"enabled": false}' >/dev/null
    ;;
  latency)
    echo "Injecting 900 ms of latency (above the 700 ms cut) on Open Finance..."
    curl -sf -X POST "$API/proxies/openfinance/toxics" \
      -H 'Content-Type: application/json' \
      -d '{"name":"high_latency","type":"latency","stream":"downstream","attributes":{"latency":900,"jitter":0}}' >/dev/null
    ;;
  up)
    echo "Restoring Open Finance..."
    curl -s -X DELETE "$API/proxies/openfinance/toxics/high_latency" >/dev/null || true
    curl -sf -X POST "$API/proxies/openfinance" \
      -H 'Content-Type: application/json' -d '{"enabled": true}' >/dev/null
    ;;
  *)
    echo "Usage: $0 {down|latency|up}" >&2
    exit 1
    ;;
esac
