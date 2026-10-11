# Configuration tests of the remote state bucket (T4). They run with a mocked
# AWS provider: no credentials, no network, nothing is created. Every run uses
# command = plan, so there is nothing to clean up (the bucket has
# prevent_destroy, which would make the cleanup of an applied run fail).
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {}

run "bucket_is_the_project_state_bucket" {
  command = plan

  assert {
    condition     = aws_s3_bucket.this.bucket == "solventa-tfstate-368083581678"
    error_message = "The bucket must be named solventa-tfstate-368083581678."
  }

  assert {
    condition     = aws_s3_bucket.this.force_destroy == false
    error_message = "force_destroy must be false: the bucket holds the state of every composition."
  }

  assert {
    condition     = output.bucket_name == "solventa-tfstate-368083581678"
    error_message = "The bucket_name output must expose the bucket name."
  }

  assert {
    condition     = output.region == "us-east-2"
    error_message = "The bucket must live in us-east-2."
  }

  assert {
    condition = alltrue([
      for b in [
        aws_s3_bucket_ownership_controls.this.bucket,
        aws_s3_bucket_public_access_block.this.bucket,
        aws_s3_bucket_versioning.this.bucket,
        aws_s3_bucket_server_side_encryption_configuration.this.bucket,
        aws_s3_bucket_policy.this.bucket,
        aws_s3_bucket_lifecycle_configuration.this.bucket,
      ] : b == "solventa-tfstate-368083581678"
    ])
    error_message = "Every bucket setting must target the state bucket."
  }
}

run "other_regions_are_rejected" {
  command = plan

  variables {
    region = "us-east-1"
  }

  expect_failures = [var.region]
}

run "versioning_is_enabled" {
  command = plan

  assert {
    condition = alltrue([
      for v in aws_s3_bucket_versioning.this.versioning_configuration : v.status == "Enabled"
    ]) && length(aws_s3_bucket_versioning.this.versioning_configuration) == 1
    error_message = "Versioning must be enabled so a previous state can be restored."
  }
}

run "encryption_is_sse_s3_without_kms" {
  command = plan

  assert {
    condition     = length(aws_s3_bucket_server_side_encryption_configuration.this.rule) == 1
    error_message = "There must be exactly one encryption rule."
  }

  # kms_master_key_id is optional and computed, so it is unknown in a plan with
  # a mocked provider; AES256 already means SSE-S3, and the deployed bucket is
  # checked with aws s3api get-bucket-encryption (no KMSMasterKeyID).
  assert {
    condition = alltrue(flatten([
      for r in aws_s3_bucket_server_side_encryption_configuration.this.rule : [
        for d in r.apply_server_side_encryption_by_default : d.sse_algorithm == "AES256"
      ]
      ])) && alltrue([
      for r in aws_s3_bucket_server_side_encryption_configuration.this.rule :
      length(r.apply_server_side_encryption_by_default) == 1
    ])
    error_message = "Encryption must be SSE-S3 (AES256), not KMS."
  }
}

run "public_access_is_blocked_and_acls_disabled" {
  command = plan

  assert {
    condition = (
      aws_s3_bucket_public_access_block.this.block_public_acls == true &&
      aws_s3_bucket_public_access_block.this.block_public_policy == true &&
      aws_s3_bucket_public_access_block.this.ignore_public_acls == true &&
      aws_s3_bucket_public_access_block.this.restrict_public_buckets == true
    )
    error_message = "The four public access block settings must be true."
  }

  assert {
    condition = alltrue([
      for r in aws_s3_bucket_ownership_controls.this.rule : r.object_ownership == "BucketOwnerEnforced"
    ]) && length(aws_s3_bucket_ownership_controls.this.rule) == 1
    error_message = "Object ownership must be BucketOwnerEnforced (ACLs disabled)."
  }
}

run "policy_denies_requests_without_tls" {
  command = plan

  assert {
    condition     = length(jsondecode(aws_s3_bucket_policy.this.policy).Statement) == 1
    error_message = "The bucket policy must have exactly one statement."
  }

  assert {
    condition = alltrue([
      for s in jsondecode(aws_s3_bucket_policy.this.policy).Statement :
      s.Sid == "DenyInsecureTransport" && s.Effect == "Deny" && s.Principal == "*" && s.Action == "s3:*"
    ])
    error_message = "The statement must deny every S3 action to every principal."
  }

  assert {
    condition = alltrue([
      for s in jsondecode(aws_s3_bucket_policy.this.policy).Statement :
      toset(s.Resource) == toset([
        "arn:aws:s3:::solventa-tfstate-368083581678",
        "arn:aws:s3:::solventa-tfstate-368083581678/*",
      ])
    ])
    error_message = "The statement must cover the bucket and every object in it."
  }

  assert {
    condition = alltrue([
      for s in jsondecode(aws_s3_bucket_policy.this.policy).Statement :
      s.Condition.Bool["aws:SecureTransport"] == "false"
    ])
    error_message = "The statement must apply only to requests without TLS (aws:SecureTransport = false)."
  }
}

run "lifecycle_cleans_old_versions_uploads_and_markers" {
  command = plan

  assert {
    condition     = length(aws_s3_bucket_lifecycle_configuration.this.rule) == 1
    error_message = "There must be exactly one lifecycle rule."
  }

  assert {
    condition = alltrue([
      for r in aws_s3_bucket_lifecycle_configuration.this.rule :
      r.id == "expire-old-state-versions" && r.status == "Enabled"
    ])
    error_message = "The lifecycle rule must be enabled."
  }

  # The filter's prefix and size limits are optional and computed (unknown in a
  # plan with a mocked provider); an empty filter block has no and/tag blocks.
  assert {
    condition = alltrue(flatten([
      for r in aws_s3_bucket_lifecycle_configuration.this.rule : [
        for f in r.filter : length(f.and) == 0 && length(f.tag) == 0
      ]
      ])) && alltrue([
      for r in aws_s3_bucket_lifecycle_configuration.this.rule : length(r.filter) == 1
    ])
    error_message = "The lifecycle rule must apply to the whole bucket (empty filter)."
  }

  assert {
    condition = alltrue(flatten([
      for r in aws_s3_bucket_lifecycle_configuration.this.rule : [
        for n in r.noncurrent_version_expiration : n.noncurrent_days == 30
      ]
    ]))
    error_message = "Noncurrent state versions must expire after 30 days."
  }

  assert {
    condition = alltrue(flatten([
      for r in aws_s3_bucket_lifecycle_configuration.this.rule : [
        for e in r.expiration : e.expired_object_delete_marker == true
      ]
    ]))
    error_message = "Expired object delete markers must be removed."
  }

  assert {
    condition = alltrue(flatten([
      for r in aws_s3_bucket_lifecycle_configuration.this.rule : [
        for a in r.abort_incomplete_multipart_upload : a.days_after_initiation == 7
      ]
    ]))
    error_message = "Incomplete multipart uploads must be aborted after 7 days."
  }
}

run "tags_identify_a_permanent_shared_resource" {
  command = plan

  assert {
    condition = local.default_tags == {
      Project = "solventa"
      Owner   = "nicolas"
      Env     = "shared"
      Purpose = "terraform-state"
    }
    error_message = "The default tags must be exactly Project, Owner, Env = shared and Purpose = terraform-state."
  }

  assert {
    condition     = !contains(keys(local.default_tags), "TTL")
    error_message = "The bucket is permanent: it must not carry a TTL tag."
  }
}
