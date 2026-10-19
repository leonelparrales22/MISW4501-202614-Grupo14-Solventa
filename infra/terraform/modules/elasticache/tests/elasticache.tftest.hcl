# Configuration tests of the Redis cache (T4). They run with a mocked AWS
# provider: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {}

variables {
  name                       = "solventa-test"
  vpc_id                     = "vpc-12345678"
  subnet_ids                 = ["subnet-aaaa1111", "subnet-bbbb2222"]
  allowed_security_group_ids = { tasks = "sg-tasks111" }
}

run "one_encrypted_node_by_default" {
  command = plan

  assert {
    condition     = aws_elasticache_replication_group.this.num_cache_clusters == 1 && aws_elasticache_replication_group.this.automatic_failover_enabled == false
    error_message = "A single node without failover by default (cost)."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.at_rest_encryption_enabled == "true"
    error_message = "The cache must be encrypted at rest."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.engine == "redis" && aws_elasticache_replication_group.this.node_type == "cache.t4g.micro"
    error_message = "Redis on cache.t4g.micro by default."
  }
}

run "multi_az_adds_a_replica_with_failover" {
  command = plan

  variables {
    multi_az = true
  }

  assert {
    condition     = aws_elasticache_replication_group.this.num_cache_clusters == 2 && aws_elasticache_replication_group.this.automatic_failover_enabled && aws_elasticache_replication_group.this.multi_az_enabled
    error_message = "multi_az must add a replica in the other zone with automatic failover."
  }
}

run "redis_only_from_the_tasks" {
  command = plan

  assert {
    condition     = length(aws_vpc_security_group_ingress_rule.redis) == 1 && aws_vpc_security_group_ingress_rule.redis["tasks"].from_port == 6379
    error_message = "Only the task security group may reach port 6379."
  }
}
