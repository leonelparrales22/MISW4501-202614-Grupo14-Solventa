# Redis administrado, un nodo, la talla más chica. Sin réplica ni Multi-AZ:
# es la caché del experimento, no la de producción.

resource "aws_elasticache_subnet_group" "exp" {
  name       = "${local.nombre}-redis-subredes"
  subnet_ids = [aws_subnet.publica.id]
}

resource "aws_elasticache_cluster" "redis" {
  cluster_id           = "${local.nombre}-redis"
  engine               = "redis"
  engine_version       = "7.1"
  node_type            = "cache.t4g.micro"
  num_cache_nodes      = 1
  parameter_group_name = "default.redis7"
  port                 = 6379
  subnet_group_name    = aws_elasticache_subnet_group.exp.name
  security_group_ids   = [aws_security_group.exp.id]

  # Que no guarde snapshots: al destruir no queremos que quede nada.
  snapshot_retention_limit = 0
}
