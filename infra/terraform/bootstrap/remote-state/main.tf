# Remote state of Terraform for every Solventa composition (task T4).
#
# One S3 bucket, shared by all environments, that stores each composition's
# state under <environment>/<composition>/terraform.tfstate and locks it with
# S3 native locking (use_lockfile = true, no DynamoDB table).
#
# It is applied once and is permanent: prevent_destroy blocks any plan that
# would delete the bucket, and it must not be destroyed by hand either, since
# it holds the state of every other composition.

locals {
  account_id  = "368083581678"
  bucket_name = "solventa-tfstate-${local.account_id}"
  bucket_arn  = "arn:aws:s3:::${local.bucket_name}"

  # Shared and permanent resource: no Story and no TTL tags.
  default_tags = {
    Project = "solventa"
    Owner   = "nicolas"
    Env     = "shared"
    Purpose = "terraform-state"
  }
}

resource "aws_s3_bucket" "this" {
  bucket        = local.bucket_name
  force_destroy = false

  lifecycle {
    prevent_destroy = true
  }
}

# ACLs disabled: the bucket owner owns every object.
resource "aws_s3_bucket_ownership_controls" "this" {
  bucket = aws_s3_bucket.this.bucket

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "this" {
  bucket = aws_s3_bucket.this.bucket

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Versioning lets a previous state be restored after a bad write or a delete.
resource "aws_s3_bucket_versioning" "this" {
  bucket = aws_s3_bucket.this.bucket

  versioning_configuration {
    status = "Enabled"
  }
}

# SSE-S3: no KMS key (a customer managed key costs USD 1 per month).
resource "aws_s3_bucket_server_side_encryption_configuration" "this" {
  bucket = aws_s3_bucket.this.bucket

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_policy" "this" {
  bucket = aws_s3_bucket.this.bucket

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "DenyInsecureTransport"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:*"
        Resource  = [local.bucket_arn, "${local.bucket_arn}/*"]
        Condition = {
          Bool = {
            "aws:SecureTransport" = "false"
          }
        }
      },
    ]
  })

  # Avoid concurrent configuration calls on the same bucket.
  depends_on = [aws_s3_bucket_public_access_block.this]
}

# Each plan or apply writes and deletes <key>.tflock, so with versioning every
# lock leaves a noncurrent version and a delete marker. Noncurrent versions
# expire after 30 days and the delete markers left behind are then removed.
resource "aws_s3_bucket_lifecycle_configuration" "this" {
  bucket = aws_s3_bucket.this.bucket

  rule {
    id     = "expire-old-state-versions"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = 30
    }

    expiration {
      expired_object_delete_marker = true
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  # Versioning must be enabled before the lifecycle configuration.
  depends_on = [aws_s3_bucket_versioning.this]
}
