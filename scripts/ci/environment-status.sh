#!/usr/bin/env bash
# Tells backend-cd whether an environment (its ECS cluster) can take a deploy.
# Prints "up" or "off: <reason>".
#
#   environment-status.sh <cluster>
#
# Up means the cluster exists and every ECS service has tasks. While
# scripts/environment/up.sh prepares the database, the cluster exists with
# every service at 0 tasks: that counts as off, because a deploy would turn the
# services on before the database is ready (0 -> N is a change inside ECS). A
# test window that takes a stub down (stub_desired_count_overrides) is off too:
# a deploy would bring the stub back in the middle of the window.
set -euo pipefail

cluster="$1"

status=$(aws ecs describe-clusters --clusters "$cluster" --query 'clusters[0].status' --output text)
if [[ "$status" != "ACTIVE" ]]; then
  echo "off: the cluster ${cluster} does not exist"
  exit 0
fi

mapfile -t services < <(aws ecs list-services --cluster "$cluster" --query 'serviceArns[]' --output text | tr '\t' '\n' | sed '/^$/d')
if [[ ${#services[@]} -eq 0 ]]; then
  echo "off: the cluster ${cluster} has no services"
  exit 0
fi

# describe-services accepts at most 10 services per call.
stopped=()
for ((i = 0; i < ${#services[@]}; i += 10)); do
  while read -r name; do
    [[ -n "$name" ]] && stopped+=("$name")
  done < <(aws ecs describe-services --cluster "$cluster" --services "${services[@]:i:10}" \
    --query 'services[?desiredCount==`0`].serviceName' --output text | tr '\t' '\n')
done

if [[ ${#stopped[@]} -gt 0 ]]; then
  echo "off: services at 0 tasks (${stopped[*]})"
  exit 0
fi

echo "up"
