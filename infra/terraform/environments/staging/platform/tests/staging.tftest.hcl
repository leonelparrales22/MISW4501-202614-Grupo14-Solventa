# Configuration tests of the staging platform (T4). They run with mocked
# providers: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {
  mock_data "aws_availability_zones" {
    defaults = {
      names = ["us-east-2a", "us-east-2b", "us-east-2c"]
    }
  }

  mock_data "aws_region" {
    defaults = {
      region = "us-east-2"
    }
  }

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

  mock_data "aws_ecr_repository" {
    defaults = {
      repository_url = "368083581678.dkr.ecr.us-east-2.amazonaws.com/solventa/image"
    }
  }

  mock_data "aws_ecr_image" {
    defaults = {
      image_digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
    }
  }
}

mock_provider "random" {}

# Outputs of bootstrap/identity (the permanent user pool), read without AWS:
# the data source is replaced at its address and the S3 backend is never called.
override_data {
  target          = data.terraform_remote_state.identity
  override_during = plan
  values = {
    outputs = {
      user_pool_id  = "us-east-2_PermTest1"
      user_pool_arn = "arn:aws:cognito-idp:us-east-2:368083581678:userpool/us-east-2_PermTest1"
      issuer        = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1"
      jwks_url      = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1/.well-known/jwks.json"
      client_ids    = { web = "web-client-test", mobile = "mobile-client-test" }
    }
  }
}

variables {
  ttl = "2026-12-31"
}

run "staging_is_ephemeral_and_small" {
  command = plan

  assert {
    condition     = length(module.platform.nat_gateway_ids) == 0
    error_message = "Staging runs without NAT by default."
  }

  assert {
    condition     = alltrue([for c in values(module.platform.desired_counts) : c == 1])
    error_message = "Staging runs one task per service and stub."
  }

  assert {
    condition     = module.platform.database_users["origination"].username == "origination_app"
    error_message = "Each service connects with its own login role."
  }
}

# HA-D02 (C5): origination keeps one task, without autoscaling, while
# acceptances and the advisor inbox live in memory.
run "origination_runs_a_single_task" {
  command = plan

  assert {
    condition = (
      module.platform.desired_counts["origination"] == 1 &&
      module.platform.service_sizing["origination"].desired_count == 1 &&
      module.platform.service_sizing["origination"].max_count == 1
    )
    error_message = "Origination runs exactly one task and never scales out (C5): desired_count = max_count = 1."
  }
}

# HA-D02: the levers of a window are optional; without them staging is the
# platform of T4 as it was.
run "window_variables_are_optional" {
  command = plan

  assert {
    condition = (
      length(var.stub_desired_count_overrides) == 0 && length(var.origination_environment) == 0 &&
      length(var.extra_tags) == 0
    )
    error_message = "Without a window every lever is empty."
  }

  assert {
    condition     = module.platform.desired_counts["payment-gateway"] == 1 && module.platform.desired_counts["email"] == 1
    error_message = "By default the payment gateway and e-mail stubs run one task each."
  }
}

# The values of ha-d02.example.tfvars (the test users are the permanent ones of
# bootstrap/identity, so the window declares none).
run "the_ha_d02_window_takes_down_only_the_payment_gateway" {
  command = plan

  variables {
    stub_desired_count_overrides = { payment-gateway = 0 }
    origination_environment      = { RETRY_WINDOW_SECONDS = "300", CHARGE_RETRY_MAX_MS = "15000" }
    extra_tags                   = { Owner = "nicolas", Story = "HA-D02" }
  }

  assert {
    condition = module.platform.desired_counts["payment-gateway"] == 0 && alltrue([
      for k, c in module.platform.desired_counts : c == 1 if k != "payment-gateway"
    ])
    error_message = "The outage lever only takes the payment gateway to 0 tasks."
  }
}

run "extra_tags_cannot_replace_the_default_tags" {
  command = plan

  variables {
    extra_tags = { Env = "production" }
  }

  expect_failures = [var.extra_tags]
}

run "ttl_is_required_and_validated" {
  command = plan

  variables {
    ttl = "mañana"
  }

  expect_failures = [var.ttl]
}

# T4: the windows use the permanent user pool of bootstrap/identity and its
# four test users; staging never creates a pool of its own.
run "uses_the_permanent_user_pool" {
  command = plan

  assert {
    condition     = module.platform.cognito.user_pool_id == "us-east-2_PermTest1"
    error_message = "Staging must use the permanent user pool of bootstrap/identity."
  }

  assert {
    condition = (
      module.platform.cognito.issuer == "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1" &&
      module.platform.cognito.client_ids == tomap({ web = "web-client-test", mobile = "mobile-client-test" })
    )
    error_message = "The issuer and the app clients of the services are those of the permanent user pool."
  }
}
