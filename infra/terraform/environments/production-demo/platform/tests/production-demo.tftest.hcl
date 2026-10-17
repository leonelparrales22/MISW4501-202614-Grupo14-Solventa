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

run "adr02_sizing_without_nat_or_multi_az_by_default" {
  command = plan

  assert {
    condition     = module.platform.desired_counts["origination"] == 3 && module.platform.desired_counts["claims"] == 2
    error_message = "ADR-02: 3 tasks in origination and 2 in every other service."
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
