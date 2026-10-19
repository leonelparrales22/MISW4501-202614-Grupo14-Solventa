# Permanent user pool of the human channels (T4): the portal web
# and the mobile app sign in against the same pool and app clients in every
# environment, so their IDs never change between staging windows.
#
# It is the user pool of modules/cognito (HA-S01) with its four role groups,
# the web and mobile clients and one permanent test user per role. Deletion
# protection stays on; the pool is never destroyed from here.
#
# Applied once and permanent, like bootstrap/remote-state and bootstrap/cd-ecr.
# environments/production-demo/platform and environments/staging/platform read
# its outputs with terraform_remote_state and pass them to modules/platform
# (existing_cognito), so neither creates a pool of its own.

locals {
  account_id = "368083581678"

  # Shared and permanent resource: no Story and no TTL tags.
  default_tags = {
    Project = "solventa"
    Owner   = "nicolas"
    Env     = "shared"
    Task    = "T4"
    Purpose = "identity"
  }

  # Permanent test users, one per role group. The addresses are fictitious
  # (@example.com); socio-a is the default partner of the usage plans
  # (modules/platform). They share one password, set only through
  # TF_VAR_seed_user_password.
  test_users = {
    insured    = { email = "asegurado@example.com", group = "insured" }
    advisor    = { email = "asesor@example.com", group = "advisor" }
    operations = { email = "operaciones@example.com", group = "operations" }
    partner    = { email = "socio@example.com", group = "partner", partner_id = "socio-a" }
  }
}

module "cognito" {
  source = "../../modules/cognito"

  name                = "solventa-users"
  deletion_protection = true

  seed_users         = local.test_users
  seed_user_password = var.seed_user_password
}
