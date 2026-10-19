#!/usr/bin/env bash
# Guards the production-demo deploy of backend-cd: a new image tag may only
# replace ECS task definitions and update ECS services. Any other change in the
# plan (drift, a default changed in a pending PR, an environment half turned
# on) stops the deploy before apply. So does a service going from 0 tasks to
# some: that is scripts/environment/up.sh turning the services on once the
# database is ready, never a deploy.
#
#   terraform show -json tfplan | check-deploy-plan.sh
#
# Prints every change as "<actions> <address>" and exits 1 if one is not allowed.
set -euo pipefail

report=$(jq -r '
  ["aws_ecs_task_definition", "aws_ecs_service"] as $allowed
  | .resource_changes[]?
  | select(.mode == "managed" and .change.actions != ["no-op"])
  | (if (.type | IN($allowed[])) | not then "REJECTED"
     elif .type == "aws_ecs_service" and .change.before.desired_count == 0 and (.change.after.desired_count // 0) > 0 then "REJECTED"
     else "ok      " end)
    + "  \(.change.actions | join("+")) \(.address)"')

if [[ -z "$report" ]]; then
  echo "No changes: the tag is already deployed."
  exit 0
fi

echo "$report"
if grep -q '^REJECTED' <<<"$report"; then
  echo "The plan changes resources other than the ECS task definitions and services, or turns services on from 0 tasks; apply it by hand (scripts/environment/up.sh) after reviewing it." >&2
  exit 1
fi
