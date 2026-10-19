#!/usr/bin/env bash
# Controls the faults injected on the three Toxiproxy proxies (openfinance,
# opendata, kyc) through the Toxiproxy API (published on localhost:8475 by
# docker-compose.yml).
#
# Latency profiles approximate those of EXP-L01 with Toxiproxy's uniform
# latency (latency +/- jitter, in ms):
#   normal     85 +/- 55   -> about 18 % of the calls above the 120 ms budget (EXP-L01 measured 17 %)
#   degradado  150 +/- 120 -> about 62 % above 120 ms (EXP-L01 measured 61 %)
#   caida      one proxy disabled (default opendata), the others with the normal profile
#
# Usage: ./toxiproxy.sh {normal|degradado|caida [openfinance|opendata|kyc]|up}
set -euo pipefail

API="${TOXIPROXY_API:-http://localhost:8475}"
PROXIES="openfinance opendata kyc"

set_latency() { # proxy latency jitter
  curl -s -X DELETE "$API/proxies/$1/toxics/profile_latency" >/dev/null || true
  curl -sf -X POST "$API/proxies/$1/toxics" -H 'Content-Type: application/json' \
    -d "{\"name\":\"profile_latency\",\"type\":\"latency\",\"stream\":\"downstream\",\"attributes\":{\"latency\":$2,\"jitter\":$3}}" >/dev/null
}

set_enabled() { # proxy true|false
  curl -sf -X POST "$API/proxies/$1" -H 'Content-Type: application/json' -d "{\"enabled\": $2}" >/dev/null
}

case "${1:-}" in
  normal)
    echo "Latency profile 'normal' (85 +/- 55 ms) on the three sources..."
    for p in $PROXIES; do set_latency "$p" 85 55; done
    ;;
  degradado)
    echo "Latency profile 'degradado' (150 +/- 120 ms) on the three sources..."
    for p in $PROXIES; do set_latency "$p" 150 120; done
    ;;
  caida)
    down="${2:-opendata}"
    echo "Outage of '$down'; the other sources keep the 'normal' profile..."
    for p in $PROXIES; do set_latency "$p" 85 55; done
    set_enabled "$down" false
    ;;
  up)
    echo "Restoring the three sources (no toxics, enabled)..."
    for p in $PROXIES; do
      curl -s -X DELETE "$API/proxies/$p/toxics/profile_latency" >/dev/null || true
      set_enabled "$p" true
    done
    ;;
  *)
    echo "Usage: $0 {normal|degradado|caida [openfinance|opendata|kyc]|up}" >&2
    exit 1
    ;;
esac
