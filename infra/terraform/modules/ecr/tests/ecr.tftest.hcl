# Configuration tests of the ECR repositories (T4). They run with a mocked AWS
# provider: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {}

variables {
  images = ["origination", "claims"]
}

run "immutable_scanned_repositories_with_lifecycle" {
  command = plan

  assert {
    condition     = length(aws_ecr_repository.this) == 2
    error_message = "There must be one repository per image."
  }

  assert {
    condition     = aws_ecr_repository.this["origination"].name == "solventa/origination"
    error_message = "Repositories are named solventa/<image>."
  }

  assert {
    condition     = alltrue([for r in aws_ecr_repository.this : r.image_tag_mutability == "IMMUTABLE"])
    error_message = "Tags must be immutable by default so a commit SHA always points to the same image."
  }

  assert {
    condition     = alltrue([for r in aws_ecr_repository.this : r.image_scanning_configuration[0].scan_on_push])
    error_message = "Every repository must scan images on push."
  }

  assert {
    condition     = alltrue([for r in aws_ecr_repository.this : r.force_delete == false])
    error_message = "Repositories must not be force-deletable by default."
  }

  assert {
    condition     = length(aws_ecr_lifecycle_policy.this) == 2
    error_message = "Every repository needs a lifecycle policy."
  }

  assert {
    condition = alltrue([
      for p in aws_ecr_lifecycle_policy.this :
      jsondecode(p.policy).rules[0].selection.tagStatus == "untagged" && jsondecode(p.policy).rules[0].selection.countNumber == 7
    ])
    error_message = "Untagged images must expire after 7 days by default."
  }
}

run "rejects_invalid_image_names" {
  command = plan

  variables {
    images = ["Origination"]
  }

  expect_failures = [var.images]
}
