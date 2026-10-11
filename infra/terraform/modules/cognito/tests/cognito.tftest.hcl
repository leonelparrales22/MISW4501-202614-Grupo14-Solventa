# Configuration tests of the Cognito module (HA-S01, CA-3 and D-6). They run
# with a mocked AWS provider: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {}

variables {
  name = "solventa-test-ha-s01"
}

run "only_administrators_create_users" {
  command = plan

  assert {
    condition     = aws_cognito_user_pool.this.admin_create_user_config[0].allow_admin_create_user_only == true
    error_message = "Self sign-up must be disabled: only an administrator creates users (CA-3)."
  }

  assert {
    condition     = aws_cognito_user_pool.this.username_attributes == toset(["email"])
    error_message = "Users must sign in with their email."
  }

  assert {
    condition     = aws_cognito_user_pool.this.deletion_protection == "ACTIVE"
    error_message = "Deletion protection must be on by default."
  }

  assert {
    condition     = aws_cognito_user_pool.this.user_pool_tier == "ESSENTIALS"
    error_message = "The user pool must use the Essentials plan (architecture)."
  }

  assert {
    condition = anytrue([
      for s in aws_cognito_user_pool.this.schema : s.name == "partner_id" && s.attribute_data_type == "String" && !s.developer_only_attribute
    ])
    error_message = "The pool must define the custom:partner_id attribute (D-3b)."
  }
}

run "one_group_per_role" {
  command = plan

  assert {
    condition     = toset(keys(aws_cognito_user_group.this)) == toset(["insured", "advisor", "operations", "partner"])
    error_message = "The pool must have exactly the four role groups of pkg/security (D-3)."
  }

  assert {
    condition     = alltrue([for k, g in aws_cognito_user_group.this : g.name == k && g.precedence != null])
    error_message = "Each group must be named after its role and have a precedence."
  }

  assert {
    condition     = length(distinct([for g in aws_cognito_user_group.this : g.precedence])) == 4
    error_message = "Group precedences must be unique."
  }
}

run "public_clients_cannot_change_roles_or_ownership" {
  command = plan

  assert {
    condition     = toset(keys(aws_cognito_user_pool_client.this)) == toset(["web", "mobile"])
    error_message = "There must be one web client and one mobile client."
  }

  assert {
    condition     = alltrue([for c in aws_cognito_user_pool_client.this : c.generate_secret == false])
    error_message = "Browser and mobile clients are public: they must not have a secret."
  }

  assert {
    condition     = alltrue([for c in aws_cognito_user_pool_client.this : c.enable_token_revocation == true])
    error_message = "Token revocation must be enabled (sign-out, CA-4)."
  }

  assert {
    condition     = alltrue([for c in aws_cognito_user_pool_client.this : c.prevent_user_existence_errors == "ENABLED"])
    error_message = "Sign-in errors must not reveal whether a user exists."
  }

  assert {
    condition = alltrue([
      for c in aws_cognito_user_pool_client.this : !contains(c.write_attributes, "custom:partner_id") && !contains(c.write_attributes, "email")
    ])
    error_message = "No client may write custom:partner_id or the sign-in email (CA-3, D-3b)."
  }

  assert {
    condition     = alltrue([for c in aws_cognito_user_pool_client.this : length(c.write_attributes) > 0])
    error_message = "write_attributes must be explicit: an empty list lets users write every attribute."
  }

  assert {
    condition     = alltrue([for c in aws_cognito_user_pool_client.this : contains(c.read_attributes, "custom:partner_id")])
    error_message = "custom:partner_id must be readable so it reaches the ID token (D-3b)."
  }

  assert {
    condition = alltrue([
      for c in aws_cognito_user_pool_client.this :
      c.explicit_auth_flows == toset(["ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"])
    ])
    error_message = "The clients must allow only USER_PASSWORD_AUTH and REFRESH_TOKEN_AUTH (D-5)."
  }
}

run "short_lived_tokens" {
  command = plan

  assert {
    condition = alltrue([
      for c in aws_cognito_user_pool_client.this :
      c.id_token_validity == 5 && c.access_token_validity == 5 &&
      c.token_validity_units[0].id_token == "minutes" && c.token_validity_units[0].access_token == "minutes"
    ])
    error_message = "ID and access tokens must last 5 minutes (D-6)."
  }

  assert {
    condition = (
      aws_cognito_user_pool_client.this["web"].refresh_token_validity == 12 &&
      aws_cognito_user_pool_client.this["web"].token_validity_units[0].refresh_token == "hours"
    )
    error_message = "The web refresh token must last 12 hours (D-6)."
  }

  assert {
    condition = (
      aws_cognito_user_pool_client.this["mobile"].refresh_token_validity == 30 &&
      aws_cognito_user_pool_client.this["mobile"].token_validity_units[0].refresh_token == "days"
    )
    error_message = "The mobile refresh token must last 30 days (D-6)."
  }
}

run "staging_pool_can_be_destroyed" {
  command = plan

  variables {
    deletion_protection = false
  }

  assert {
    condition     = aws_cognito_user_pool.this.deletion_protection == "INACTIVE"
    error_message = "deletion_protection = false must turn protection off."
  }
}

run "seed_users_get_one_group_and_partner_ownership" {
  command = plan

  variables {
    seed_user_password = "mock-value-not-a-password"
    seed_users = {
      advisor = { email = "advisor@example.com", group = "advisor" }
      partner = { email = "partner@example.com", group = "partner", partner_id = "partner-a" }
    }
  }

  assert {
    condition     = aws_cognito_user_in_group.seed["advisor"].group_name == "advisor"
    error_message = "The advisor seed user must be in the advisor group."
  }

  assert {
    condition     = aws_cognito_user_in_group.seed["partner"].group_name == "partner"
    error_message = "The partner seed user must be in the partner group."
  }

  assert {
    condition     = aws_cognito_user.seed["partner"].attributes["partner_id"] == "partner-a"
    error_message = "The partner seed user must carry custom:partner_id."
  }

  assert {
    condition     = !contains(keys(aws_cognito_user.seed["advisor"].attributes), "partner_id")
    error_message = "Only partner users carry custom:partner_id."
  }

  assert {
    condition     = alltrue([for u in aws_cognito_user.seed : u.message_action == "SUPPRESS"])
    error_message = "Seed users must not receive an invitation (permanent password, D-13)."
  }
}

run "partner_user_without_partner_id_is_rejected" {
  command = plan

  variables {
    seed_user_password = "mock-value-not-a-password"
    seed_users = {
      partner = { email = "partner@example.com", group = "partner" }
    }
  }

  expect_failures = [var.seed_users]
}

run "unknown_group_is_rejected" {
  command = plan

  variables {
    seed_user_password = "mock-value-not-a-password"
    seed_users = {
      admin = { email = "admin@example.com", group = "admin" }
    }
  }

  expect_failures = [var.seed_users]
}

run "seed_users_need_a_password" {
  command = plan

  variables {
    seed_users = {
      advisor = { email = "advisor@example.com", group = "advisor" }
    }
  }

  expect_failures = [aws_cognito_user.seed]
}

run "tokens_shorter_than_cognito_minimum_are_rejected" {
  command = plan

  variables {
    token_validity_minutes = 1
  }

  expect_failures = [var.token_validity_minutes]
}

run "admin_password_flow_is_rejected_on_public_clients" {
  command = plan

  variables {
    explicit_auth_flows = ["ALLOW_ADMIN_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"]
  }

  expect_failures = [var.explicit_auth_flows]
}
