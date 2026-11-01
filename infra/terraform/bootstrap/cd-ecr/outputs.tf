output "role_arn" {
  description = "ARN of the role the backend-cd workflow assumes (role-to-assume)."
  value       = aws_iam_role.cd.arn
}

output "deploy_role_arn" {
  description = "ARN of the role the deploy job of backend-cd assumes (environment production-demo)."
  value       = aws_iam_role.deploy.arn
}

output "image_tag_parameter" {
  description = "SSM parameter with the release tag of main, read by scripts/environment/up.sh."
  value       = aws_ssm_parameter.production_demo_image_tag.name
}

output "oidc_provider_arn" {
  description = "ARN of the GitHub OIDC provider trusted by the role."
  value       = local.oidc_provider_arn
}

output "repository_urls" {
  description = "ECR repository URL of each image, by image name."
  value       = module.ecr.repository_urls
}

output "staging_deploy_role_arn" {
  description = "ARN of the role the deploy job of backend-cd assumes (environment staging)."
  value       = aws_iam_role.deploy_staging.arn
}
