output "invoke_url" {
  description = "Base URL of the API Gateway stage."
  value       = module.platform.invoke_url
}

output "api_key_ids" {
  description = "API key ID per partner."
  value       = module.platform.api_key_ids
}

output "api_key_values" {
  description = "API key value per partner."
  value       = module.platform.api_key_values
  sensitive   = true
}

output "cognito" {
  description = "User pool of the human channels and the AUTH_* values of the services."
  value       = module.platform.cognito
}

output "ecs_cluster" {
  description = "ECS cluster."
  value       = module.platform.ecs_cluster
}

output "log_group" {
  description = "CloudWatch log group of the tasks."
  value       = module.platform.log_group
}

output "images" {
  description = "Image deployed for each service and stub."
  value       = module.platform.images
}

output "rds_address" {
  description = "Host of PostgreSQL."
  value       = module.platform.rds_address
}

output "redis_endpoint" {
  description = "Primary endpoint of Redis."
  value       = module.platform.redis_endpoint
}

output "database_secret_arns" {
  description = "Secret with the credentials and DATABASE_URL of each service."
  value       = module.platform.database_secret_arns
}

output "topic_arns" {
  description = "SNS topic of each domain."
  value       = module.platform.topic_arns
}

output "queue_urls" {
  description = "SQS queue of each consumer."
  value       = module.platform.queue_urls
}

output "dlq_urls" {
  description = "Dead-letter queue of each consumer."
  value       = module.platform.dlq_urls
}

output "buckets" {
  description = "S3 buckets."
  value       = module.platform.buckets
}

output "db_init_command" {
  description = "Command that creates one database and one login role per service; run it after the first apply."
  value       = module.platform.db_init_command
}
