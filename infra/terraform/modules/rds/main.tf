# PostgreSQL instance of an environment (T4), shared by the services with one
# database and one login role per service: each service only connects to its
# own database (database per service).
#
# - The administrator password is managed by RDS in Secrets Manager and never
#   reaches the Terraform state. Only the database bootstrap task reads it.
# - One random password per service; the caller stores it, with the
#   DATABASE_URL, in that service's secret (modules/secrets).
# - multi_az is a variable: false by default for cost, true for the final demo
#   or a fault tolerance test. Switching it is an online modification (RDS
#   builds the standby from a snapshot) and keeps the data.

resource "aws_db_subnet_group" "this" {
  name       = var.name
  subnet_ids = var.subnet_ids
  tags       = var.tags
}

resource "aws_security_group" "this" {
  name        = "${var.name}-rds"
  description = "PostgreSQL, reachable only from the Fargate tasks"
  vpc_id      = var.vpc_id
  tags        = merge(var.tags, { Name = "${var.name}-rds" })
}

resource "aws_vpc_security_group_ingress_rule" "postgres" {
  for_each = var.allowed_security_group_ids

  security_group_id            = aws_security_group.this.id
  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}

resource "aws_db_instance" "this" {
  identifier     = var.name
  engine         = "postgres"
  engine_version = var.engine_version
  instance_class = var.instance_class

  allocated_storage     = var.allocated_storage
  max_allocated_storage = var.max_allocated_storage
  storage_type          = "gp3"
  storage_encrypted     = true

  db_name                     = "postgres"
  username                    = var.master_username
  manage_master_user_password = true

  multi_az               = var.multi_az
  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.this.id]
  publicly_accessible    = false
  port                   = 5432

  backup_retention_period    = var.backup_retention_days
  auto_minor_version_upgrade = true
  apply_immediately          = var.apply_immediately
  copy_tags_to_snapshot      = true
  deletion_protection        = var.deletion_protection
  skip_final_snapshot        = var.skip_final_snapshot
  final_snapshot_identifier  = var.skip_final_snapshot ? null : "${var.name}-final"

  tags = var.tags
}

# One login role per service; the bootstrap task creates it with this password.
resource "random_password" "database" {
  for_each = var.databases

  length  = 32
  special = false # goes inside the DATABASE_URL without URL encoding
}
