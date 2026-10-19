#!/usr/bin/env bash
# Switches an environment on, in order (T5, decision D-16; #68 and #69):
#
#   preflight      tools, AWS account, the permanent user pool (bootstrap/identity) and its
#                  reading in both platform compositions (P1)
#   images         staging: per image, the newest 12-character tag whose commit is in
#                  origin/develop (never prueba-*); production-demo: the tag that the CD of
#                  main publishes in SSM /solventa/production-demo/image-tag ("none" or
#                  missing: it stops)
#   platform-base  <env>/platform with services_enabled=false (stops if the plan destroys)
#   db-init        the literal db_init_command output: one database and role per service
#   site           <env>/web: bucket, CloudFront, config.json and SSM parameters
#   platform       <env>/platform with services_enabled=true (the portal URL comes from SSM),
#                  waits for the 10 ECS services and checks the links of origination
#   front          staging: dispatches cd-portal.yml from develop (only after the PR of the
#                  front end is merged); production-demo: builds main in FRONT_DIR and runs
#                  its scripts/publicar-portal.sh, or skips with a warning
#   smoke          tests/e2e/portal-web/smoke.sh
#
# Usage:
#   up.sh <staging|production-demo> [--only <phases>] [--from <phase>] [--skip <phases>]
#         [--skip-front] [--dry-run] [--yes] [--confirm-production-demo]
#
#   (no --yes)   every phase up to the first Terraform one runs; that one is planned, the plan
#                is saved in STATE_DIR and the command stops so it can be reviewed
#   --yes        applies the saved plan of each phase (or a fresh one) and goes on
#   --dry-run    reads only: STS, ECR, SSM, git fetch and terraform plan -lock=false
#   production-demo needs --confirm-production-demo (exit code 2 without it). The harness of
#   agents never runs it (R8): a person runs it from their terminal.
#
# Environment:
#   STATE_DIR          plans and resolved tags (default ${TMPDIR:-/tmp}/solventa-environment-<env>)
#   TTL                staging: TTL tag, YYYY-MM-DD (default today)
#   EXTRA_TAGS         staging: extra tags, for example '{Owner="nicolas",Story="T5"}'
#   CD_EXTRA_BRANCHES  staging: branches added to develop in the publishing role (comma list)
#   FRONT_REF          staging: branch dispatched in the front phase (default develop)
#   FRONT_DIR          production-demo: clean checkout of the front end at origin/main
#   SEED_USER_PASSWORD password of the permanent users for the smoke phase (default
#                      SOLVENTA_IDENTITY_SEED_PASSWORD); never printed
#   SMOKE_PHASES       phases of the smoke test (default all)
set -euo pipefail

# shellcheck source=scripts/environment/lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"

PHASES_ALL=(preflight images platform-base db-init site platform front smoke)

usage() { print_header "$0"; }

