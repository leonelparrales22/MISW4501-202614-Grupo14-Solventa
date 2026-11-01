# Configuration tests of the private S3 bucket (T4). They run with a mocked AWS
# provider: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{}"
    }
  }
}

variables {
  bucket = "solventa-test-documents-123456789012"
}

run "private_encrypted_versioned_bucket" {
  command = plan

  assert {
    condition = (
      aws_s3_bucket_public_access_block.this.block_public_acls &&
      aws_s3_bucket_public_access_block.this.block_public_policy &&
      aws_s3_bucket_public_access_block.this.ignore_public_acls &&
      aws_s3_bucket_public_access_block.this.restrict_public_buckets
    )
    error_message = "Every kind of public access must be blocked."
  }

  assert {
    condition     = one(one(aws_s3_bucket_server_side_encryption_configuration.this.rule).apply_server_side_encryption_by_default).sse_algorithm == "AES256"
    error_message = "Objects must be encrypted with SSE-S3."
  }

  assert {
    condition     = aws_s3_bucket_versioning.this.versioning_configuration[0].status == "Enabled"
    error_message = "The bucket must be versioned."
  }

  assert {
    condition     = aws_s3_bucket.this.force_destroy == false
    error_message = "force_destroy must be false by default."
  }

  assert {
    condition     = aws_s3_bucket_ownership_controls.this.rule[0].object_ownership == "BucketOwnerEnforced"
    error_message = "ACLs must be disabled (BucketOwnerEnforced)."
  }
}

run "policy_denies_insecure_transport" {
  command = plan

  assert {
    condition = anytrue([
      for s in data.aws_iam_policy_document.tls_only.statement :
      s.effect == "Deny" && anytrue([for c in s.condition : c.variable == "aws:SecureTransport" && contains(c.values, "false")])
    ])
    error_message = "The policy must deny any request without TLS."
  }
}

run "rejects_an_invalid_bucket_name" {
  command = plan

  variables {
    bucket = "Solventa_Documents"
  }

  expect_failures = [var.bucket]
}

run "default_policy_is_unchanged" {
  command = plan

  assert {
    condition     = length(data.aws_iam_policy_document.tls_only.source_policy_documents) == 0
    error_message = "Without extra_policy_json the bucket policy must not merge any other document."
  }

  assert {
    condition     = length(data.aws_iam_policy_document.tls_only.statement) == 1
    error_message = "Without extra_policy_json the bucket policy must keep only the TLS statement."
  }
}

run "extra_statements_are_merged" {
  command = plan

  variables {
    extra_policy_json = jsonencode({
      Version = "2012-10-17"
      Statement = [{
        Sid       = "AllowCloudFrontRead"
        Effect    = "Allow"
        Principal = { Service = "cloudfront.amazonaws.com" }
        Action    = "s3:GetObject"
        Resource  = "arn:aws:s3:::solventa-test-documents-123456789012/*"
      }]
    })
  }

  assert {
    condition = (
      length(data.aws_iam_policy_document.tls_only.source_policy_documents) == 1 &&
      data.aws_iam_policy_document.tls_only.source_policy_documents[0] == var.extra_policy_json
    )
    error_message = "extra_policy_json must be merged into the bucket policy."
  }

  assert {
    condition = anytrue([
      for s in data.aws_iam_policy_document.tls_only.statement :
      s.effect == "Deny" && anytrue([for c in s.condition : c.variable == "aws:SecureTransport"])
    ])
    error_message = "The TLS-only statement must stay when extra statements are added."
  }
}

run "no_cors_by_default" {
  command = plan

  assert {
    condition     = length(aws_s3_bucket_cors_configuration.this) == 0
    error_message = "Without origins the bucket has no CORS configuration."
  }
}

run "cors_lets_only_the_listed_origins_upload_and_read" {
  command = plan

  variables {
    cors_allowed_origins = ["https://d1234abcd.cloudfront.net"]
  }

  assert {
    condition = (
      length(aws_s3_bucket_cors_configuration.this) == 1 &&
      one(aws_s3_bucket_cors_configuration.this[0].cors_rule).allowed_origins == toset(["https://d1234abcd.cloudfront.net"]) &&
      one(aws_s3_bucket_cors_configuration.this[0].cors_rule).allowed_methods == toset(["GET", "PUT"])
    )
    error_message = "The portal origin may PUT and GET with signed URLs, and nothing else."
  }
}

run "rejects_a_wildcard_or_a_path_as_origin" {
  command = plan

  variables {
    cors_allowed_origins = ["*"]
  }

  expect_failures = [var.cors_allowed_origins]
}

run "rejects_extra_policy_that_is_not_json" {
  command = plan

  variables {
    extra_policy_json = "not a policy"
  }

  expect_failures = [var.extra_policy_json]
}
