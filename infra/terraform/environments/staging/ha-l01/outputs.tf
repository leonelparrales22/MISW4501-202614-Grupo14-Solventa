output "invoke_url" {
  description = "Base URL of the API Gateway stage (BASE_URL of the k6 scripts)."
  value       = module.apigateway.invoke_url
}

output "api_key_ids" {
  description = "API key ID per test partner (values in the sensitive output api_key_values)."
  value       = module.apigateway.api_key_ids
}

output "api_key_values" {
  description = "Test API keys of the window's partners."
  value       = module.apigateway.api_key_values
  sensitive   = true
}

output "ecr_repositories" {
  description = "ECR repository URL per image; push the images with the image_tag before enabling the services."
  value       = { for k, r in aws_ecr_repository.this : k => r.repository_url }
}

output "image_tag" {
  description = "Tag the services expect in every repository."
  value       = var.image_tag
}

output "ecs_cluster" {
  description = "ECS cluster of the window."
  value       = aws_ecs_cluster.this.name
}

output "log_group" {
  description = "CloudWatch log group (1-day retention) with the service logs and the k6 summaries."
  value       = aws_cloudwatch_log_group.this.name
}

output "internal_alb_dns" {
  description = "DNS name of the internal ALB (reachable only through the VPC link)."
  value       = aws_lb.internal.dns_name
}
