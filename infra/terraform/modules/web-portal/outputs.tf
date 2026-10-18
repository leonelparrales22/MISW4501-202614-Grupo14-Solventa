output "url" {
  description = "HTTPS origin of the portal (https://<distribution>.cloudfront.net)."
  value       = local.url
}

output "domain_name" {
  description = "Domain name of the distribution."
  value       = aws_cloudfront_distribution.this.domain_name
}

output "distribution_id" {
  description = "ID of the distribution (for invalidations)."
  value       = aws_cloudfront_distribution.this.id
}

output "bucket" {
  description = "Name of the private site bucket."
  value       = module.bucket.bucket
}

output "config_etag" {
  description = "ETag of the config.json written by Terraform (null without api and cognito); the smoke test compares it with the published object."
  value       = one(aws_s3_object.config[*].etag)
}

output "ssm_parameters" {
  description = "SSM parameters with the bucket, the distribution ID and the URL."
  value       = { for k, p in aws_ssm_parameter.site : k => p.name }
}

output "cd_role_arn" {
  description = "ARN of the publishing role of the front-end workflow (null without cd)."
  value       = one(aws_iam_role.cd[*].arn)
}
