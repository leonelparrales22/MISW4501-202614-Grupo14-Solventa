#!/usr/bin/env bash
# Controls the fault injected on the "payment-gateway" Toxiproxy proxy through
# its HTTP API (published on localhost:8484 by docker-compose.yml).
#
#   down       total outage: the proxy refuses every connection; nothing is
#              charged.
#   ambiguous  the request reaches the gateway, which charges it, but the
#              answer never comes back (timeout toxic on the downstream): the
#              caller cannot know whether it was charged (EC-D02).
#   up         restores the gateway.
#
# Usage: ./toxiproxy.sh {down|ambiguous|up}
set -euo pipefail

API="${TOXIPROXY_API:-http://localhost:${TOXIPROXY_PORT:-8484}}"
PROXY="$API/proxies/payment-gateway"

case "${1:-}" in
  down)
    echo "Simulating a total outage of the payment gateway..."
    curl -s -X DELETE "$PROXY/toxics/ambiguous_answer" >/dev/null || true
    curl -sf -X POST "$PROXY" -H 'Content-Type: application/json' -d '{"enabled": false}' >/dev/null
    ;;
  ambiguous)
    echo "Cutting the answers of the payment gateway (the charges still happen)..."
    curl -sf -X POST "$PROXY" -H 'Content-Type: application/json' -d '{"enabled": true}' >/dev/null
    curl -sf -X POST "$PROXY/toxics" -H 'Content-Type: application/json' \
      -d '{"name":"ambiguous_answer","type":"timeout","stream":"downstream","attributes":{"timeout":0}}' >/dev/null
    ;;
  up)
    echo "Restoring the payment gateway..."
    curl -s -X DELETE "$PROXY/toxics/ambiguous_answer" >/dev/null || true
    curl -sf -X POST "$PROXY" -H 'Content-Type: application/json' -d '{"enabled": true}' >/dev/null
    ;;
  *)
    echo "Usage: $0 {down|ambiguous|up}" >&2
    exit 1
    ;;
esac
