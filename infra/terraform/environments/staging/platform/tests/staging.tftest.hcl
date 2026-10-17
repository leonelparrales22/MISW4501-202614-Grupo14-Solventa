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

run "ttl_is_required_and_validated" {
  command = plan

  variables {
    ttl = "mañana"
  }

  expect_failures = [var.ttl]
}
