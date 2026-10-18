# Configuration tests of the web portal of staging (T5). They run with a mocked
# AWS provider and an overridden remote state: no credentials, no network,
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

  mock_data "aws_iam_openid_connect_provider" {
    defaults = {
      arn = "arn:aws:iam::368083581678:oidc-provider/token.actions.githubusercontent.com"
    }
  }
}

# The outputs that staging/platform publishes (T5), with a mock key.
override_data {
  target = data.terraform_remote_state.platform
  values = {
    outputs = {
      api_origin = {
        domain     = "abc123.execute-api.us-east-2.amazonaws.com"
        stage      = "staging"
        invoke_url = "https://abc123.execute-api.us-east-2.amazonaws.com/staging"
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

variables {
  ttl = "2026-12-31"
}

run "site_reads_the_platform_and_its_user_pool" {
  command = plan

  assert {
    condition = (
      local.api == { domain = "abc123.execute-api.us-east-2.amazonaws.com", stage = "staging" } &&
      local.cognito == { region = "us-east-2", user_pool_id = "us-east-2_Permanent", client_id = "webclientid" }
    )
    error_message = "The site puts the stage of staging/platform on its domain and writes the user pool and the web client to config.json."
  }

  assert {
    condition     = length(data.terraform_remote_state.platform) == 1
    error_message = "With the API the site reads the remote state of staging/platform."
  }
}

run "cd_trusts_develop_by_default" {
  command = plan

  assert {
    condition     = var.cd_branches == tolist(["develop"])
    error_message = "By default the publishing role of staging trusts develop only."
  }
}

run "a_window_adds_its_branch_by_variable" {
  command = plan

  variables {
    cd_branches = ["develop", "chore/T5-web-portal-hosting"]
  }

  assert {
    condition     = contains(var.cd_branches, "chore/T5-web-portal-hosting")
    error_message = "A window can make the role trust its branch, by variable and only while it exists (D-9 b)."
  }
}

run "the_role_never_trusts_main" {
  command = plan

  variables {
    cd_branches = ["develop", "main"]
  }

  expect_failures = [var.cd_branches]
}

run "site_only_without_api" {
  command = plan

  variables {
    api_enabled = false
  }

  assert {
    condition     = length(data.terraform_remote_state.platform) == 0 && local.api == null && local.cognito == null
    error_message = "With api_enabled = false the site does not read the platform and has no API nor config.json."
  }
}

run "a_platform_without_the_portal_outputs_fails_clearly" {
  command = plan

  override_data {
    target = data.terraform_remote_state.platform
    values = {
      outputs = {
        invoke_url = "https://abc123.execute-api.us-east-2.amazonaws.com/staging"
      }
    }
  }

  expect_failures = [data.terraform_remote_state.platform]
}
