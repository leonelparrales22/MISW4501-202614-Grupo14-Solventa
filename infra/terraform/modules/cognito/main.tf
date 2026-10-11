# Cognito user pool of the human channels (HA-S01): insured, advisor,
# operations analyst and partner portal user, on the web and mobile clients.
#
# - Only an administrator creates users (no self sign-up, CA-3).
# - One group per role; the group names are the roles of pkg/security
#   (cognito:groups claim, decision D-3).
# - custom:partner_id carries the partner ownership (D-3b). The app clients
#   can read it but never write it, so nobody changes their own ownership.
# - Public clients (no secret) with token revocation and short-lived tokens
#   (D-6). Partner machine-to-machine integrations never use this pool (CA-5).

locals {
  # Role groups (decision D-3). Precedence only matters if a user ever ends
  # up in two groups, which provisioning must not allow.
  groups = {
    operations = { precedence = 10, description = "Operations analysts" }
    advisor    = { precedence = 20, description = "Sales advisors" }
    partner    = { precedence = 30, description = "Partner portal users (with custom:partner_id)" }
    insured    = { precedence = 40, description = "Insured customers (provisioned after KYC, HU-M1.1-01)" }
  }

  clients = {
    web = {
      refresh_token_validity = var.web_refresh_token_validity_hours
      refresh_token_unit     = "hours"
    }
    mobile = {
      refresh_token_validity = var.mobile_refresh_token_validity_days
      refresh_token_unit     = "days"
    }
  }

  # Attributes the app clients may read (they reach the ID token) and write
  # (UpdateUserAttributes with the user's own access token). custom:partner_id
  # and the email used to sign in are never writable by the user.
  read_attributes  = ["email", "email_verified", "name", "custom:partner_id"]
  write_attributes = ["name"]
}

resource "aws_cognito_user_pool" "this" {
  name                = var.name
  user_pool_tier      = var.user_pool_tier
  deletion_protection = var.deletion_protection ? "ACTIVE" : "INACTIVE"
  mfa_configuration   = "OFF"
  tags                = var.tags

  username_attributes      = ["email"]
  auto_verified_attributes = ["email"]

  username_configuration {
    case_sensitive = false
  }

  admin_create_user_config {
    allow_admin_create_user_only = true
  }

  password_policy {
    minimum_length                   = var.password_minimum_length
    require_lowercase                = true
    require_uppercase                = true
    require_numbers                  = true
    require_symbols                  = true
    temporary_password_validity_days = 7
  }

  account_recovery_setting {
    recovery_mechanism {
      name     = "verified_email"
      priority = 1
    }
  }

  schema {
    name                     = "partner_id"
    attribute_data_type      = "String"
    mutable                  = true
    developer_only_attribute = false
    required                 = false

    string_attribute_constraints {
      min_length = "1"
      max_length = "64"
    }
  }
}

resource "aws_cognito_user_group" "this" {
  for_each = local.groups

  user_pool_id = aws_cognito_user_pool.this.id
  name         = each.key
  precedence   = each.value.precedence
  description  = each.value.description
}

resource "aws_cognito_user_pool_client" "this" {
  for_each = local.clients

  name         = "${var.name}-${each.key}"
  user_pool_id = aws_cognito_user_pool.this.id

  generate_secret               = false
  enable_token_revocation       = true
  prevent_user_existence_errors = "ENABLED"
  explicit_auth_flows           = var.explicit_auth_flows

  id_token_validity      = var.token_validity_minutes
  access_token_validity  = var.token_validity_minutes
  refresh_token_validity = each.value.refresh_token_validity

  token_validity_units {
    id_token      = "minutes"
    access_token  = "minutes"
    refresh_token = each.value.refresh_token_unit
  }

  read_attributes  = local.read_attributes
  write_attributes = local.write_attributes
}

# --- Optional seed users (staging windows only, decision D-13) -----------------

resource "aws_cognito_user" "seed" {
  for_each = var.seed_users

  user_pool_id   = aws_cognito_user_pool.this.id
  username       = each.value.email
  password       = var.seed_user_password
  message_action = "SUPPRESS"

  # Custom attributes go without the "custom:" prefix; the provider adds it.
  attributes = merge(
    {
      email          = each.value.email
      email_verified = "true"
    },
    each.value.partner_id == null ? {} : { partner_id = each.value.partner_id },
  )

  lifecycle {
    precondition {
      condition     = var.seed_user_password != null
      error_message = "Seed users need seed_user_password (set TF_VAR_seed_user_password; never commit it)."
    }
  }
}

resource "aws_cognito_user_in_group" "seed" {
  for_each = var.seed_users

  user_pool_id = aws_cognito_user_pool.this.id
  group_name   = aws_cognito_user_group.this[each.value.group].name
  username     = aws_cognito_user.seed[each.key].username
}
