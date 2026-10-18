# Configuration tests of the production-demo platform (T4). They run with
# mocked providers: no credentials, no network, nothing is created.
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

run "adr02_sizing_without_nat_or_multi_az_by_default" {
  command = plan

  assert {
    condition = (
      module.platform.desired_counts["origination"] == 1 &&
      module.platform.service_sizing["origination"].max_count == 1 &&
      module.platform.desired_counts["claims"] == 2
    )
    error_message = "ADR-02: 2 tasks in every service; origination keeps 1 task without autoscaling until acceptances and the advisor inbox are persisted (C5, HA-D02 #54), then goes back to 3."
  }

  assert {
    condition     = length(module.platform.nat_gateway_ids) == 0 && var.rds_multi_az == false && var.redis_multi_az == false
    error_message = "NAT and Multi-AZ stay off by default (budget); they are switched on for the final demo."
  }
}

run "final_demo_switches_on_fault_tolerance" {
  command = plan

  variables {
    nat_mode       = "per_az"
    rds_multi_az   = true
    redis_multi_az = true
  }

  assert {
    condition     = length(module.platform.nat_gateway_ids) == 2
    error_message = "per_az creates one NAT Gateway per zone."
  }
}

# T4: production-demo never creates a user pool; it uses the permanent one of
# bootstrap/identity.
run "uses_the_permanent_user_pool" {
  command = plan

  assert {
    condition     = module.platform.cognito.user_pool_id == "us-east-2_PermTest1"
    error_message = "production-demo must use the permanent user pool of bootstrap/identity."
  }

  assert {
    condition = (
      module.platform.cognito.issuer == "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1" &&
      module.platform.cognito.client_ids == tomap({ web = "web-client-test", mobile = "mobile-client-test" })
    )
    error_message = "The issuer and the app clients of the services are those of the permanent user pool."
  }
}
