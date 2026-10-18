output "url" {
  description = "HTTPS origin of the portal of production-demo."
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
  description = "Always null: production-demo has no publishing role (the main pipeline publishes it, #68)."
  value       = module.web.cd_role_arn
}
