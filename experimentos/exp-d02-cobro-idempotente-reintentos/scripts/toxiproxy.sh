#!/usr/bin/env bash
# Controla la falla inyectada sobre el proxy "pasarela" de Toxiproxy,
# hablando directamente con su API HTTP (publicada en localhost:8474 por
# docker-compose.yml). Se usa la API en vez de toxiproxy-cli porque la
# imagen ghcr.io/shopify/toxiproxy es "distroless" y no trae el CLI.
#
# Uso: ./scripts/toxiproxy.sh {down|ambiguo|up}
set -euo pipefail

API="${TOXIPROXY_API:-http://localhost:8474}"

case "${1:-}" in
  down)
    echo "Simulando caída total de la pasarela de pago..."
    curl -sf -X POST "$API/proxies/pasarela" \
      -H 'Content-Type: application/json' \
      -d '{"enabled": false}' >/dev/null
    ;;
  ambiguo)
    # El toxic "timeout" en stream=downstream deja pasar el REQUEST
    # intacto (el stub sí lo recibe y lo procesa de inmediato, ver
    # stub-pasarela/main.go) pero corta la conexión antes de que la
    # RESPUESTA llegue al cliente. Ese es el caso ambiguo real: el
    # cliente ve un timeout mientras el "proveedor" ya procesó el cobro.
    echo "Inyectando respuesta ambigua (corte de la respuesta, no del request)..."
    curl -sf -X POST "$API/proxies/pasarela/toxics" \
      -H 'Content-Type: application/json' \
      -d '{"name":"corte_respuesta","type":"timeout","stream":"downstream","attributes":{"timeout":1500}}' >/dev/null
    ;;
  up)
    echo "Restaurando la pasarela de pago (sin fallas)..."
    curl -s -X DELETE "$API/proxies/pasarela/toxics/corte_respuesta" >/dev/null || true
    curl -sf -X POST "$API/proxies/pasarela" \
      -H 'Content-Type: application/json' \
      -d '{"enabled": true}' >/dev/null
    ;;
  *)
    echo "Uso: $0 {down|ambiguo|up}" >&2
    exit 1
    ;;
esac
