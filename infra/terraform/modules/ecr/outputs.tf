output "repository_names" {
  description = "Repository name of each image."
  value       = { for image, r in aws_ecr_repository.this : image => r.name }
}

output "repository_urls" {
  description = "Repository URL of each image."
  value       = { for image, r in aws_ecr_repository.this : image => r.repository_url }
}

output "repository_arns" {
  description = "Repository ARN of each image."
  value       = { for image, r in aws_ecr_repository.this : image => r.arn }
}
