output "bucket" {
  description = "Name of the bucket."
  value       = aws_s3_bucket.this.bucket
}

output "arn" {
  description = "ARN of the bucket."
  value       = aws_s3_bucket.this.arn
}
