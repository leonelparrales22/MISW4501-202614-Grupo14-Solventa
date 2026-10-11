#!/bin/sh
# Measurement sequence of the HA-L01 staging window, run by the k6 task of
# infra/terraform/environments/staging/ha-l01 (image built from ../Dockerfile).
# BASE_URL is the API Gateway stage; API_KEY_A and API_KEY_B are the test keys.
#   1. peak.js x3 through the gateway with STRICT_LATENCY=1 (CA-1)
#   2. gateway-cache.js (CA-4)
#   3. rate-limit.js (CA-5)
# The summaries go to the task logs (CloudWatch). Afterwards the container
# idles so the ECS service does not run the sequence again; the window is
# destroyed with terraform destroy.
cd /scripts || exit 1

status=0
for run in 1 2 3; do
  echo "== HA-L01 CA-1 peak, run $run of 3"
  k6 run --quiet -e STRICT_LATENCY=1 -e API_KEY="$API_KEY_A" -e PROFILE="staging-run-$run" peak.js || status=1
done

echo "== HA-L01 CA-4 gateway cache"
k6 run --quiet gateway-cache.js || status=1

echo "== HA-L01 CA-5 rate limiting"
k6 run --quiet rate-limit.js || status=1

echo "== HA-L01 staging sequence finished (exit status $status; 0 = every threshold met)"
while true; do sleep 3600; done
