#!/usr/bin/env bash
# Prints, as a JSON array, the images that backend-cd must build.
#
#   changed-images.sh <base-sha> <head-sha>
#
# An image is rebuilt when its own directory changed. A change in a shared input
# (pkg/, the Go workspace, the build context rules or the CD files) rebuilds every
# image. With no usable base (first push, forced build) every image is built.
set -euo pipefail

base="${1:-}"
head="${2:-HEAD}"

# image-name:directory
images=(
  "audit:services/audit"
  "claims:services/claims"
  "notifications:services/notifications"
  "origination:services/origination"
  "partners:services/partners"
  "kyc:stubs/kyc"
  "opendata:stubs/opendata"
  "openfinance:stubs/openfinance"
)

shared='^(pkg/|go\.work$|go\.work\.sum$|\.dockerignore$|scripts/ci/|\.github/workflows/backend-cd\.yml$)'

all() {
  printf '%s\n' "${images[@]}" | cut -d: -f1
}

if [[ -z "$base" || "$base" =~ ^0+$ ]] || ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
  selected=$(all)
else
  changed=$(git diff --name-only "$base" "$head")
  if grep -Eq "$shared" <<<"$changed"; then
    selected=$(all)
  else
    selected=""
    for entry in "${images[@]}"; do
      name="${entry%%:*}"
      dir="${entry#*:}"
      if grep -q "^${dir}/" <<<"$changed"; then
        selected+="${name}"$'\n'
      fi
    done
  fi
fi

printf '%s' "$selected" | jq -R -s -c 'split("\n") | map(select(length > 0))'