parse_environment_args usage "$@"
validate_phase_names "${PHASES_ALL[@]}"
setup_environment
mapfile -t PHASES < <(select_phases "${PHASES_ALL[@]}")
[[ ${#PHASES[@]} -gt 0 ]] || { echo "No phase selected." >&2; exit 2; }
say "up.sh $ENVIRONMENT: ${PHASES[*]}$([[ "$DRY_RUN" == 1 ]] && echo ' (dry run)')$([[ "$YES" == 1 ]] && echo ' (--yes)')"
say "STATE_DIR $STATE_DIR"
# A planning run (without --yes) that includes the images phase recomputes the tags;
# --yes reuses the ones of the reviewed run, so it applies what was reviewed.
if [[ " ${PHASES[*]} " == *" images "* && "$YES" != 1 ]]; then
  rm -f "$STATE_DIR/images" "$STATE_DIR/image_tag_overrides" "$STATE_DIR/image_tag"
fi

has_phase() { [[ " ${PHASES[*]} " == *" $1 "* ]]; }

phase_preflight() {
  step "preflight"
  local extra=()
  if has_phase front; then
    if [[ "$ENVIRONMENT" == staging ]]; then
      extra+=(gh)
    elif [[ -n "${FRONT_DIR:-}" ]]; then
      extra+=(node npm)
    fi
  fi
  check_tools "${extra[@]}"
  if [[ " ${extra[*]} " == *" node "* ]]; then
    version_at_least "$(node --version | tr -d v)" 24 || die "Node $(node --version): 24 or newer is needed to build the front end."
  fi
  check_account
  if ! check_identity_state; then
    [[ "$DRY_RUN" == 1 ]] && warn "falta P1: no existe $IDENTITY_STATE_KEY (pool permanente sin aplicar)" ||
      die "falta P1: no existe $IDENTITY_STATE_KEY (pool permanente sin aplicar)."
  fi
  if compositions_read_identity; then
    say "P1: staging/platform and production-demo/platform read bootstrap/identity"
  else
    [[ "$DRY_RUN" == 1 ]] && warn "falta P1: las composiciones no leen bootstrap/identity" ||
      die "falta P1: las composiciones no leen bootstrap/identity."
  fi
}

phase_images() {
  step "images ($ENVIRONMENT)"
  if [[ "$YES" == 1 && ( -s "$STATE_DIR/image_tag_overrides" || -s "$STATE_DIR/image_tag" ) ]]; then
    say "Using the tags of the reviewed run ($STATE_DIR):"
    if [[ "$ENVIRONMENT" == staging ]]; then sed 's/=/  /' "$STATE_DIR/images"; else cat "$STATE_DIR/image_tag"; echo; fi
    return 0
  fi
  if [[ "$ENVIRONMENT" == staging ]]; then
    resolve_staging_images
  else
    resolve_production_image
  fi
}

phase_platform_base() {
  step "platform-base: $ENVIRONMENT/platform with the tasks at 0"
  ensure_images
  local vars
  mapfile -t vars < <(platform_vars false)
  tf_phase platform-base "$PLATFORM_DIR" guard "${vars[@]}"
}

phase_db_init() {
  step "db-init"
  local cmd arn cluster code ready
  if [[ "$DRY_RUN" == 1 ]]; then say "dry run: db-init skipped (it starts an ECS task)"; return 0; fi
  if [[ "$YES" != 1 ]]; then say "db-init starts an ECS task: run it with --yes."; exit 0; fi
  tf_init "$PLATFORM_DIR"
  cmd="$(tf_output_raw "$PLATFORM_DIR" db_init_command)"
  cluster="$(tf_output_raw "$PLATFORM_DIR" ecs_cluster)"
  [[ "$cmd" == "aws ecs run-task "* && -n "$cluster" ]] || die "no db_init_command output: apply platform-base first."
  say "Running: $cmd"
  arn="$(MSYS_NO_PATHCONV=1 bash -c "$cmd --query 'tasks[0].taskArn' --output text" | tr -d '\r')"
  [[ "$arn" == arn:aws:ecs:* ]] || die "the db-init task did not start: $arn"
  say "Task ${arn##*/}: waiting for it to stop"
  aws_ro ecs wait tasks-stopped --cluster "$cluster" --tasks "$arn"
  code="$(aws_ro ecs describe-tasks --cluster "$cluster" --tasks "$arn" --query 'tasks[0].containers[0].exitCode' --output text | tr -d '\r')"
  [[ "$code" == 0 ]] || die "db-init ended with exit code $code (logs: prefix db-init of $(tf_output_raw "$PLATFORM_DIR" log_group))."
  sleep 5
  ready="$(aws_ro logs filter-log-events --log-group-name "$(tf_output_raw "$PLATFORM_DIR" log_group)" \
    --log-stream-name-prefix db-init --filter-pattern '"ready"' --start-time "$(( ($(date +%s) - 900) * 1000 ))" \
    --query 'events[].message' --output text 2>/dev/null | tr '\t' '\n' | grep -c 'database .* ready' || true)"
  [[ "$ready" -ge 5 ]] || die "db-init exit code 0 but only $ready of 5 'database <svc> ready' lines in the log."
  say "db-init: exit code 0, $ready databases ready"
}

phase_site() {
  step "site: $ENVIRONMENT/web"
  local api=true vars
  if [[ "$DRY_RUN" == 1 ]]; then
    tf_init "$PLATFORM_DIR"
    if [[ -z "$(tf_output_raw "$PLATFORM_DIR" invoke_url)" ]]; then
      say "dry run: $ENVIRONMENT/platform has no state yet, so the site is planned with api_enabled=false"
      api=false
    fi
  fi
  mapfile -t vars < <(web_vars "$api")
  tf_phase site "$WEB_DIR" noguard "${vars[@]}"
  [[ "$DRY_RUN" == 1 ]] || say "Portal: $(tf_output_raw "$WEB_DIR" url)"
}

phase_platform() {
  step "platform: $ENVIRONMENT/platform with the tasks (portal URL from SSM)"
  ensure_images
  local vars cluster url td offer
  mapfile -t vars < <(platform_vars true)
  tf_phase platform "$PLATFORM_DIR" noguard "${vars[@]}"
  [[ "$DRY_RUN" == 1 ]] && return 0
  cluster="$(tf_output_raw "$PLATFORM_DIR" ecs_cluster)"
  say "Waiting for the ECS services to be stable"
  aws_ro ecs wait services-stable --cluster "$cluster" --services "${ECS_SERVICES[@]}"
  tf_init "$WEB_DIR"
  url="$(tf_output_raw "$WEB_DIR" url)"
  td="$(aws_ro ecs describe-services --cluster "$cluster" --services origination --query 'services[0].taskDefinition' --output text | tr -d '\r')"
  offer="$(aws_ro ecs describe-task-definition --task-definition "$td" \
    --query "taskDefinition.containerDefinitions[0].environment[?name=='OFFER_LINK_BASE_URL'].value | [0]" --output text | tr -d '\r')"
  [[ -n "$url" && "$offer" == "$url" ]] || die "origination has OFFER_LINK_BASE_URL '$offer' instead of the portal '$url'."
  say "Services stable; origination links to the portal ($url)"
}

phase_front() {
  step "front: publish the web portal of $ENVIRONMENT"
  if [[ "$DRY_RUN" == 1 ]]; then say "dry run: front skipped"; return 0; fi
  if [[ "$ENVIRONMENT" == production-demo ]]; then
    if [[ -z "${FRONT_DIR:-}" ]]; then
      warn "el front de production-demo no se publicó: corre la fase front con FRONT_DIR o publícalo con el pipeline de main de Maycol."
      return 0
    fi
    if [[ "$YES" != 1 ]]; then say "front builds main in $FRONT_DIR and uploads it: run it with --yes."; exit 0; fi
    [[ -f "$FRONT_DIR/scripts/publicar-portal.sh" ]] || die "$FRONT_DIR is not a checkout of the front end (no scripts/publicar-portal.sh)."
    [[ -z "$(git -C "$FRONT_DIR" status --porcelain)" ]] || die "$FRONT_DIR has uncommitted changes."
    git -C "$FRONT_DIR" fetch --quiet origin main
    [[ "$(git -C "$FRONT_DIR" rev-parse HEAD)" == "$(git -C "$FRONT_DIR" rev-parse origin/main)" ]] ||
      die "$FRONT_DIR is not at origin/main (it never switches branches by itself)."
    say "Building main ($(git -C "$FRONT_DIR" rev-parse --short HEAD)) in $FRONT_DIR"
    (cd "$FRONT_DIR" && npm ci && npm run build -- --configuration production)
    (cd "$FRONT_DIR" && bash scripts/publicar-portal.sh production-demo)
    return 0
  fi
  # staging: the workflow, dispatched from develop (it only exists there once the PR of
  # the front end is merged).
  if [[ "$YES" != 1 ]]; then say "front dispatches cd-portal.yml: run it with --yes."; exit 0; fi
  local ref="${FRONT_REF:-develop}" before id i
  before="$(gh run list --repo "$FRONT_REPO" --workflow cd-portal.yml --limit 1 --json databaseId --jq '.[0].databaseId // 0' 2>/dev/null || echo 0)"
  gh workflow run cd-portal.yml --repo "$FRONT_REPO" --ref "$ref" -f ambiente=staging
  id=""
  for i in $(seq 1 30); do
    sleep 4
    id="$(gh run list --repo "$FRONT_REPO" --workflow cd-portal.yml --event workflow_dispatch --limit 1 --json databaseId --jq '.[0].databaseId // 0')"
    [[ -n "$id" && "$id" != 0 && "$id" != "$before" ]] && break
    id=""
  done
  [[ -n "$id" ]] || die "the dispatched run of cd-portal.yml did not show up."
  say "Run $id of cd-portal.yml ($ref)"
  gh run watch "$id" --repo "$FRONT_REPO" --exit-status
}

phase_smoke() {
  step "smoke"
  if [[ "$DRY_RUN" == 1 ]]; then say "dry run: smoke skipped"; return 0; fi
  tf_init "$WEB_DIR"
  tf_init "$PLATFORM_DIR"
  local phases
  read -r -a phases <<<"${SMOKE_PHASES:-all}"
  ENVIRONMENT="$ENVIRONMENT" SEED_USER_PASSWORD="${SEED_USER_PASSWORD:-${SOLVENTA_IDENTITY_SEED_PASSWORD:-}}" \
    bash "$REPO_ROOT/tests/e2e/portal-web/smoke.sh" "${phases[@]}"
}

for phase in "${PHASES[@]}"; do
  "phase_${phase//-/_}"
done

if [[ "$DRY_RUN" != 1 ]] && has_phase platform; then
  step "on"
  say "Portal:  $(tf_output_raw "$WEB_DIR" url)"
  say "Users:   ${PERMANENT_USERS[*]} (password: SOLVENTA_IDENTITY_SEED_PASSWORD)"
  say "Cost:    $COST_PER_HOUR"
  if [[ "$ENVIRONMENT" == production-demo ]]; then
    say "Off:     scripts/environment/down.sh production-demo --confirm-production-demo --yes   (the same day)"
  else
    say "Off:     scripts/environment/down.sh staging --yes   (in the same session)"
  fi
fi
