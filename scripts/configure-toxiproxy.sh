#!/bin/sh
set -eu
until curl --fail --silent http://toxiproxy:8474/version >/dev/null; do sleep 1; done
curl --fail --silent --show-error -X POST http://toxiproxy:8474/proxies -H 'Content-Type: application/json' -d '{"name":"openfinance","listen":"0.0.0.0:8666","upstream":"openfinance:8081"}'
