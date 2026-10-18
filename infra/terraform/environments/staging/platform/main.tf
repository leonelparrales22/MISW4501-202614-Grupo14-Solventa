# Staging platform (T4): the whole platform of modules/platform, sized for
# experiments and destroyed after each one (ephemeral). One task per service,
# no deletion protection, single-AZ data stores and no NAT by default.

module "platform" {
  source = "../../../modules/platform"

  name        = "solventa-stg"
  environment = "staging"
  vpc_cidr    = "10.50.0.0/16"
  nat_mode    = var.nat_mode

  image_tag           = var.image_tag
  image_tag_overrides = var.image_tag_overrides
  services_enabled    = var.services_enabled

  # Levers of a test window (HA-D02): take a stub down, tune origination and
  # create test users. Empty by default.
  stub_desired_count_overrides = var.stub_desired_count_overrides
  origination_environment      = var.origination_environment
  seed_users                   = var.seed_users
  seed_user_password           = var.seed_user_password

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

  deletion_protection = false
  ephemeral           = true
}
