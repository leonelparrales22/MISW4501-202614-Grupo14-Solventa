#!/usr/bin/env bash
# Prints, as a Terraform JSON var file, the variables of staging/platform that
# scripts/environment/up.sh applied and backend-cd must reuse: ttl and
# extra_tags. Both become default tags of every resource, so a deploy with
# other values would change every resource and check-deploy-plan.sh would
# reject it.
#
#   staging-vars.sh <cluster> > staging.tfvars.json
#
# They are read from the tags of the running ECS cluster: the cluster carries
# only the provider default tags, merge(extra_tags, {Project, Env, Task, TTL}).
set -euo pipefail

cluster="$1"

aws ecs describe-clusters --clusters "$cluster" --include TAGS --query 'clusters[0].tags' --output json |
  jq '
    (map({(.key): .value}) | add // {}) as $tags
    | if ($tags.TTL // "") == "" then error("the cluster has no TTL tag") else . end
    | {
        ttl: $tags.TTL,
        extra_tags: ($tags | del(.Project, .Env, .Task, .TTL))
      }'
