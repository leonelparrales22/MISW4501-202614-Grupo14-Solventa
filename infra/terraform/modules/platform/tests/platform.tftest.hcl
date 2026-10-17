# Configuration tests of the platform composition (T4). They run with mocked
# providers: no credentials, no network, nothing is created. They check that
# the modules fit together and the requirements of T4 hold end to end; each
# module has its own detailed tests.
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
      account_id = "123456789012"
    }
  }

  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{}"
    }
  }

  mock_data "aws_ecr_repository" {
    defaults = {
      repository_url = "123456789012.dkr.ecr.us-east-2.amazonaws.com/solventa/image"
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
  name        = "solventa-test"
  environment = "staging"
  vpc_cidr    = "10.50.0.0/16"
  image_tag   = "0123456789ab"

  service_sizing = {
    origination   = { cpu = 512, memory = 1024, desired_count = 3, max_count = 6 }
    claims        = { cpu = 256, memory = 512, desired_count = 2, max_count = 4 }
    partners      = { cpu = 256, memory = 512, desired_count = 2, max_count = 4 }
    audit         = { cpu = 256, memory = 512, desired_count = 2, max_count = 4 }
    notifications = { cpu = 256, memory = 512, desired_count = 2, max_count = 4 }
  }
}

run "usage_plans_follow_decision_d7" {
  command = plan

  assert {
    condition     = length(module.apigateway.usage_plan_ids) == 2 && alltrue([for p in values(var.partners) : p.rate_limit == 100 && p.burst_limit == 200])
    error_message = "Each partner gets its own usage plan of 100 req/s with a burst of 200 (decision D-7)."
  }

  assert {
    condition     = length(module.apigateway.api_key_ids) == 2
    error_message = "Each partner needs its own API key."
  }

  assert {
    condition     = module.apigateway.stage_name == "staging"
    error_message = "The stage is named after the environment."
  }
}

run "every_service_has_its_own_database_secret" {
  command = plan

  assert {
    condition     = toset(keys(module.secrets.names)) == toset(["origination/database", "claims/database", "partners/database", "audit/database", "notifications/database"])
    error_message = "Each of the five services needs its own database secret."
  }

  assert {
    condition     = module.rds.databases["claims"].username == "claims_app"
    error_message = "Each service connects with its own login role, never the administrator."
  }

  assert {
    condition     = strcontains(local.db_init_script, "REVOKE ALL ON DATABASE %I FROM PUBLIC") && strcontains(local.db_init_script, "CREATE DATABASE %I OWNER %I")
    error_message = "The bootstrap task must give each role its own database and remove the default CONNECT of PUBLIC."
  }
}

run "defaults_keep_the_cheap_topology" {
  command = plan

  assert {
    condition     = length(module.network.nat_gateway_ids) == 0 && module.network.app_assign_public_ip
    error_message = "nat_mode defaults to none: no NAT Gateway, tasks with a public IP."
  }

  assert {
    condition     = length(module.network.private_subnet_ids) == 2 && length(module.network.availability_zones) == 2
    error_message = "The VPC spans two availability zones."
  }
}

run "demo_switches_on_nat_per_zone" {
  command = plan

  variables {
    nat_mode = "per_az"
  }

  assert {
    condition     = length(module.network.nat_gateway_ids) == 2 && module.network.app_assign_public_ip == false
    error_message = "per_az creates one NAT per zone and moves the tasks to the private subnets."
  }
}

run "images_follow_the_tag_and_its_overrides" {
  command = plan

  variables {
    image_tag_overrides = { kyc = "fedcba987654" }
  }

  assert {
    condition     = endswith(local.image_uris["origination"], ":0123456789ab") && endswith(local.image_uris["kyc"], ":fedcba987654")
    error_message = "Every image uses image_tag unless it has an override."
  }

  assert {
    condition     = length(data.aws_ecr_image.latest) == 0
    error_message = "With image_tag set, no image is resolved by digest."
  }
}

run "without_tag_the_latest_image_is_pinned_by_digest" {
  command = plan

  variables {
    image_tag = null
  }

  assert {
    condition     = length(data.aws_ecr_image.latest) == 8 && strcontains(local.image_uris["audit"], "@sha256:")
    error_message = "Without image_tag every image is the most recent one, pinned by digest."
  }
}

run "services_can_be_switched_off" {
  command = plan

  variables {
    services_enabled = false
  }

  assert {
    condition     = length(module.ecs.desired_counts) == 8 && alltrue([for c in values(module.ecs.desired_counts) : c == 0])
    error_message = "The five services and the three stubs keep their ECS services, at 0 tasks."
  }
}

run "rejects_incomplete_service_sizing" {
  command = plan

  variables {
    service_sizing = {
      origination = { cpu = 512, memory = 1024, desired_count = 1, max_count = 2 }
    }
  }

  expect_failures = [var.service_sizing]
}
