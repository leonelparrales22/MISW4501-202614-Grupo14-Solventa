# Production-demo platform (T4): the evaluable delivery. Sized as ADR-02 (2
# tasks per service, autoscaling on CPU at 60 %) except origination, which
# keeps one task for now (see below), with
# deletion protection, a final RDS snapshot and 7 days of backups. NAT and
# Multi-AZ stay off by default for cost and are switched on for the final demo
# (nat_mode = "per_az", rds_multi_az = true, redis_multi_az = true).
# User pool: the permanent one of bootstrap/identity.

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

  name        = "solventa-demo"
  environment = "production-demo"
  vpc_cidr    = "10.60.0.0/16"
  nat_mode    = var.nat_mode

  image_tag           = var.image_tag
  image_tag_overrides = var.image_tag_overrides
  services_enabled    = var.services_enabled

  service_sizing = {
    # EXP-L01: 0.5 vCPU / 1 GB. Temporary deviation from ADR-02 (3 tasks,
    # decided in HA-D02, #54): one task until acceptances, offers and the
    # advisor inbox are persisted (C5, HU-W1.2-03); back to 3 tasks with
    # autoscaling up to 6 afterwards.
    origination   = { cpu = 512, memory = 1024, desired_count = 1, max_count = 1 }
    claims        = { cpu = 256, memory = 512, desired_count = 2, max_count = 4 }
    partners      = { cpu = 256, memory = 512, desired_count = 2, max_count = 4 }
    audit         = { cpu = 256, memory = 512, desired_count = 2, max_count = 4 }
    notifications = { cpu = 256, memory = 512, desired_count = 2, max_count = 4 }
  }
  log_retention_days = 14

  rds_multi_az              = var.rds_multi_az
  rds_backup_retention_days = 7
  redis_multi_az            = var.redis_multi_az

  cors_allow_origin = var.cors_allow_origin

  existing_cognito = {
    user_pool_id  = data.terraform_remote_state.identity.outputs.user_pool_id
    user_pool_arn = data.terraform_remote_state.identity.outputs.user_pool_arn
    issuer        = data.terraform_remote_state.identity.outputs.issuer
    jwks_url      = data.terraform_remote_state.identity.outputs.jwks_url
    client_ids    = data.terraform_remote_state.identity.outputs.client_ids
  }

  deletion_protection = true
  ephemeral           = false
}
