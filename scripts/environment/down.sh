#!/usr/bin/env bash
# Switches an environment off (T5, decisions D-16 and D-18; #69):
#
#   staging          destroys <env>/platform and then <env>/web, checks that both states are
#                    empty and reads what could be left (distribution, bucket, SSM, role,
#                    database, snapshots, secrets, VPC). The permanent user pool is not part
#                    of either composition and stays.
#   production-demo  1. image tag: the one in SSM /solventa/production-demo/image-tag (or,
#                       when it is "none" or missing, the one the platform runs), so the task
#                       definitions do not change;
#                    2. plan and apply production-demo/platform with deletion_protection=false,
#                       ephemeral=true and services_enabled=false (locks off; secrets without
#                       recovery window, buckets with their objects, no final snapshot);
#                    3. destroy with the same variables;
#                    4. read-only checks of what could be left.
#                    It keeps production-demo/web (site and SSM, about US$ 0 an hour) and
#                    bootstrap/identity (the permanent user pool).
#
# Usage: down.sh <staging|production-demo> [--dry-run] [--yes] [--confirm-production-demo]
#   (no --yes)  plans the destroy (and, in production-demo, the lock change) and stops
#   --dry-run   reads only (terraform plan -destroy -lock=false)
#   production-demo needs --confirm-production-demo (exit code 2 without it). The harness of
#   agents never runs it (R8): a person runs it from their terminal.
#
# Environment: STATE_DIR, TTL and EXTRA_TAGS (staging), as in up.sh.
set -euo pipefail

# shellcheck source=scripts/environment/lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"

usage() { print_header "$0"; }

parse_environment_args usage "$@"
[[ -z "$ONLY$FROM$SKIP" ]] || { echo "down.sh has no phases: --only, --from and --skip do not apply." >&2; exit 2; }
setup_environment
say "down.sh $ENVIRONMENT$([[ "$DRY_RUN" == 1 ]] && echo ' (dry run)')$([[ "$YES" == 1 ]] && echo ' (--yes)')"
say "STATE_DIR $STATE_DIR"

check_tools
check_account

destroy_composition() { # name dir var-args...
  local name="$1" dir="$2" log count
  shift 2
  log="$STATE_DIR/destroy-$name.log"
  tf_init "$dir"
  if [[ "$DRY_RUN" == 1 || "$YES" != 1 ]]; then
    terraform -chdir="$dir" plan -destroy -no-color -input=false $([[ "$DRY_RUN" == 1 ]] && echo -lock=false) "$@" >"$log" 2>&1 ||
      { tail -n 30 "$log" >&2; die "plan -destroy of $name failed (log: $log)"; }
    grep -E '^(Plan:|No changes)' "$log" || true
    return 0
  fi
  terraform -chdir="$dir" destroy -auto-approve -no-color -input=false "$@" >"$log" 2>&1 ||
    { tail -n 30 "$log" >&2; die "destroy of $name failed (log: $log); retry it, never terraform state rm."; }
  grep -E '^Destroy complete' "$log" || true
  count="$(terraform -chdir="$dir" state list 2>/dev/null | wc -l | tr -d ' ')"
  [[ "$count" == 0 ]] || die "$name still has $count resources in its state."
  say "$name: state list empty"
}

leftover() { # label value (empty or None = nothing left)
  local v
  v="$(tr -d '\r' <<<"$2" | sed '/^None$/d;/^$/d')"
  if [[ -z "$v" ]]; then say "  nothing left: $1"; else warn "left: $1: $(tr '\n' ' ' <<<"$v")"; LEFT=1; fi
}

if [[ "$ENVIRONMENT" == staging ]]; then
  PLATFORM_ARGS=("-var=ttl=$TTL")
  # The site is destroyed after the platform, whose outputs no longer exist: it must not read them (window of 2026-10-18).
  WEB_ARGS=("-var=ttl=$TTL" "-var=api_enabled=false")
  if [[ -n "${EXTRA_TAGS:-}" ]]; then
    PLATFORM_ARGS+=("-var=extra_tags=$EXTRA_TAGS")
    WEB_ARGS+=("-var=extra_tags=$EXTRA_TAGS")
  fi
  # The platform first: it is what costs by the hour; then the site.
  step "destroy staging/platform"
  destroy_composition staging-platform "$PLATFORM_DIR" "${PLATFORM_ARGS[@]}"
  step "destroy staging/web"
  destroy_composition staging-web "$WEB_DIR" "${WEB_ARGS[@]}"
  [[ "$DRY_RUN" == 1 || "$YES" != 1 ]] && { say "Run it with --yes to destroy."; exit 0; }
