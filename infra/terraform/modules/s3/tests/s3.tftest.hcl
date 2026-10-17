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
