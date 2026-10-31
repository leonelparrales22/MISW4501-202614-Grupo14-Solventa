output "bucket" {
  description = "Name of the bucket."
  value       = aws_s3_bucket.this.bucket
}

output "arn" {
  description = "ARN of the bucket."
  value       = aws_s3_bucket.this.arn
}

output "regional_domain_name" {
  description = "Regional domain name of the bucket (origin of a CloudFront distribution)."
  value       = aws_s3_bucket.this.bucket_regional_domain_name
}

output "cors_allowed_origins" {
  description = "Origins allowed to upload to and read from the bucket with signed URLs (empty: no CORS)."
  value       = var.cors_allowed_origins
}
