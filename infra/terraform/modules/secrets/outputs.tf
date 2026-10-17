output "arns" {
  description = "ARN of each secret, by key."
  value       = { for k, s in aws_secretsmanager_secret.this : k => s.arn }
}

output "names" {
  description = "Name of each secret, by key."
  value       = { for k, s in aws_secretsmanager_secret.this : k => s.name }
}
