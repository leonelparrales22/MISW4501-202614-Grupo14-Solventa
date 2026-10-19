output "url" {
  description = "HTTPS origin of the portal of staging."
  value       = module.web.url
}

output "distribution_id" {
  description = "ID of the CloudFront distribution."
  value       = module.web.distribution_id
}

output "bucket" {
  description = "Private site bucket."
  value       = module.web.bucket
}

output "config_etag" {
  description = "ETag of the config.json written by Terraform (null with api_enabled = false)."
  value       = module.web.config_etag
}

output "ssm_parameters" {
  description = "SSM parameters with the bucket, the distribution ID and the URL."
  value       = module.web.ssm_parameters
}

output "cd_role_arn" {
  description = "Role assumed by cd-portal.yml (GitHub OIDC) to publish staging."
  value       = module.web.cd_role_arn
}
