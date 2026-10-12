output "role_arn" {
  description = "ARN of the role the backend-cd workflow assumes (role-to-assume)."
  value       = aws_iam_role.cd.arn
}

output "oidc_provider_arn" {
  description = "ARN of the GitHub OIDC provider trusted by the role."
  value       = local.oidc_provider_arn
}

output "repository_urls" {
  description = "ECR repository URL of each image, by image name."
  value       = { for name, r in aws_ecr_repository.this : name => r.repository_url }
}
