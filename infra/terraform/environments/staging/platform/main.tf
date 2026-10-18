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
  portal_base_url   = local.portal_base_url

  deletion_protection = false
  ephemeral           = true
}

# --- Web portal (T5) ----------------------------------------------------------------

# The portal composition (environments/staging/web) publishes its URL in SSM. The
# platform reads it when it exists, so the e-mail links of origination point to
# the portal (decision D-11 of T5).
# Without the site the lists are empty and nothing changes. The values are
# plain String parameters (bucket, distribution id and URL), never secrets.
data "aws_ssm_parameters_by_path" "portal" {
  path = "/solventa/staging/web"
}

locals {
  portal_parameters = zipmap(
    data.aws_ssm_parameters_by_path.portal.names,
    nonsensitive(data.aws_ssm_parameters_by_path.portal.values),
  )
  portal_base_url = var.portal_base_url != null ? var.portal_base_url : lookup(local.portal_parameters, "/solventa/staging/web/url", null)
}
