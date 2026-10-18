#!/usr/bin/env bash
# Contract test of the risk-profile routes of origination (HU-W1.2-01, OBJ 05):
# starts the real service in Docker and runs Schemathesis against
# ../../api/openapi.yaml with every check, on the routes /v1/risk-profiles:
#   1. origination without AUDIT_URL: the caller asserts consent_active, so the
#      200 answers are reached.
#   2. origination with AUDIT_URL: the consent is verified with the real audit
#      service, which has no consent for the generated customers, so the 422
#      answers are reached.
# The script exits with a non-zero code if any check fails. The environment is
# always removed at the end (down -v). Requirements: Docker (compose v2) and
# curl. The PostgreSQL secret is random, generated here and never printed.
#
# The HU-W1.2-03 routes (offers) are added to this folder by that story.
set -euo pipefail
cd "$(dirname "$0")"

command -v docker >/dev/null || {
  echo "docker is required" >&2
  exit 1
}
command -v curl >/dev/null || {
  echo "curl is required" >&2
  exit 1
}

COMPOSE="env MSYS_NO_PATHCONV=1 docker compose -f docker-compose.yml"
export ORIGINATION_PORT="${ORIGINATION_PORT:-8293}" ORIGINATION_AUDIT_PORT="${ORIGINATION_AUDIT_PORT:-8294}"
PG_PASSWORD="$(head -c 128 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-32)"
export PG_PASSWORD

cleanup() { $COMPOSE --profile tools down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

wait_healthy() { # <port>
  local i
  for i in $(seq 1 90); do
    curl -sf "http://localhost:$1/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "the service on port $1 did not become healthy" >&2
  $COMPOSE logs origination origination-audit audit | tail -40 >&2
  return 1
}

echo "== Starting origination, origination with audit, audit, Redis and the Open Finance stub"
$COMPOSE up -d --build --wait redis openfinance postgres-audit
$COMPOSE up -d --build origination origination-audit
wait_healthy "$ORIGINATION_PORT"
wait_healthy "$ORIGINATION_AUDIT_PORT"

ROUTES='^/v1/risk-profiles'

echo "== Schemathesis: origination without AUDIT_URL (the caller asserts the consent)"
$COMPOSE --profile tools run --rm schemathesis run /api/openapi.yaml --base-url=http://origination:8080 \
  --include-path-regex="$ROUTES" --checks=all --hypothesis-max-examples=100 --hypothesis-seed=1

echo "== Schemathesis: origination with AUDIT_URL (the consent is verified with audit)"
$COMPOSE --profile tools run --rm schemathesis run /api/openapi.yaml --base-url=http://origination-audit:8080 \
  --include-path-regex="$ROUTES" --checks=all --hypothesis-max-examples=100 --hypothesis-seed=2

echo "== origination contract passed."
