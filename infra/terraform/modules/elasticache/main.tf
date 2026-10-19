# ElastiCache Redis of an environment (T4): risk profile cache of origination
# and, later, the claim status read cache (EC-L02).
#
# - multi_az follows the same criterion as RDS and the NAT: false by default
#   (one node), true adds a replica in the second zone with automatic failover.
# - Encrypted at rest. Not in transit: the origination client
#   (services/origination/cmd/origination, go-redis with only Addr) speaks
#   plain TCP and no password. Turning TLS and an AUTH token on needs that
#   client to support them first. The security group only admits the tasks.

resource "aws_elasticache_subnet_group" "this" {
  name       = var.name
  subnet_ids = var.subnet_ids
  tags       = var.tags
}

resource "aws_security_group" "this" {
  name        = "${var.name}-redis"
  description = "Redis, reachable only from the Fargate tasks"
  vpc_id      = var.vpc_id
  tags        = merge(var.tags, { Name = "${var.name}-redis" })
}

resource "aws_vpc_security_group_ingress_rule" "redis" {
  for_each = var.allowed_security_group_ids

  security_group_id            = aws_security_group.this.id
  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}

resource "aws_elasticache_replication_group" "this" {
  replication_group_id = var.name
  description          = "Redis of ${var.name}"

  engine               = "redis"
  engine_version       = var.engine_version
  node_type            = var.node_type
  port                 = 6379
  parameter_group_name = var.parameter_group_name

  num_cache_clusters         = var.multi_az ? 2 : 1
  automatic_failover_enabled = var.multi_az
  multi_az_enabled           = var.multi_az

  subnet_group_name  = aws_elasticache_subnet_group.this.name
  security_group_ids = [aws_security_group.this.id]

  at_rest_encryption_enabled = true
  transit_encryption_enabled = false

  snapshot_retention_limit   = var.snapshot_retention_days
  apply_immediately          = true
  auto_minor_version_upgrade = true

  tags = var.tags
}
