output "bucket_name" {
  description = "Name of the remote state bucket (the bucket argument of every backend \"s3\" block)."
  value       = aws_s3_bucket.this.bucket
}

output "bucket_arn" {
  description = "ARN of the remote state bucket."
  value       = aws_s3_bucket.this.arn
}

output "region" {
  description = "Region of the remote state bucket (the region argument of every backend \"s3\" block)."
  value       = var.region
}
