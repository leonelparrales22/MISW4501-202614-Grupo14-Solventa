#!/usr/bin/env bash
# Confirms a staging deploy of backend-cd: waits until every ECS
# service of the cluster is stable and checks that each one runs the new tag.
# The deployment circuit breaker rolls a failing service back to its previous
# task definition, which is stable too, so stability alone is not enough.
#
#   verify-ecs-deploy.sh <cluster> <image-tag>
#
# The services-stable waiter can return a few seconds before ECS marks the
# rollout COMPLETED, so a rollout still IN_PROGRESS is polled every
# POLL_SECONDS (default 15) for up to ROLLOUT_TIMEOUT seconds (default 600).
set -euo pipefail

cluster="$1"
tag="$2"
timeout="${ROLLOUT_TIMEOUT:-600}"
poll="${POLL_SECONDS:-15}"

mapfile -t services < <(aws ecs list-services --cluster "$cluster" --query 'serviceArns[]' --output text | tr '\t' '\n' | sed '/^$/d')
if [[ ${#services[@]} -eq 0 ]]; then
  echo "The cluster ${cluster} has no services." >&2
  exit 1
fi

# describe-services and the waiter accept at most 10 services per call.
status=0
for ((i = 0; i < ${#services[@]}; i += 10)); do
  aws ecs wait services-stable --cluster "$cluster" --services "${services[@]:i:10}" || status=1
done

# Prints "<service> <task definition> <rollout state>" of the primary deployment of each service.
primary_deployments() {
  local i
  for ((i = 0; i < ${#services[@]}; i += 10)); do
    aws ecs describe-services --cluster "$cluster" --services "${services[@]:i:10}" \
      --query 'services[].[serviceName, deployments[?status==`PRIMARY`]|[0].taskDefinition, deployments[?status==`PRIMARY`]|[0].rolloutState]' \
      --output text
  done
}

deadline=$((SECONDS + timeout))
while true; do
  deployments=$(primary_deployments | tr -d '\r')
  if ! grep -q 'IN_PROGRESS$' <<<"$deployments" || ((SECONDS >= deadline)); then
    break
  fi
  echo "Waiting for: $(awk '$3 == "IN_PROGRESS" { printf "%s ", $1 }' <<<"$deployments")"
  sleep "$poll"
done

while read -r name task_definition rollout; do
  image=$(aws ecs describe-task-definition --task-definition "$task_definition" \
    --query 'taskDefinition.containerDefinitions[0].image' --output text | tr -d '\r')
  if [[ "$image" == *":${tag}" && "$rollout" == "COMPLETED" ]]; then
    echo "ok      ${name} ${image}"
  else
    echo "FAILED  ${name} ${image} (rollout ${rollout})"
    status=1
  fi
done <<<"$deployments"

exit $status
