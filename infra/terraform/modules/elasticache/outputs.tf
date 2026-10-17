output "primary_endpoint" {
  description = "Primary endpoint of the replication group (host:port, REDIS_ADDR of the services)."
  value       = "${aws_elasticache_replication_group.this.primary_endpoint_address}:${aws_elasticache_replication_group.this.port}"
}

output "security_group_id" {
  description = "Security group of the cache."
  value       = aws_security_group.this.id
}
