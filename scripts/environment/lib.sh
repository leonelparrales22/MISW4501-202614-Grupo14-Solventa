#!/usr/bin/env bash
# Common functions of scripts/environment/up.sh and down.sh (T5, decisions D-16 and
# D-18, adjusted by #68 and #69). Sourced, never run on its own.
#
# Every AWS call made here outside Terraform is a read (STS, ECR, SSM, S3 head, ECS
# describe/wait, CloudWatch Logs); the only mutations are terraform apply/destroy, the
# db-init task (aws ecs run-task, the literal db_init_command output), the dispatch of
# the front-end workflow (staging) and the upload of publicar-portal.sh (production-demo).

ACCOUNT_ID="368083581678"
REGION="us-east-2"
STATE_BUCKET="solventa-tfstate-368083581678"
IDENTITY_STATE_KEY="bootstrap/identity/terraform.tfstate"
FRONT_REPO="nicolasjaramillocely99/MISW4501-202614-Grupo14-Solventa-FrontWeb"
PRODUCTION_IMAGE_TAG_PARAMETER="/solventa/production-demo/image-tag"
IMAGES=(origination claims partners audit notifications openfinance opendata kyc email payment-gateway policy-core push)
ECS_SERVICES=("${IMAGES[@]}")
PERMANENT_USERS=(asegurado@example.com asesor@example.com operaciones@example.com socio@example.com)

LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$LIB_DIR/../.." && pwd)"

export AWS_PAGER=""
export AWS_REGION="$REGION"
export TF_IN_AUTOMATION=1

# --- Output ---------------------------------------------------------------------------

say() { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
warn() { printf 'WARNING %s\n' "$*" >&2; }
die() { printf 'ERROR %s\n' "$*" >&2; exit 1; }

# Read-only AWS CLI calls: keep Git Bash from rewriting /solventa/... or /ecs/... and
# print UTF-8 on Windows.
aws_ro() { MSYS_NO_PATHCONV=1 PYTHONUTF8=1 aws "$@"; }

# --- Arguments --------------------------------------------------------------------------

# parse_environment_args <usage-function> "$@": sets ENVIRONMENT, DRY_RUN, YES, CONFIRM,
# ONLY, FROM, SKIP (comma lists). production-demo without --confirm-production-demo exits
# with code 2 before anything is read.
parse_environment_args() {
  local usage_fn="$1"
  shift
  ENVIRONMENT=""
  DRY_RUN=0
  YES=0
  CONFIRM=0
  ONLY=""
  FROM=""
  SKIP=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -h | --help) "$usage_fn"; exit 0 ;;
      staging | production-demo)
        [[ -z "$ENVIRONMENT" ]] || { echo "Only one environment, please." >&2; exit 2; }
        ENVIRONMENT="$1" ;;
      --dry-run) DRY_RUN=1 ;;
      --yes) YES=1 ;;
      --confirm-production-demo) CONFIRM=1 ;;
      --only) [[ $# -ge 2 ]] || { echo "--only needs a list of phases." >&2; exit 2; }; ONLY="$2"; shift ;;
      --from) [[ $# -ge 2 ]] || { echo "--from needs a phase." >&2; exit 2; }; FROM="$2"; shift ;;
      --skip) [[ $# -ge 2 ]] || { echo "--skip needs a list of phases." >&2; exit 2; }; SKIP="${SKIP:+$SKIP,}$2"; shift ;;
      --skip-front) SKIP="${SKIP:+$SKIP,}front" ;;
      *) echo "Unknown argument: $1 (see --help)." >&2; exit 2 ;;
    esac
    shift
  done
  [[ -n "$ENVIRONMENT" ]] || { "$usage_fn" >&2; exit 2; }
  require_confirmation
}

# The guard of production-demo (R8 of the harness of agents; D-16). Exit code 2.
require_confirmation() {
  if [[ "$ENVIRONMENT" == production-demo && "$CONFIRM" != 1 ]]; then
    echo "production-demo needs --confirm-production-demo: it is the evaluable environment and it costs about US\$ 0.33 an hour while it is on." >&2
    exit 2
  fi
}

# Prints the leading comment block of a script (its --help).
print_header() { awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$1"; }

# validate_phase_names "all phases": exits with code 2 on an unknown phase of --only,
# --from or --skip.
validate_phase_names() {
  local all=("$@") p
  for p in $(tr ',' ' ' <<<"$ONLY $FROM $SKIP"); do
    [[ " ${all[*]} " == *" $p "* ]] || { echo "Unknown phase: $p (phases: ${all[*]})." >&2; exit 2; }
  done
}

# select_phases "all phases in order": prints the phases to run after --only, --from
# and --skip, in their canonical order.
select_phases() {
  local all=("$@") p selected=() started=1
  local only=",$ONLY," skip=",$SKIP,"
  [[ -n "$FROM" ]] && started=0
  for p in "${all[@]}"; do
    [[ "$p" == "$FROM" ]] && started=1
    [[ "$started" == 1 ]] || continue
    [[ -z "$ONLY" || "$only" == *",$p,"* ]] || continue
    [[ "$skip" == *",$p,"* ]] && continue
    selected+=("$p")
  done
  printf '%s\n' "${selected[@]}"
}

# --- Environment ------------------------------------------------------------------------

setup_environment() {
  ENV_DIR="$REPO_ROOT/infra/terraform/environments/$ENVIRONMENT"
  PLATFORM_DIR="$ENV_DIR/platform"
  WEB_DIR="$ENV_DIR/web"
  if [[ "$ENVIRONMENT" == staging ]]; then
    NAME="solventa-stg"
    SOURCE_BRANCH="${SOURCE_BRANCH:-develop}"
    TTL="${TTL:-$(date +%F)}"
    COST_PER_HOUR="≈ US\$ 0.26 an hour with the services on (≈ US\$ 0.08 with the tasks at 0)"
  else
    NAME="solventa-demo"
    SOURCE_BRANCH="main"
    COST_PER_HOUR="≈ US\$ 0.33 an hour (no NAT, no Multi-AZ)"
  fi
  # Plans and resolved image tags stay here between calls, so a reviewed plan can be
  # applied with --yes. Plans hold sensitive values: private directory, and each plan
  # is deleted as soon as it is applied.
  STATE_DIR="${STATE_DIR:-${TMPDIR:-/tmp}/solventa-environment-$ENVIRONMENT}"
  umask 077
  mkdir -p "$STATE_DIR"
}

# --- Preflight --------------------------------------------------------------------------

version_at_least() { # have want
  [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n 1)" == "$2" ]]
}

check_tools() { # extra tools...
  local t v
  for t in terraform aws git curl "$@"; do
    command -v "$t" >/dev/null 2>&1 || die "missing tool: $t"
  done
  v="$(terraform version | head -n 1 | sed 's/^Terraform v//')"
  version_at_least "$v" 1.11 || die "Terraform $v: 1.11 or newer is needed (S3 native locking)."
  say "Tools: terraform $v, $(aws --version 2>&1 | cut -d' ' -f1), $(git --version | cut -d' ' -f3), curl"
}

check_account() {
  local account
  account="$(aws_ro sts get-caller-identity --query Account --output text 2>/dev/null | tr -d '\r' || true)"
  [[ "$account" == "$ACCOUNT_ID" ]] || die "AWS account '$account' instead of $ACCOUNT_ID (AWS_PROFILE=solventa?)."
  say "AWS account $account, region $REGION"
}

check_identity_state() {
  aws_ro s3api head-object --bucket "$STATE_BUCKET" --key "$IDENTITY_STATE_KEY" >/dev/null 2>&1 ||
    return 1
  say "Permanent user pool: state $IDENTITY_STATE_KEY found"
}

# P1 (planes/T4, #66): both platform compositions always read bootstrap/identity; no
# identity variable is passed.
compositions_read_identity() {
  local f
  for f in "$REPO_ROOT/infra/terraform/environments/staging/platform/main.tf" \
    "$REPO_ROOT/infra/terraform/environments/production-demo/platform/main.tf"; do
    grep -q 'data "terraform_remote_state" "identity"' "$f" || return 1
    grep -q "$IDENTITY_STATE_KEY" "$f" || return 1
    grep -q 'existing_cognito' "$f" || return 1
  done
}

# --- Image tags ---------------------------------------------------------------------------

is_sha12() { [[ "$1" =~ ^[0-9a-f]{12}$ ]]; }

# staging: for each image, the newest tag of 12 hexadecimal characters whose commit is
# in origin/<SOURCE_BRANCH> (never prueba-* or any other tag). Writes $STATE_DIR/images.
resolve_staging_images() {
  local image tags tag chosen line=""
  git -C "$REPO_ROOT" fetch --quiet origin "$SOURCE_BRANCH" || die "git fetch origin $SOURCE_BRANCH failed."
  : >"$STATE_DIR/images.tmp"
  printf '%-16s %s\n' IMAGE TAG
  for image in "${IMAGES[@]}"; do
    tags="$(aws_ro ecr describe-images --repository-name "solventa/$image" \
      --query 'reverse(sort_by(imageDetails,&imagePushedAt))[].imageTags[]' --output text 2>/dev/null | tr '\t\r' '\n ' || true)"
    chosen=""
    for tag in $tags; do
      is_sha12 "$tag" || continue
      if git -C "$REPO_ROOT" merge-base --is-ancestor "$tag" "origin/$SOURCE_BRANCH" 2>/dev/null; then
        chosen="$tag"
        break
      fi
    done
    [[ -n "$chosen" ]] || die "solventa/$image has no 12-character tag from origin/$SOURCE_BRANCH (has backend-cd published it?)."
    printf '%-16s %s\n' "$image" "$chosen"
    printf '%s=%s\n' "$image" "$chosen" >>"$STATE_DIR/images.tmp"
    line="${line:+$line,}\"$image\"=\"$chosen\""
  done
  mv "$STATE_DIR/images.tmp" "$STATE_DIR/images"
  printf '{%s}' "$line" >"$STATE_DIR/image_tag_overrides"
}

# production-demo (#69): the tag is the SHA of the merge to main that the CD of main
# (the pipeline of Maycol) publishes in SSM after building every image with it. T5 only
# reads it. Prints the tag; exits when it is missing, "none" (its initial value) or not a
# 12-character SHA. Never falls back to "the most recent image".
read_production_image_tag() { # [parameter]
  local parameter="${1:-$PRODUCTION_IMAGE_TAG_PARAMETER}" value
  value="$(aws_ro ssm get-parameter --name "$parameter" --query Parameter.Value --output text 2>/dev/null | tr -d '\r\n' || true)"
  if [[ -z "$value" || "$value" == none || "$value" == None ]]; then
    echo "ERROR falta correr el CD de main: no publicó la etiqueta en $parameter (valor: '${value:-<no existe>}')." >&2
    return 1
  fi
  if ! is_sha12 "$value"; then
    echo "ERROR $parameter tiene '$value', que no es un SHA de 12 caracteres hexadecimales." >&2
    return 1
  fi
  printf '%s' "$value"
}

resolve_production_image() {
  local tag
  tag="$(read_production_image_tag)" || exit 1
  printf '%s' "$tag" >"$STATE_DIR/image_tag"
  printf '%-16s %s\n' "ALL IMAGES" "$tag (from $PRODUCTION_IMAGE_TAG_PARAMETER)"
}

ensure_images() {
  if [[ "$ENVIRONMENT" == staging ]]; then
    [[ -s "$STATE_DIR/image_tag_overrides" ]] || resolve_staging_images
  else
    [[ -s "$STATE_DIR/image_tag" ]] || resolve_production_image
  fi
}

# Variables of the platform composition. staging: ttl, extra_tags and the tag of each
# image. production-demo (#69): only image_tag (plus services_enabled per phase); never
# nat_mode, rds_multi_az or redis_multi_az, so the defaults of the composition hold, the
# same ones the CD of main uses.
platform_vars() { # services_enabled -> one argument per line
  printf '%s\n' "-var=services_enabled=$1"
  if [[ "$ENVIRONMENT" == staging ]]; then
    printf '%s\n' "-var=ttl=$TTL" "-var=image_tag_overrides=$(cat "$STATE_DIR/image_tag_overrides")"
    [[ -n "${EXTRA_TAGS:-}" ]] && printf '%s\n' "-var=extra_tags=$EXTRA_TAGS"
  else
    printf '%s\n' "-var=image_tag=$(cat "$STATE_DIR/image_tag")"
  fi
  return 0
}

web_vars() { # api_enabled -> one argument per line
  printf '%s\n' "-var=api_enabled=$1"
  if [[ "$ENVIRONMENT" == staging ]]; then
    # develop (on demand) and main (cd-portal.yml publishes staging on every merge to main).
    local branches='"develop","main"' b
    for b in $(tr ',' ' ' <<<"${CD_EXTRA_BRANCHES:-}"); do branches="$branches,\"$b\""; done
    printf '%s\n' "-var=ttl=$TTL" "-var=cd_branches=[$branches]"
    [[ -n "${EXTRA_TAGS:-}" ]] && printf '%s\n' "-var=extra_tags=$EXTRA_TAGS"
  fi
  return 0
}

# --- Terraform ------------------------------------------------------------------------------

tf_init() { # dir
  terraform -chdir="$1" init -input=false -reconfigure >"$STATE_DIR/init.log" 2>&1 ||
    { cat "$STATE_DIR/init.log" >&2; die "terraform init failed in $1"; }
}

# Prints the summary of a plan log: the Plan line, what would be destroyed or replaced,
# and the creations that the cheap topology must not have.
plan_summary() { # log
  grep -E '^(Plan:|No changes)' "$1" || true
  grep -E 'will be destroyed|must be replaced' "$1" | sed 's/^/  /' || true
  printf '  to create: %s NAT gateways, %s Elastic IPs, %s Cognito resources\n' \
    "$(grep -cE '# .*aws_nat_gateway\..* will be created' "$1" || true)" \
    "$(grep -cE '# .*aws_eip\..* will be created' "$1" || true)" \
    "$(grep -cE '# .*aws_cognito_[a-z_]+\..* will be created' "$1" || true)"
}

planned_destroys() { # log
  sed -n 's/^Plan: .* \([0-9][0-9]*\) to destroy.*/\1/p' "$1" | head -n 1
}

# tf_phase phase dir guard-destroys var-args...: plans the phase. With --dry-run, a
# read-only plan (-lock=false, nothing saved). Without --yes, saves the plan, prints its
# summary and stops the run. With --yes, applies the saved plan of the phase (or a fresh
# one) and deletes it.
tf_phase() {
  local phase="$1" dir="$2" guard="$3" plan_file log destroys
  shift 3
  plan_file="$STATE_DIR/$phase.tfplan"
  log="$STATE_DIR/$phase.plan.log"
  tf_init "$dir"
  if [[ "$DRY_RUN" == 1 ]]; then
    terraform -chdir="$dir" plan -no-color -input=false -lock=false "$@" >"$log" 2>&1 ||
      { tail -n 30 "$log" >&2; die "plan of $phase failed (log: $log)"; }
    plan_summary "$log"
    return 0
  fi
  if [[ "$YES" == 1 && -s "$plan_file" ]]; then
    say "Applying the saved plan of $phase ($plan_file)"
  else
    terraform -chdir="$dir" plan -no-color -input=false -out="$plan_file" "$@" >"$log" 2>&1 ||
      { tail -n 30 "$log" >&2; die "plan of $phase failed (log: $log)"; }
    plan_summary "$log"
    destroys="$(planned_destroys "$log")"
    if [[ "$guard" == guard && -n "$destroys" && "$destroys" != 0 ]]; then
      rm -f "$plan_file"
      die "the plan of $phase destroys $destroys resources; review $log before going on."
    fi
    if [[ "$YES" != 1 ]]; then
      say "Plan saved in $plan_file (full output: $log)."
      say "Review it and run the same command with --yes (same STATE_DIR) to apply it."
      exit 0
    fi
  fi
  terraform -chdir="$dir" apply -no-color -input=false "$plan_file" >"$STATE_DIR/$phase.apply.log" 2>&1 || {
    rm -f "$plan_file"
    tail -n 30 "$STATE_DIR/$phase.apply.log" >&2
    die "apply of $phase failed (log: $STATE_DIR/$phase.apply.log)"
  }
  rm -f "$plan_file"
  grep -E '^Apply complete' "$STATE_DIR/$phase.apply.log" || true
}

# Value of an output, or nothing. Without state Terraform prints the warning "No outputs
# found" on stdout with exit code 0, so that text counts as nothing.
tf_output_raw() {
  local v
  v="$(terraform -chdir="$1" output -no-color -raw "$2" 2>/dev/null)" || return 0
  [[ "$v" == *"No outputs found"* || "$v" == *"Warning:"* ]] && return 0
  printf '%s' "$v"
}