else
  step "image tag of production-demo"
  if TAG="$(read_production_image_tag 2>/dev/null)"; then
    say "Tag $TAG (from $PRODUCTION_IMAGE_TAG_PARAMETER)"
  else
    tf_init "$PLATFORM_DIR"
    TAG="$(terraform -chdir="$PLATFORM_DIR" output -no-color images 2>/dev/null | sed -n 's/^ *"origination" *= *".*:\([0-9a-f]\{12\}\)".*/\1/p' | head -n 1 || true)"
    is_sha12 "$TAG" || die "no image tag: the SSM parameter has none and the platform runs no tagged image."
    say "Tag $TAG (the one the platform runs; the SSM parameter has none)"
  fi
  OFF_ARGS=("-var=image_tag=$TAG" "-var=services_enabled=false" "-var=deletion_protection=false" "-var=ephemeral=true")
  step "locks off and ephemeral mode (production-demo/platform)"
  tf_phase shutdown "$PLATFORM_DIR" noguard "${OFF_ARGS[@]}"
  [[ "$DRY_RUN" == 1 ]] && { step "destroy production-demo/platform (plan)"; destroy_composition production-demo-platform "$PLATFORM_DIR" "${OFF_ARGS[@]}"; exit 0; }
  step "destroy production-demo/platform"
  destroy_composition production-demo-platform "$PLATFORM_DIR" "${OFF_ARGS[@]}"
  say "Kept: production-demo/web (site, about US\$ 0 an hour) and bootstrap/identity (permanent user pool)"
fi

step "what could be left ($NAME), read-only"
LEFT=0
leftover "RDS instances" "$(aws_ro rds describe-db-instances --query "DBInstances[?starts_with(DBInstanceIdentifier,'$NAME')].DBInstanceIdentifier" --output text 2>&1 || true)"
leftover "RDS snapshots" "$(aws_ro rds describe-db-snapshots --query "DBSnapshots[?starts_with(DBSnapshotIdentifier,'$NAME')].DBSnapshotIdentifier" --output text 2>&1 || true)"
leftover "RDS automated backups" "$(aws_ro rds describe-db-instance-automated-backups --query "DBInstanceAutomatedBackups[?starts_with(DBInstanceIdentifier,'$NAME')].DBInstanceIdentifier" --output text 2>&1 || true)"
leftover "secrets $NAME/*" "$(aws_ro secretsmanager list-secrets --include-planned-deletion --query "SecretList[?starts_with(Name,'$NAME/')].Name" --output text 2>&1 || true)"
leftover "load balancers" "$(aws_ro elbv2 describe-load-balancers --query "LoadBalancers[?LoadBalancerName=='$NAME'].LoadBalancerName" --output text 2>&1 || true)"
leftover "buckets $NAME-documents/evidence" "$(aws_ro s3api list-buckets --query "Buckets[?starts_with(Name,'$NAME-documents') || starts_with(Name,'$NAME-evidence')].Name" --output text 2>&1 || true)"
if [[ "$ENVIRONMENT" == staging ]]; then
  leftover "VPC 10.50.0.0/16" "$(aws_ro ec2 describe-vpcs --filters Name=cidr,Values=10.50.0.0/16 --query 'Vpcs[].VpcId' --output text 2>&1 || true)"
  leftover "site bucket" "$(aws_ro s3api list-buckets --query "Buckets[?starts_with(Name,'$NAME-web')].Name" --output text 2>&1 || true)"
  leftover "CloudFront distribution" "$(aws_ro cloudfront list-distributions --query "DistributionList.Items[?Comment=='Solventa web portal (staging)'].Id" --output text 2>&1 || true)"
  leftover "SSM /solventa/staging/web" "$(aws_ro ssm get-parameters-by-path --path /solventa/staging/web --query 'Parameters[].Name' --output text 2>&1 || true)"
  leftover "publishing role" "$(aws_ro iam list-roles --query "Roles[?RoleName=='solventa-github-actions-web-stg'].RoleName" --output text 2>&1 || true)"
else
  leftover "VPC 10.60.0.0/16" "$(aws_ro ec2 describe-vpcs --filters Name=cidr,Values=10.60.0.0/16 --query 'Vpcs[].VpcId' --output text 2>&1 || true)"
fi
say "Permanent user pool: $(aws_ro cognito-idp list-user-pools --max-results 60 --query "UserPools[?Name=='solventa-users'].Id" --output text 2>&1 | tr -d '\r' || true) (kept)"
[[ "$LEFT" == 0 ]] || die "something was left; review it before closing the window."
say "Off. Nothing left that costs by the hour."
