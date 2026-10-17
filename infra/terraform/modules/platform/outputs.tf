output "invoke_url" {
  description = "Base URL of the API Gateway stage."
  value       = module.apigateway.invoke_url
}

output "api_key_ids" {
  description = "API key ID per partner (values in the sensitive output api_key_values)."
  value       = module.apigateway.api_key_ids
}

output "api_key_values" {
  description = "API key value per partner."
  value       = module.apigateway.api_key_values
  sensitive   = true
}

output "usage_plan_ids" {
  description = "Usage plan ID per partner."
  value       = module.apigateway.usage_plan_ids
}

output "cognito" {
  description = "User pool of the human channels and the AUTH_* values of the services."
  value = {
    user_pool_id = module.cognito.user_pool_id
    issuer       = module.cognito.issuer
    jwks_url     = module.cognito.jwks_url
    client_ids   = module.cognito.client_ids
  }
}

output "alb_dns_name" {
  description = "Internal DNS name of the ALB (reachable only through the VPC link)."
  value       = module.alb.dns_name
}

output "ecs_cluster" {
  description = "ECS cluster."
  value       = module.ecs.cluster_name
}

output "log_group" {
  description = "CloudWatch log group of the tasks."
  value       = module.ecs.log_group_name
}

output "desired_counts" {
  description = "Configured task count of each service and stub."
  value       = module.ecs.desired_counts
}

output "database_users" {
  description = "Database and login role of each service."
  value       = module.rds.databases
}

output "images" {
  description = "Image deployed for each service and stub."
  value       = local.image_uris
}

output "rds_address" {
  description = "Host of PostgreSQL."
  value       = module.rds.address
}

output "redis_endpoint" {
  description = "Primary endpoint of Redis (host:port)."
  value       = module.redis.primary_endpoint
}

output "database_secret_arns" {
  description = "Secret with the credentials and DATABASE_URL of each service."
  value       = { for s in keys(local.services) : s => module.secrets.arns["${s}/database"] }
}

output "topic_arns" {
  description = "SNS topic of each domain."
  value       = module.messaging.topic_arns
}

output "queue_urls" {
  description = "SQS queue of each consumer."
  value       = module.messaging.queue_urls
}

output "dlq_urls" {
  description = "Dead-letter queue of each consumer."
  value       = module.messaging.dlq_urls
}

output "buckets" {
  description = "S3 bucket names (documents, evidence)."
  value       = { for k, b in module.buckets : k => b.bucket }
}

output "nat_gateway_ids" {
  description = "NAT Gateways of the current nat_mode."
  value       = module.network.nat_gateway_ids
}

output "db_init_command" {
  description = "Command that runs the database bootstrap task (creates one database and one login role per service). Run it after the first apply and after a password change."
  value = join(" ", [
    "aws ecs run-task",
    "--cluster ${module.ecs.cluster_name}",
    "--task-definition ${module.ecs.task_definition_families["db-init"]}",
    "--launch-type FARGATE",
    "--network-configuration 'awsvpcConfiguration={subnets=[${join(",", module.network.app_subnet_ids)}],securityGroups=[${module.ecs.tasks_security_group_id}],assignPublicIp=${module.network.app_assign_public_ip ? "ENABLED" : "DISABLED"}}'",
  ])
}
