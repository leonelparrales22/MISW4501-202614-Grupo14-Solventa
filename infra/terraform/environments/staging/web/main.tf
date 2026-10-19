# Web portal of staging (T5): the site and its CloudFront distribution, with the
# API Gateway of staging/platform on the same domain. Ephemeral, like the rest of
# staging: it is switched on and off with the platform (scripts/environment).
#
# The site reads the platform through its remote state (API origin, user pool and
# the key of the portal partner); the platform reads the URL of the site from
# SSM (decision D-6 of T5), so switching the platform on or off never touches the
# site and the URL stays the same for the whole window.

locals {
  # Subject prefix of the front-end repository, which uses immutable subject
  # claims (gh api repos/<owner>/<name>/actions/oidc/customization/sub).
  front_subject_prefix = "repo:nicolasjaramillocely99@142270871/MISW4501-202614-Grupo14-Solventa-FrontWeb@1411161734"

  platform = var.api_enabled ? data.terraform_remote_state.platform[0].outputs : null

  api = var.api_enabled ? {
    domain = local.platform.api_origin.domain
    stage  = local.platform.api_origin.stage
  } : null

  # The permanent user pool of bootstrap/identity and its web client, through
  # the cognito output of the platform (same shape with or without it).
  cognito = var.api_enabled ? {
    region       = var.region
    user_pool_id = local.platform.cognito.user_pool_id
    client_id    = local.platform.cognito.client_ids["web"]
  } : null
}

data "terraform_remote_state" "platform" {
  count = var.api_enabled ? 1 : 0

  backend = "s3"
  config = {
    bucket = "solventa-tfstate-368083581678"
    key    = "staging/platform/terraform.tfstate"
    region = "us-east-2"
  }

  lifecycle {
    postcondition {
      condition = (
        can(self.outputs.api_origin.domain) &&
        can(self.outputs.cognito.client_ids["web"]) &&
        can(self.outputs.portal_partner_api_key)
      )
      error_message = "staging/platform has no api_origin, cognito or portal_partner_api_key outputs: apply the platform first (phase platform-base of scripts/environment/up.sh), or plan only the site with -var api_enabled=false."
    }
  }
}

module "web" {
  source = "../../../modules/web-portal"

  name          = "solventa-stg"
  environment   = "staging"
  force_destroy = true

  api             = local.api
  partner_api_key = var.api_enabled ? local.platform.portal_partner_api_key : null
  cognito         = local.cognito

  # Publishing role of cd-portal.yml (decision D-8 of T5): develop, plus the
  # branch of a window only while it exists (D-9 b).
  cd = {
    role_name             = "solventa-github-actions-web-stg"
    github_subject_prefix = local.front_subject_prefix
    branches              = var.cd_branches
  }
}
