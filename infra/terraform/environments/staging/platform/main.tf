# Staging platform (T4): the whole platform of modules/platform, sized for
# experiments and destroyed after each one (ephemeral). One task per service,
# no deletion protection, single-AZ data stores and no NAT by default.
# User pool: the permanent one of bootstrap/identity (destroy never touches it).

# Permanent user pool of the human channels (T4, bootstrap/identity): applied once,
# outside this composition, so the portal and the app keep the same pool and client IDs.
data "terraform_remote_state" "identity" {
  backend = "s3"
  config = {
    bucket = "solventa-tfstate-368083581678"
    key    = "bootstrap/identity/terraform.tfstate"
    region = "us-east-2"
  }
}

module "platform" {
  source = "../../../modules/platform"

  name        = "solventa-stg"
  environment = "staging"
  vpc_cidr    = "10.50.0.0/16"
  nat_mode    = var.nat_mode

  image_tag           = var.image_tag
  image_tag_overrides = var.image_tag_overrides
  services_enabled    = var.services_enabled

  # Levers of a test window (HA-D02): take a stub down and tune origination.
  # Empty by default.
  stub_desired_count_overrides = var.stub_desired_count_overrides
  origination_environment      = var.origination_environment

  service_sizing = {
    # EXP-L01: 0.5 vCPU / 1 GB. One task until acceptances, offers and the
    # advisor inbox are persisted (C5, HU-W1.2-03): with two tasks an
    # acceptance kept in the memory of one would not exist in the other.
    origination   = { cpu = 512, memory = 1024, desired_count = 1, max_count = 1 }
    claims        = { cpu = 256, memory = 512, desired_count = 1, max_count = 2 }
    partners      = { cpu = 256, memory = 512, desired_count = 1, max_count = 2 }
    audit         = { cpu = 256, memory = 512, desired_count = 1, max_count = 2 }
    notifications = { cpu = 256, memory = 512, desired_count = 1, max_count = 2 }
  }
  log_retention_days = 3

  rds_multi_az              = var.rds_multi_az
  rds_backup_retention_days = 1
  redis_multi_az            = var.redis_multi_az

  cors_allow_origin = var.cors_allow_origin

  existing_cognito = {
    user_pool_id  = data.terraform_remote_state.identity.outputs.user_pool_id
    user_pool_arn = data.terraform_remote_state.identity.outputs.user_pool_arn
    issuer        = data.terraform_remote_state.identity.outputs.issuer
    jwks_url      = data.terraform_remote_state.identity.outputs.jwks_url
    client_ids    = data.terraform_remote_state.identity.outputs.client_ids
  }

  deletion_protection = false
  ephemeral           = true
}
