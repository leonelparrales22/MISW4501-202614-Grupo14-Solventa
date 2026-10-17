# Production-demo platform (T4): the evaluable delivery. Sized as ADR-02 (2
# tasks per service, 3 in origination, autoscaling on CPU at 60 %), with
# deletion protection, a final RDS snapshot and 7 days of backups. NAT and
# Multi-AZ stay off by default for cost and are switched on for the final demo
# (nat_mode = "per_az", rds_multi_az = true, redis_multi_az = true).

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
    origination   = { cpu = 512, memory = 1024, desired_count = 3, max_count = 6 } # EXP-L01: 0.5 vCPU / 1 GB
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

  deletion_protection = true
  ephemeral           = false
}
