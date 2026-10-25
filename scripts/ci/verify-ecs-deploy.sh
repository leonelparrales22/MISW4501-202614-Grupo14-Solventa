#!/usr/bin/env bash
# Confirms a staging deploy of backend-cd: waits until every ECS
# service of the cluster is stable and checks that each one runs the new tag.
# The deployment circuit breaker rolls a failing service back to its previous
# task definition, which is stable too, so stability alone is not enough.
#
#   verify-ecs-deploy.sh <cluster> <image-tag>
set -euo pipefail

cluster="$1"
tag="$2"

mapfile -t services < <(aws ecs list-services --cluster "$cluster" --query 'serviceArns[]' --output text | tr '\t' '\n' | sed '/^$/d')
if [[ ${#services[@]} -eq 0 ]]; then
  echo "The cluster ${cluster} has no services." >&2
  exit 1
fi

# describe-services and the waiter accept at most 10 services per call.
status=0
for ((i = 0; i < ${#services[@]}; i += 10)); do
  batch=("${services[@]:i:10}")
  aws ecs wait services-stable --cluster "$cluster" --services "${batch[@]}" || status=1

  while read -r name task_definition rollout; do
    image=$(aws ecs describe-task-definition --task-definition "$task_definition" \
      --query 'taskDefinition.containerDefinitions[0].image' --output text)
    if [[ "$image" == *":${tag}" && "$rollout" == "COMPLETED" ]]; then
      echo "ok      ${name} ${image}"
    else
      echo "FAILED  ${name} ${image} (rollout ${rollout})"
      status=1
    fi
  done < <(aws ecs describe-services --cluster "$cluster" --services "${batch[@]}" \
    --query 'services[].[serviceName, deployments[?status==`PRIMARY`]|[0].taskDefinition, deployments[?status==`PRIMARY`]|[0].rolloutState]' \
    --output text)
done

exit $status
