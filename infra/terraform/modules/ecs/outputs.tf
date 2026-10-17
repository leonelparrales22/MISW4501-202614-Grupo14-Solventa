output "cluster_name" {
  description = "Name of the ECS cluster."
  value       = aws_ecs_cluster.this.name
}

output "cluster_arn" {
  description = "ARN of the ECS cluster."
  value       = aws_ecs_cluster.this.arn
}

output "tasks_security_group_id" {
  description = "Security group of every task (source allowed by RDS and ElastiCache)."
  value       = aws_security_group.tasks.id
}

output "log_group_name" {
  description = "CloudWatch log group of the cluster (one stream prefix per container)."
  value       = aws_cloudwatch_log_group.this.name
}

output "service_names" {
  description = "ECS service name of each service."
  value       = { for k, s in aws_ecs_service.this : k => s.name }
}

output "desired_counts" {
  description = "Configured task count of each service (autoscaling may move it up to max_count)."
  value       = { for k, s in aws_ecs_service.this : k => s.desired_count }
}

output "task_definition_families" {
  description = "Task definition family of each one-off task."
  value       = { for k, t in aws_ecs_task_definition.task : k => t.family }
}

output "execution_role_arn" {
  description = "Execution role shared by every task (image pull, logs and the referenced secrets)."
  value       = aws_iam_role.execution.arn
}

output "task_role_arns" {
  description = "Task role ARN of each service with AWS permissions."
  value       = { for k, r in aws_iam_role.task : k => r.arn }
}
