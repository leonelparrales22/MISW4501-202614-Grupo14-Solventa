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

  # T5 adds the demonstration partner of the web portal (portal-embebido, its
  # own run below); the partners of D-7 keep 100 req/s with a burst of 200.
  assert {
    condition     = length(module.apigateway.usage_plan_ids) == 3 && alltrue([for k in ["socio-a", "socio-b"] : var.partners[k].rate_limit == 100 && var.partners[k].burst_limit == 200])
    error_message = "Each partner gets its own usage plan of 100 req/s with a burst of 200 (decision D-7)."
  }

  assert {
    condition     = length(module.apigateway.api_key_ids) == 3
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
    condition     = length(data.aws_ecr_image.latest) == 11 && strcontains(local.image_uris["audit"], "@sha256:")
    error_message = "Without image_tag every image is the most recent one, pinned by digest."
  }
}

run "services_can_be_switched_off" {
  command = plan

  variables {
    services_enabled = false
  }

  assert {
    condition     = length(module.ecs.desired_counts) == 11 && alltrue([for c in values(module.ecs.desired_counts) : c == 0])
    error_message = "The five services and the six stubs keep their ECS services, at 0 tasks."
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

# HU-M2.2-01: the mobile app files the claim through the gateway.
run "the_claim_filing_is_a_human_route" {
  command = plan

  assert {
    condition = (
      toset(var.human_routes["claims"].methods) == toset(["GET", "POST"]) &&
      var.human_routes["claims"].root_methods == null
    )
    error_message = "GET and POST /v1/claims are human routes: the insured files and reads on their own behalf (HU-M2.2-01, HU-W2.1-01)."
  }

  assert {
    condition     = !contains(keys(var.public_routes), "claims")
    error_message = "The filing needs a session: it is never a public route."
  }

  assert {
    condition     = contains(local.services.claims.paths, "/v1/claims*")
    error_message = "The ALB sends /v1/claims* to the claims service."
  }

  # Without the user pool the service does not mount the filing, so the
  # published route would answer 404 to everybody. The values come from the
  # pool and are only known after it exists, so what is checked here is that
  # the three of them reach the service.
  assert {
    condition = alltrue([
      for v in ["AUTH_ISSUER", "AUTH_JWKS_URL", "AUTH_CLIENT_IDS"] :
      contains(keys(local.service_environment.claims), v)
    ])
    error_message = "Claims verifies the token of the insured: it needs the user pool of the environment."
  }
}

# HU-W2.1-01: the web filing asks the policy core for the policies of the
# insured. The core is a stub that only claims calls inside the VPC.
run "the_policy_core_runs_behind_cloud_map_for_claims" {
  command = plan

  variables {
    image_tag_overrides = { policy-core = "prueba-hu-w2-1" }
  }

  assert {
    condition     = local.stubs["policy-core"] == 9060 && module.ecs.desired_counts["policy-core"] == 1
    error_message = "The policy core stub runs one task on port 9060."
  }

  assert {
    condition     = local.service_environment.claims.POLICY_CORE_URL == "http://policy-core.solventa-test.local:9060"
    error_message = "Claims reaches the policy core by its Cloud Map name; without it the web filing is not mounted."
  }

  assert {
    condition = (local.service_environment.claims.PAYMENT_GATEWAY_URL == "http://payment-gateway.solventa-test.local:9040"
    && local.service_environment.claims.EMAIL_URL == "http://email.solventa-test.local:9030")
    error_message = "Claims pays the indemnities through the payment gateway and notifies the rejections by e-mail (HU-W2.2-03)."
  }

  assert {
    condition     = !contains(keys(local.lb_stubs), "policy-core") && !contains(keys(module.alb.target_group_arns), "policy-core")
    error_message = "The browser never calls the policy core: it has no rule in the ALB."
  }

  assert {
    condition     = endswith(local.image_uris["policy-core"], ":prueba-hu-w2-1")
    error_message = "The policy core image accepts a tag override like the others."
  }
}

# HU-M1.1-01: the public route of the sign-up also needs its rule in the load
# balancer; the API Gateway alone does not know where to send it.
run "the_sign_up_reaches_origination" {
  command = plan

  assert {
    condition     = contains(local.services.origination.paths, "/v1/registrations*")
    error_message = "The ALB sends /v1/registrations* to origination: without the rule it answers 404."
  }

  assert {
    condition     = module.apigateway.registration_path == "/v1/registrations"
    error_message = "The gateway publishes the sign-up on the same path the balancer routes."
  }
}

# HU-M2.1-01: the evidence of the claim goes to the evidence bucket, and the
# service signs the upload with its own task role.
run "the_claim_evidence_has_its_bucket" {
  command = plan

  assert {
    condition     = local.service_environment.claims.EVIDENCE_BUCKET == module.buckets["evidence"].bucket
    error_message = "Claims keeps the evidence in the evidence bucket of the environment."
  }

  assert {
    condition     = local.services.claims.buckets == ["evidence"]
    error_message = "Claims reaches its own bucket and no other."
  }

  assert {
    condition     = local.service_environment.claims.AWS_REGION == data.aws_region.current.region
    error_message = "The AWS SDK of claims takes the region of the environment."
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

# T4 (permanent user pool): without existing_cognito the platform creates its
# own user pool, as before.
run "creates_its_own_user_pool_by_default" {
  command = plan

  assert {
    condition     = var.existing_cognito == null && length(module.cognito) == 1
    error_message = "Without existing_cognito the platform creates its own user pool."
  }
}

# T4: with the permanent user pool of bootstrap/identity the platform creates
# none and wires the received one into the authorizer, AUTH_* and the output.
run "uses_an_existing_user_pool" {
  command = plan

  variables {
    existing_cognito = {
      user_pool_id  = "us-east-2_PermTest1"
      user_pool_arn = "arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_PermTest1"
      issuer        = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1"
      jwks_url      = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1/.well-known/jwks.json"
      client_ids    = { web = "web-client-test", mobile = "mobile-client-test" }
    }
  }

  assert {
    condition     = length(module.cognito) == 0
    error_message = "With existing_cognito the platform must not create a user pool."
  }

  assert {
    condition     = local.cognito.user_pool_arn == "arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_PermTest1"
    error_message = "The Cognito authorizer must use the received user pool."
  }

  assert {
    condition = (
      local.service_environment.origination.AUTH_ISSUER == "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1" &&
      local.service_environment.origination.AUTH_JWKS_URL == "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1/.well-known/jwks.json" &&
      local.service_environment.origination.AUTH_CLIENT_IDS == "mobile-client-test,web-client-test"
    )
    error_message = "AUTH_ISSUER, AUTH_JWKS_URL and AUTH_CLIENT_IDS of the services come from the received user pool."
  }

  assert {
    condition     = output.cognito.user_pool_id == "us-east-2_PermTest1" && output.cognito.client_ids["web"] == "web-client-test"
    error_message = "The cognito output describes the received user pool."
  }
}

run "existing_pool_rejects_seed_users" {
  command = plan

  variables {
    existing_cognito = {
      user_pool_id  = "us-east-2_PermTest1"
      user_pool_arn = "arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_PermTest1"
      issuer        = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1"
      jwks_url      = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1/.well-known/jwks.json"
      client_ids    = { web = "web-client-test", mobile = "mobile-client-test" }
    }
    seed_users = {
      advisor = { email = "asesor.prueba@example.com", group = "advisor" }
    }
  }

  expect_failures = [var.existing_cognito]
}

run "existing_pool_must_be_a_cognito_pool" {
  command = plan

  variables {
    existing_cognito = {
      user_pool_id  = "us-east-2_PermTest1"
      user_pool_arn = "arn:aws:iam::123456789012:role/us-east-2_PermTest1"
      issuer        = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1"
      jwks_url      = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1/.well-known/jwks.json"
      client_ids    = { web = "web-client-test", mobile = "mobile-client-test" }
    }
  }

  expect_failures = [var.existing_cognito]
}

# --- Web portal (T5) ------------------------------------------------------------------

# Decision D-4 of T5: the browser tokenizes the card through the API Gateway, so
# the payment gateway stub is also behind the ALB, only on /tokens.
run "payment_gateway_tokens_behind_the_alb" {
  command = plan

  assert {
    condition = (
      toset(keys(module.alb.target_group_arns)) == toset(["origination", "claims", "partners", "audit", "notifications", "payment-gateway"]) &&
      local.lb_stubs["payment-gateway"].paths == ["/tokens"] &&
      local.lb_stubs["payment-gateway"].priority == 6
    )
    error_message = "The payment gateway stub gets its own target group, routed only on /tokens."
  }

  assert {
    condition     = length(local.lb_stubs) == 1
    error_message = "No other stub is reachable through the ALB."
  }

  assert {
    condition     = module.apigateway.payment_tokenization_path == "/pasarela/tokens"
    error_message = "The platform publishes the public card tokenization."
  }

  assert {
    condition     = local.service_environment.origination.PAYMENT_GATEWAY_URL == "http://payment-gateway.solventa-test.local:9040"
    error_message = "Origination keeps charging through Cloud Map."
  }
}

# Decision D-3 of T5: CloudFront sends the key of a demonstration partner with
# its own usage plan, never the key of a real partner.
run "portal_partner_has_its_own_usage_plan" {
  command = plan

  assert {
    condition = (
      contains(keys(module.apigateway.usage_plan_ids), "portal-embebido") &&
      var.partners["portal-embebido"].rate_limit == 10 &&
      var.partners["portal-embebido"].burst_limit == 20 &&
      var.portal_partner == "portal-embebido"
    )
    error_message = "portal-embebido has its own API key and usage plan (10 req/s, burst 20)."
  }
}

run "portal_partner_must_be_a_partner" {
  command = plan

  variables {
    portal_partner = "socio-z"
  }

  expect_failures = [output.portal_partner_api_key]
}

run "api_origin_matches_the_stage" {
  command = plan

  override_resource {
    target          = module.apigateway.aws_api_gateway_rest_api.this
    override_during = plan
    values = {
      id = "abc123"
    }
  }

  override_resource {
    target          = module.apigateway.aws_api_gateway_stage.this
    override_during = plan
    values = {
      invoke_url = "https://abc123.execute-api.us-east-2.amazonaws.com/staging"
    }
  }

  assert {
    condition = (
      output.api_origin.domain == "abc123.execute-api.us-east-2.amazonaws.com" &&
      output.api_origin.stage == "staging" &&
      output.api_origin.invoke_url == "https://${output.api_origin.domain}/${output.api_origin.stage}"
    )
    error_message = "The API origin of the portal is the REST API domain with the stage as origin path."
  }
}

# Decision D-11 of T5: with the portal, both e-mail links open through it.
run "portal_links_follow_portal_base_url" {
  command = plan

  variables {
    portal_base_url = "https://d1234abcd.cloudfront.net"
  }

  assert {
    condition = (
      local.service_environment.origination.OFFER_LINK_BASE_URL == "https://d1234abcd.cloudfront.net" &&
      local.service_environment.origination.DOCUMENTS_PUBLIC_BASE_URL == "https://d1234abcd.cloudfront.net"
    )
    error_message = "With portal_base_url, the offer and document links of the e-mails point to the portal."
  }
}

run "without_portal_the_links_stay_as_before" {
  command = plan

  override_resource {
    target          = module.apigateway.aws_api_gateway_stage.this
    override_during = plan
    values = {
      invoke_url = "https://abc123.execute-api.us-east-2.amazonaws.com/staging"
    }
  }

  assert {
    condition = (
      !contains(keys(local.service_environment.origination), "OFFER_LINK_BASE_URL") &&
      local.service_environment.origination.DOCUMENTS_PUBLIC_BASE_URL == "https://abc123.execute-api.us-east-2.amazonaws.com/staging"
    )
    error_message = "Without a portal, origination keeps its default offer link and the document link on the stage."
  }
}

# HU-W2.1-01: the browser of the portal uploads the supports of a claim to the
# evidence bucket, so only that bucket accepts the origin of the portal.
run "the_portal_may_upload_to_the_evidence_bucket_only" {
  command = plan

  variables {
    portal_base_url = "https://d1234abcd.cloudfront.net"
  }

  assert {
    condition = (
      length(module.buckets["evidence"].cors_allowed_origins) == 1 &&
      module.buckets["evidence"].cors_allowed_origins[0] == "https://d1234abcd.cloudfront.net" &&
      length(module.buckets["documents"].cors_allowed_origins) == 0
    )
    error_message = "The evidence bucket allows the origin of the portal; the documents bucket allows none."
  }
}

run "without_a_portal_the_evidence_bucket_has_no_cors" {
  command = plan

  assert {
    condition     = length(module.buckets["evidence"].cors_allowed_origins) == 0
    error_message = "Without portal_base_url there is no origin to allow."
  }
}

run "portal_base_url_is_an_https_origin" {
  command = plan

  variables {
    portal_base_url = "https://d1234abcd.cloudfront.net/"
  }

  expect_failures = [var.portal_base_url]
}

# --- Consent and personalized offer as partner routes (T5, #70) -----------------------

run "audit_partner_routes_are_published_with_api_key" {
  command = plan

  assert {
    condition = (
      tolist(module.apigateway.partner_paths) == tolist(["/v1/audit/consent-terms/current", "/v1/audit/consents", "/v1/offers"]) &&
      var.partner_routes["audit/consent-terms/current"].methods == tolist(["GET"]) &&
      var.partner_routes["audit/consents"].methods == tolist(["POST"]) &&
      var.partner_routes["offers"].methods == tolist(["POST"])
    )
    error_message = "The terms and the consent of HU-W1.2-01 and POST /v1/offers of HU-W1.2-03 are partner routes with API key."
  }
}

run "offers_root_is_partner_and_sub_paths_are_human" {
  command = plan

  assert {
    condition = (
      length(var.human_routes["offers"].root_methods) == 0 &&
      toset(var.human_routes["offers"].methods) == toset(["GET", "POST"]) &&
      contains(module.apigateway.partner_paths, "/v1/offers")
    )
    error_message = "The root /v1/offers belongs to the partner; /v1/offers/{offer_id}, renewal and acceptance keep Cognito."
  }

  # consents sets its root to GET only (HU-M1.3-02); it has its own test.
  assert {
    condition     = alltrue([for k, r in var.human_routes : r.root_methods == null if !contains(["offers", "consents"], k)])
    error_message = "Every other human route keeps its root as before."
  }
}

run "consent_verifications_is_not_published" {
  command = plan

  assert {
    condition = (
      !contains(module.apigateway.partner_paths, "/v1/audit/consent-verifications") &&
      !contains(keys(var.human_routes), "audit") &&
      !contains(keys(var.public_routes), "audit")
    )
    error_message = "GET /v1/audit/consent-verifications is internal to the VPC: no partner, human or public route publishes it."
  }
}

run "consent_verifications_cannot_become_a_partner_route" {
  command = plan

  variables {
    partner_routes = { "audit/consent-verifications" = { methods = ["GET"] } }
  }

  expect_failures = [var.partner_routes]
}

# --- Origination reaches audit through Cloud Map (T5, #70, decisions D-21 a and D-23 c) ---

run "audit_is_discoverable_by_origination" {
  command = plan

  assert {
    condition = (
      local.discoverable_services == ["audit"] &&
      contains(keys(local.services), "audit")
    )
    error_message = "audit is registered in Cloud Map (and stays behind the ALB); no other service is."
  }

  assert {
    condition = (
      local.service_environment.origination.AUDIT_URL == "http://audit.solventa-test.local:8080" &&
      output.audit_url == local.service_environment.origination.AUDIT_URL
    )
    error_message = "Origination verifies the consent with audit at http://audit.<name>.local:8080, inside the VPC (HU-W1.2-01)."
  }

  assert {
    condition     = !contains(keys(local.service_environment.origination), "AUDIT_TIMEOUT_MS")
    error_message = "AUDIT_TIMEOUT_MS keeps the default of origination (300 ms)."
  }

  assert {
    condition     = !contains(keys(var.human_routes), "audit")
    error_message = "D-23 (c): audit is never a human route as a whole; the evidence lookup and the revocation of the compliance portal (HU-W4.2-02) are published only through human_subroutes."
  }
}

# --- HU-W4.2-02: the compliance portal reaches the evidence and the revocation of audit ---

run "the_compliance_portal_reaches_only_the_consent_paths_of_audit" {
  command = plan

  assert {
    condition = (
      length(var.human_subroutes) == 1 &&
      var.human_subroutes["audit/consents"].methods == tolist(["GET", "POST"]) &&
      var.human_subroutes["audit/consents"].root_methods == tolist(["GET"]) &&
      contains(keys(var.partner_routes), "audit/consents") &&
      !contains(var.partner_routes["audit/consents"].methods, "GET")
    )
    error_message = "GET /v1/audit/consents (the list, HU-W4.2-01), GET /v1/audit/consents/{consent_id}, GET .../revocation and POST .../revocations and .../revocation-retries are published with Cognito; the partner keeps only its POST."
  }

  assert {
    condition = (
      length(module.apigateway.partner_paths) == 3 &&
      !contains(keys(var.human_routes), "audit") &&
      !contains(keys(var.public_routes), "audit") &&
      !contains(module.apigateway.partner_paths, "/v1/audit/consent-verifications") &&
      !contains(module.apigateway.partner_paths, "/v1/audit/decisions")
    )
    error_message = "The consent verification and the decision record of audit (no token) are in no route of the gateway."
  }
}

run "the_internal_routes_of_audit_cannot_become_human_subroutes" {
  command = plan

  variables {
    human_subroutes = { "audit/decisions" = { methods = ["POST"] } }
  }

  expect_failures = [var.human_subroutes]
}

run "only_origination_creates_accounts_in_the_user_pool" {
  command = plan

  variables {
    existing_cognito = {
      user_pool_id  = "us-east-2_PermTest1"
      user_pool_arn = "arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_PermTest1"
      issuer        = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1"
      jwks_url      = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1/.well-known/jwks.json"
      client_ids    = { web = "web-client-test", mobile = "mobile-client-test" }
    }
  }

  assert {
    condition = anytrue([
      for st in local.task_role_statements.origination : (
        st.sid == "CreateInsuredAccounts" &&
        length(st.actions) == 3 &&
        contains(st.actions, "cognito-idp:AdminCreateUser") &&
        contains(st.actions, "cognito-idp:AdminSetUserPassword") &&
        contains(st.actions, "cognito-idp:AdminAddUserToGroup") &&
        st.resources == ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_PermTest1"]
      )
    ])
    error_message = "Origination can only create the account, set its password and add it to its group, on that user pool (HU-M1.1-01)."
  }

  assert {
    condition = alltrue([
      for k, sts in local.task_role_statements : k == "origination" || alltrue([
        for st in sts : st.sid != "CreateInsuredAccounts"
      ])
    ])
    error_message = "No other service touches the user pool."
  }

  assert {
    condition     = local.service_environment.origination.COGNITO_USER_POOL_ID == "us-east-2_PermTest1"
    error_message = "Origination receives the user pool it creates the accounts in."
  }
}

run "origination_environment_cannot_override_the_user_pool" {
  command = plan

  variables {
    origination_environment = { COGNITO_USER_POOL_ID = "us-east-2_Otro" }
  }

  expect_failures = [var.origination_environment]
}

run "origination_environment_cannot_override_audit_url" {
  command = plan

  variables {
    origination_environment = { AUDIT_URL = "http://elsewhere:8080" }
  }

  expect_failures = [var.origination_environment]
}

# --- HU-M1.3-01: the insured reads their consents; origination and audit talk by events ---

run "consents_are_a_human_route_of_audit" {
  command = plan

  # HU-M1.3-01 made it read-only; HU-M1.3-02 adds POST under the consent for
  # the revocation, and the root /v1/consents stays read-only.
  assert {
    condition = (
      toset(var.human_routes["consents"].methods) == toset(["GET", "POST"]) &&
      toset(var.human_routes["consents"].root_methods) == toset(["GET"]) &&
      !contains(keys(var.partner_routes), "consents") &&
      !contains(keys(var.public_routes), "consents")
    )
    error_message = "GET /v1/consents* and POST /v1/consents/{consent_id}/revocations are human routes behind the Cognito authorizer; the root /v1/consents is GET only."
  }

  assert {
    condition = (
      contains(local.services.audit.paths, "/v1/consents*") &&
      contains(local.services.audit.paths, "/v1/audit*") &&
      !contains(local.services.origination.paths, "/v1/consents*")
    )
    error_message = "The ALB sends /v1/consents* to audit, next to /v1/audit* (decision D-3 a)."
  }
}

run "audit_gets_the_queue_the_user_pool_and_the_catalog" {
  command = plan

  # The permanent user pool, so its values are known at plan time.
  variables {
    existing_cognito = {
      user_pool_id  = "us-east-2_PermTest1"
      user_pool_arn = "arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_PermTest1"
      issuer        = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1"
      jwks_url      = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1/.well-known/jwks.json"
      client_ids    = { web = "web-client-test", mobile = "mobile-client-test" }
    }
  }

  override_resource {
    target          = module.messaging.aws_sqs_queue.this
    override_during = plan
    values = {
      url = "https://sqs.us-east-2.amazonaws.com/123456789012/solventa-test-audit"
      arn = "arn:aws:sqs:us-east-2:123456789012:solventa-test-audit"
    }
  }

  assert {
    condition = (
      local.service_environment.audit.PORT == "8080" &&
      local.service_environment.audit.AWS_REGION == "us-east-2" &&
      local.service_environment.audit.AUTH_ISSUER == "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1" &&
      local.service_environment.audit.AUTH_JWKS_URL == "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1/.well-known/jwks.json" &&
      local.service_environment.audit.AUTH_CLIENT_IDS == "mobile-client-test,web-client-test" &&
      local.service_environment.audit.AUTH_CLIENT_IDS == local.service_environment.origination.AUTH_CLIENT_IDS
    )
    error_message = "audit verifies the Cognito token of the insured with the same user pool and clients as origination."
  }

  assert {
    condition     = jsondecode(local.service_environment.audit.PARTNER_DISPLAY_NAMES) == { "aliado-banco-1" = "Banco Aliado" }
    error_message = "audit gets the catalog of entity names as JSON (decision D-2 a)."
  }

  assert {
    condition     = local.service_environment.audit.EVENTS_QUEUE_URL == module.messaging.queue_urls["audit"]
    error_message = "audit consumes CustomerLinked from its own queue."
  }

  assert {
    condition     = !contains(keys(local.service_environment.audit), "DATABASE_URL")
    error_message = "The database URL of audit stays in Secrets Manager."
  }
}

run "origination_publishes_to_its_topic" {
  command = plan

  override_resource {
    target          = module.messaging.aws_sns_topic.this
    override_during = plan
    values = {
      arn = "arn:aws:sns:us-east-2:123456789012:solventa-test-origination"
    }
  }

  assert {
    condition     = local.service_environment.origination.EVENTS_TOPIC_ARN == module.messaging.topic_arns["origination"]
    error_message = "The outbox relay of origination publishes to the origination topic (HU-M1.3-01)."
  }
}

run "partner_display_names_can_be_replaced" {
  command = plan

  variables {
    partner_display_names = { "aliado-banco-1" = "Banco Aliado", "socio-a" = "Socio A" }
  }

  assert {
    condition     = jsondecode(local.service_environment.audit.PARTNER_DISPLAY_NAMES)["socio-a"] == "Socio A"
    error_message = "The catalog follows partner_display_names."
  }
}

run "partner_display_names_rejects_invalid_codes" {
  command = plan

  variables {
    partner_display_names = { "aliado banco" = "Banco" }
  }

  expect_failures = [var.partner_display_names]
}

run "origination_environment_cannot_replace_the_events_topic" {
  command = plan

  variables {
    origination_environment = { EVENTS_TOPIC_ARN = "arn:aws:sns:us-east-2:123456789012:elsewhere" }
  }

  expect_failures = [var.origination_environment]
}

# --- HU-M1.3-02: the insured revokes a consent; audit notifies the entities ---

run "audit_notifies_the_entities_through_the_open_finance_stub" {
  command = plan

  assert {
    condition = (
      local.service_environment.audit.OPEN_FINANCE_URL == "http://openfinance.${local.namespace}:9000" &&
      local.service_environment.audit.OPEN_FINANCE_URL == local.service_environment.origination.OPEN_FINANCE_URL
    )
    error_message = "audit reaches the Open Finance stub by Cloud Map, at the same address as origination (decision D-2 a)."
  }

  assert {
    condition     = local.service_environment.audit.REVOCATION_NOTICE_DEADLINE_SECONDS == "300"
    error_message = "Every entity has 5 minutes to confirm a revocation (RN-02)."
  }

  assert {
    condition     = contains(keys(local.stubs), "openfinance")
    error_message = "The Open Finance stub keeps running behind Cloud Map."
  }
}

# --- HU-W3.1-01: credentials of the commercial partners ---------------------------------

run "partners_manages_the_credentials_and_publishes_to_audit" {
  command = plan

  variables {
    existing_cognito = {
      user_pool_id  = "us-east-2_PermTest1"
      user_pool_arn = "arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_PermTest1"
      issuer        = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1"
      jwks_url      = "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1/.well-known/jwks.json"
      client_ids    = { web = "web-client-test", mobile = "mobile-client-test" }
    }
  }

  assert {
    condition     = contains(local.topics, "partners") && contains(local.queues.audit.topics, "partners")
    error_message = "The credential events of partners reach the audit queue (CA-3)."
  }

  assert {
    condition = (
      local.service_environment.partners.AUTH_ISSUER == "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1" &&
      local.service_environment.partners.CREDENTIAL_GRACE_PERIOD_HOURS == "24" &&
      contains(keys(local.service_environment.partners), "GATEWAY_REST_API_ID") &&
      contains(keys(local.service_environment.partners), "EVENTS_TOPIC_ARN")
    )
    error_message = "partners receives the user pool, the gateway where it creates the usage plans and its topic."
  }

  assert {
    condition = alltrue([
      for k, sts in local.task_role_statements : k == "partners" || alltrue([
        for st in sts : !contains(["ManageThePartnerApiKeys", "ManageThePartnerUsagePlans", "ReadTheStageOfThePartnerPlans"], st.sid)
      ])
    ])
    error_message = "Only partners manages API keys and usage plans."
  }

  assert {
    condition     = contains(keys(var.human_routes), "partners")
    error_message = "The operations portal reaches /v1/partners with Cognito."
  }
}
