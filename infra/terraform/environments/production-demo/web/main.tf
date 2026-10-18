# Web portal of production-demo (T5): the site and its CloudFront distribution,
# with the API Gateway of production-demo/platform on the same domain. Permanent
# (decision D-13 of T5): it costs about US$ 0 an hour, keeps the URL of the demo
# stable and stays when the platform is switched off (D-18).
#
# No publishing role (cd = null, #68): publishing the front end when develop is
# merged into main belongs to the main pipeline, which may enable the cd input of
# the module with branches = ["main"] or reuse scripts/publicar-portal.sh of the
# front-end repository with its own credentials.

locals {
  platform = var.api_enabled ? data.terraform_remote_state.platform[0].outputs : null

  api = var.api_enabled ? {
    domain = local.platform.api_origin.domain
    stage  = local.platform.api_origin.stage
  } : null

  # The permanent user pool of bootstrap/identity and its web client, through
  # the cognito output of the platform.
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
    key    = "production-demo/platform/terraform.tfstate"
    region = "us-east-2"
  }

  lifecycle {
    postcondition {
      condition = (
        can(self.outputs.api_origin.domain) &&
        can(self.outputs.cognito.client_ids["web"]) &&
        can(self.outputs.portal_partner_api_key)
      )
      error_message = "production-demo/platform has no api_origin, cognito or portal_partner_api_key outputs: apply the platform first (phase platform-base of the switch-on command), or plan only the site with -var api_enabled=false."
    }
  }
}

module "web" {
  source = "../../../modules/web-portal"

  name          = "solventa-demo"
  environment   = "production-demo"
  force_destroy = false

  api             = local.api
  partner_api_key = var.api_enabled ? local.platform.portal_partner_api_key : null
  cognito         = local.cognito

  cd = null
}
