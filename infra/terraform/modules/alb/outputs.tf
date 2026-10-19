output "arn" {
  description = "ARN of the ALB (integration_target_arn of the API Gateway module)."
  value       = aws_lb.this.arn
}

output "dns_name" {
  description = "Internal DNS name of the ALB (backend_base_url of the API Gateway module)."
  value       = aws_lb.this.dns_name
}

output "security_group_id" {
  description = "Security group of the ALB (source allowed by the task security group)."
  value       = aws_security_group.this.id
}

output "listener_arn" {
  description = "ARN of the HTTP listener."
  value       = aws_lb_listener.http.arn
}

output "target_group_arns" {
  description = "Target group ARN of each service."
  value       = { for k, tg in aws_lb_target_group.this : k => tg.arn }
}
