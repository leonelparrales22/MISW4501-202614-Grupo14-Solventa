# Configuration tests of the CD base (image publishing). They run with a mocked
# AWS provider: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{}"
    }
  }
}

run "one_immutable_scanned_repository_per_image" {
  command = plan

  assert {
    condition     = length(aws_ecr_repository.this) == 8
    error_message = "There must be one repository for each of the 5 services and 3 stubs with a Dockerfile."
  }

  assert {
    condition     = alltrue([for r in aws_ecr_repository.this : r.image_tag_mutability == "IMMUTABLE"])
    error_message = "Tags must be immutable so a commit SHA always points to the same image."
  }

  assert {
    condition     = alltrue([for r in aws_ecr_repository.this : r.image_scanning_configuration[0].scan_on_push])
    error_message = "Every repository must scan images on push."
  }

  assert {
    condition     = alltrue([for r in aws_ecr_repository.this : r.force_delete == false])
    error_message = "Permanent repositories must not be force-deletable."
  }

  assert {
    condition     = aws_ecr_repository.this["origination"].name == "solventa/origination"
    error_message = "Repositories are named solventa/<image>."
  }

  assert {
    condition     = length(aws_ecr_lifecycle_policy.this) == 8
    error_message = "Every repository needs a lifecycle policy."
  }
}

run "oidc_provider_is_created_by_default" {
  command = plan

  assert {
    condition     = length(aws_iam_openid_connect_provider.github) == 1
    error_message = "The GitHub OIDC provider must be created by default."
  }

  assert {
    condition     = aws_iam_openid_connect_provider.github[0].url == "https://token.actions.githubusercontent.com"
    error_message = "The provider must be GitHub's token endpoint."
  }

  assert {
    condition     = aws_iam_role.cd.name == "solventa-github-actions-ecr"
    error_message = "The CD role name is referenced by the backend-cd workflow."
  }
}

run "existing_provider_is_reused" {
  command = plan

  variables {
    create_oidc_provider       = false
    existing_oidc_provider_arn = "arn:aws:iam::368083581678:oidc-provider/token.actions.githubusercontent.com"
  }

  assert {
    condition     = length(aws_iam_openid_connect_provider.github) == 0
    error_message = "No provider may be created when an existing one is supplied."
  }
}

run "existing_provider_arn_is_required_when_not_creating" {
  command = plan

  variables {
    create_oidc_provider = false
  }

  expect_failures = [var.existing_oidc_provider_arn]
}

run "region_is_validated" {
  command = plan

  variables {
    region = "us-east-1"
  }

  expect_failures = [var.region]
}
