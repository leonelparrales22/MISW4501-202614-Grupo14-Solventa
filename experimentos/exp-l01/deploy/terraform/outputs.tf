output "ecr_url" {
  value = aws_ecr_repository.exp.repository_url
}

output "cluster" {
  value = aws_ecs_cluster.exp.name
}

output "subred" {
  value = aws_subnet.publica.id
}

output "grupo_seguridad" {
  value = aws_security_group.exp.id
}

output "k6_task_definition" {
  value = aws_ecs_task_definition.k6.family
}

output "redis" {
  value = "${aws_elasticache_cluster.redis.cache_nodes[0].address}:6379"
}

output "log_group_k6" {
  value = aws_cloudwatch_log_group.k6.name
}

output "config_actual" {
  description = "Con qué parámetros están corriendo los servicios ahora"
  value = {
    n_fuentes  = var.n_fuentes
    corte_dep  = var.corte_dep_ms
    deadline   = var.deadline_ms
    lat_p50    = var.lat_p50_ms
    lat_p95    = var.lat_p95_ms
    orq_cpu    = var.orq_cpu
  }
}
