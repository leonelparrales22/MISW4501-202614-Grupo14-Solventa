# Configuration tests of the web portal of production-demo (T5). They run with a
# mocked AWS provider and an overridden remote state: no credentials, no network,
# nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "368083581678"
    }
  }

  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{}"
    }
  }
}

# The outputs that production-demo/platform publishes (T5), with a mock key.
override_data {
  target = data.terraform_remote_state.platform
  values = {
    outputs = {
      api_origin = {
        domain     = "def456.execute-api.us-east-2.amazonaws.com"
        stage      = "production-demo"
        invoke_url = "https://def456.execute-api.us-east-2.amazonaws.com/production-demo"
      }
      cognito = {
        user_pool_id = "us-east-2_Permanent"
        issuer       = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_Permanent"
        jwks_url     = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_Permanent/.well-known/jwks.json"
        client_ids   = { web = "webclientid", mobile = "mobileclientid" }
      }
      portal_partner_api_key = "mock-partner-key-not-a-real-one"
    }
  }
}

run "site_reads_the_platform_and_its_user_pool" {
  command = plan

  assert {
    condition = (
      local.api == { domain = "def456.execute-api.us-east-2.amazonaws.com", stage = "production-demo" } &&
      local.cognito == { region = "us-east-2", user_pool_id = "us-east-2_Permanent", client_id = "webclientid" }
    )
    error_message = "The site puts the stage of production-demo/platform on its domain and writes the permanent user pool to config.json."
  }
}

# #68: publishing from main belongs to the main pipeline.
run "no_cd_role_in_production_demo" {
  command = plan

  assert {
    condition     = output.cd_role_arn == null
    error_message = "production-demo has no publishing role (cd = null)."
  }
}

run "site_only_without_api" {
  command = plan

  variables {
    api_enabled = false
  }

  assert {
    condition     = length(data.terraform_remote_state.platform) == 0 && local.api == null && local.cognito == null
    error_message = "With api_enabled = false the site does not read the platform."
  }
}

run "a_platform_without_the_portal_outputs_fails_clearly" {
  command = plan

  override_data {
    target = data.terraform_remote_state.platform
    values = {
      outputs = {}
    }
  }

  expect_failures = [data.terraform_remote_state.platform]
}
