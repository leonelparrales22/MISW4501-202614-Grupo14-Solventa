# Configuration tests of the permanent user pool (T4). They run with a mocked
# AWS provider: no credentials, no network, nothing is created. Every run uses
# command = plan, so there is nothing to clean up (the pool keeps its deletion
# protection on).
#
#   terraform init -backend=false && terraform test
#
# Admin-only sign-up, the Essentials plan, the public clients and the deletion
# protection default are asserted by the tests of modules/cognito.

mock_provider "aws" {}

# A mock value, never a real password (the real one only travels in
# TF_VAR_seed_user_password).
variables {
  seed_user_password = "mock-value-not-a-password"
}

run "tags_identify_a_permanent_shared_resource" {
  command = plan

  assert {
    condition = local.default_tags == {
      Project = "solventa"
      Owner   = "nicolas"
      Env     = "shared"
      Task    = "T4"
      Purpose = "identity"
    }
    error_message = "The default tags must be exactly Project, Owner, Env = shared, Task = T4 and Purpose = identity."
  }

  assert {
    condition     = !contains(keys(local.default_tags), "TTL") && !contains(keys(local.default_tags), "Story")
    error_message = "The user pool is permanent and shared: it must not carry TTL or Story tags."
  }
}

run "other_regions_are_rejected" {
  command = plan

  variables {
    region = "us-east-1"
  }

  expect_failures = [var.region]
}

run "pool_is_the_shared_identity_pool" {
  command = plan

  # Computed by AWS; known at plan time so the outputs can be asserted.
  override_resource {
    target          = module.cognito.aws_cognito_user_pool.this
    override_during = plan
    values = {
      id       = "us-east-2_PermTest1"
      arn      = "arn:aws:cognito-idp:us-east-2:368083581678:userpool/us-east-2_PermTest1"
      endpoint = "cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1"
    }
  }

  override_resource {
    target          = module.cognito.aws_cognito_user_pool_client.this
    override_during = plan
    values = {
      id = "client-test"
    }
  }

  assert {
    condition     = output.group_names == tolist(["advisor", "insured", "operations", "partner"])
    error_message = "The pool must have exactly the four role groups of pkg/security."
  }

  assert {
    condition     = toset(keys(output.client_ids)) == toset(["web", "mobile"])
    error_message = "The pool must have one web client and one mobile client."
  }

  assert {
    condition     = output.user_pool_id == "us-east-2_PermTest1" && output.user_pool_arn == "arn:aws:cognito-idp:us-east-2:368083581678:userpool/us-east-2_PermTest1"
    error_message = "The pool ID and ARN outputs must come from the user pool of modules/cognito."
  }

  assert {
    condition     = output.issuer == "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_PermTest1"
    error_message = "The issuer is https:// plus the endpoint of the user pool."
  }

  assert {
    condition     = output.jwks_url == "${output.issuer}/.well-known/jwks.json"
    error_message = "The JWKS URL is the one of the issuer."
  }

  assert {
    condition     = output.region == "us-east-2"
    error_message = "The pool lives in us-east-2."
  }
}

run "one_test_user_per_role" {
  command = plan

  assert {
    condition     = length(local.test_users) == 4
    error_message = "There must be exactly four test users."
  }

  assert {
    condition     = toset([for u in values(local.test_users) : u.group]) == toset(["insured", "advisor", "operations", "partner"])
    error_message = "Each test user belongs to a different role group, and every group has one."
  }
}

run "partner_user_carries_its_partner_id" {
  command = plan

  assert {
    condition     = local.test_users["partner"].partner_id == "socio-a"
    error_message = "The partner test user belongs to socio-a, the default partner of the usage plans."
  }

  assert {
    condition     = alltrue([for k, u in local.test_users : lookup(u, "partner_id", null) == null if u.group != "partner"])
    error_message = "Only the partner test user carries a partner_id."
  }
}

run "outputs_list_the_test_users" {
  command = plan

  assert {
    condition = output.test_users == {
      insured    = "asegurado@example.com"
      advisor    = "asesor@example.com"
      operations = "operaciones@example.com"
      partner    = "socio@example.com"
    }
    error_message = "The test_users output maps each role group to the e-mail of its test user."
  }
}

run "test_users_are_fictitious" {
  command = plan

  assert {
    condition     = alltrue([for u in values(local.test_users) : endswith(u.email, "@example.com")])
    error_message = "Test users must use the reserved domain example.com."
  }

  assert {
    condition     = length(distinct([for u in values(local.test_users) : lower(u.email)])) == 4
    error_message = "Each test user needs its own e-mail (the user pool signs in by e-mail, case-insensitive)."
  }
}

run "short_password_is_rejected" {
  command = plan

  variables {
    seed_user_password = "Short-1a"
  }

  expect_failures = [var.seed_user_password]
}
