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
    condition = toset(keys(module.secrets.names)) == toset([
      "origination/database", "claims/database", "partners/database", "audit/database", "notifications/database",
      "origination/document-link",
    ])
    error_message = "Each of the five services needs its own database secret, and origination the key of its document links."
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
    condition     = length(data.aws_ecr_image.latest) == 10 && strcontains(local.image_uris["audit"], "@sha256:")
    error_message = "Without image_tag every image is the most recent one, pinned by digest."
  }
}

run "services_can_be_switched_off" {
  command = plan

  variables {
    services_enabled = false
  }

  assert {
    condition     = length(module.ecs.desired_counts) == 10 && alltrue([for c in values(module.ecs.desired_counts) : c == 0])
    error_message = "The five services and the five stubs keep their ECS services, at 0 tasks."
  }
}

# HU-W1.3-02 / HA-D02: the payment gateway and e-mail stubs run next to the
# other three behind Cloud Map, and a staging window can take the payment
# gateway down on its own (decision D-6).
run "charge_and_email_stubs_run_behind_cloud_map" {
  command = plan

  variables {
    image_tag_overrides          = { email = "prueba-ha-d02" }
    stub_desired_count_overrides = { payment-gateway = 0 }
  }

  assert {
    condition     = alltrue([for s in ["openfinance", "opendata", "kyc", "email"] : module.ecs.desired_counts[s] == 1]) && module.ecs.desired_counts["payment-gateway"] == 0
    error_message = "Every stub runs one task, except the payment gateway taken down with its override."
  }

  assert {
    condition = (
      local.service_environment.origination.PAYMENT_GATEWAY_URL == "http://payment-gateway.solventa-test.local:9040" &&
      local.service_environment.origination.EMAIL_URL == "http://email.solventa-test.local:9030"
    )
    error_message = "Origination reaches both stubs by their Cloud Map names."
  }

  assert {
    condition     = endswith(local.image_uris["email"], ":prueba-ha-d02") && endswith(local.image_uris["payment-gateway"], ":0123456789ab")
    error_message = "The e-mail and payment gateway images accept a tag override like the others."
  }
}

# HA-D02, AWS phase: origination starts with the contractual document wired to
# S3 (with EMAIL_URL it needs exactly one document store and the link key).
run "origination_gets_the_document_configuration" {
  command = plan

  variables {
    origination_environment = { RETRY_WINDOW_SECONDS = "300", CHARGE_RETRY_MAX_MS = "15000" }
  }

  # Known at plan time, so the assertions can read them.
  override_resource {
    target          = module.apigateway.aws_api_gateway_stage.this
    override_during = plan
    values = {
      invoke_url = "https://abc123.execute-api.us-east-2.amazonaws.com/staging"
    }
  }

  override_resource {
    target          = module.secrets.aws_secretsmanager_secret.this
    override_during = plan
    values = {
      arn = "arn:aws:secretsmanager:us-east-2:123456789012:secret:solventa-test/secret-AbCdEf"
    }
  }

  assert {
    condition = (
      local.service_environment.origination.DOCUMENTS_BUCKET == "solventa-test-documents-123456789012" &&
      local.service_environment.origination.AWS_REGION == "us-east-2" &&
      !contains(keys(local.service_environment.origination), "DOCUMENTS_DIR")
    )
    error_message = "Origination gets the documents bucket and the region, and never a local directory."
  }

  assert {
    condition     = local.service_environment.origination.DOCUMENTS_PUBLIC_BASE_URL == "https://abc123.execute-api.us-east-2.amazonaws.com/staging"
    error_message = "The links of the e-mail point to the API Gateway stage (its invoke URL)."
  }

  assert {
    condition = (
      endswith(local.service_secrets.origination.DOCUMENT_LINK_SECRET, ":DOCUMENT_LINK_SECRET::") &&
      endswith(local.service_secrets.origination.DATABASE_URL, ":DATABASE_URL::") &&
      !contains(keys(local.service_environment.origination), "DOCUMENT_LINK_SECRET")
    )
    error_message = "The link key reaches origination from Secrets Manager, never as a plain environment variable."
  }

  assert {
    condition     = !contains(keys(local.service_secrets.claims), "DOCUMENT_LINK_SECRET")
    error_message = "Only origination reads the key of the document links."
  }

  assert {
    condition = (
      local.service_environment.origination.RETRY_WINDOW_SECONDS == "300" &&
      local.service_environment.origination.CHARGE_RETRY_MAX_MS == "15000"
    )
    error_message = "origination_environment adds the variables of a test window."
  }
}

run "origination_environment_cannot_override_platform_values" {
  command = plan

  variables {
    origination_environment = { PAYMENT_GATEWAY_URL = "http://elsewhere:9040" }
  }

  expect_failures = [var.origination_environment]
}

run "origination_environment_cannot_replace_the_document_store" {
  command = plan

  variables {
    origination_environment = { DOCUMENTS_DIR = "/tmp/documents" }
  }

  expect_failures = [var.origination_environment]
}

# HU-W1.1-02 and HA-D02: the advisor inbox and the issuance exceptions reach
# origination through the ALB and the Cognito authorizer.
run "inbox_and_exceptions_are_routed" {
  command = plan

  assert {
    condition = (
      contains(local.services.origination.paths, "/v1/review-requests*") &&
      contains(local.services.origination.paths, "/v1/issuance-exceptions*") &&
      length(chunklist(local.services.origination.paths, 5)) == 2
    )
    error_message = "Origination owns the inbox and the exceptions in the ALB (9 prefixes, 2 listener rules)."
  }

  assert {
    condition = (
      toset(var.human_routes["review-requests"].methods) == toset(["GET", "POST"]) &&
      toset(var.human_routes["issuance-exceptions"].methods) == toset(["GET"]) &&
      contains(var.human_routes["acceptances"].methods, "POST")
    )
    error_message = "The inbox (GET and POST), the exceptions (GET) and the charge actions (POST under acceptances) are human routes."
  }
}

# HA-D02: the signed link of the e-mail opens the document without a session.
run "the_document_link_is_public" {
  command = plan

  assert {
    condition     = module.apigateway.public_paths == ["/v1/documents", "/v1/documents/{proxy+}"]
    error_message = "GET /v1/documents/{token} is public: the signed, expiring token protects it."
  }

  assert {
    condition     = var.public_routes["documents"].rate_limit == 10 && var.public_routes["documents"].burst_limit == 20
    error_message = "The public route is throttled at the stage (decision D-5: 10 req/s, burst 20)."
  }

  assert {
    condition     = !contains(keys(var.human_routes), "documents")
    error_message = "The document link is not a human route."
  }
}

run "seed_users_are_optional" {
  command = plan

  assert {
    condition     = length(var.seed_users) == 0 && var.seed_user_password == null
    error_message = "The platform creates no test users unless a staging window asks for them."
  }
}

run "stub_overrides_name_real_stubs" {
  command = plan

  variables {
    stub_desired_count_overrides = { pasarela = 0 }
  }

  expect_failures = [var.stub_desired_count_overrides]
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
