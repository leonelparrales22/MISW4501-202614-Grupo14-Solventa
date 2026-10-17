# Configuration tests of the Secrets Manager secrets (T4). They run with a
# mocked AWS provider: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {}

variables {
  prefix = "solventa-test"
  secrets = {
    "origination/database" = "DATABASE_URL of origination"
    "claims/database"      = "DATABASE_URL of claims"
  }
  values = {
    "origination/database" = "{\"DATABASE_URL\":\"postgres://a:b@host:5432/origination\"}"
    "claims/database"      = "{\"DATABASE_URL\":\"postgres://c:d@host:5432/claims\"}"
  }
}

run "one_secret_and_version_per_key" {
  command = plan

  assert {
    condition     = length(aws_secretsmanager_secret.this) == 2 && length(aws_secretsmanager_secret_version.this) == 2
    error_message = "Every key needs a secret and its value."
  }

  assert {
    condition     = aws_secretsmanager_secret.this["origination/database"].name == "solventa-test/origination/database"
    error_message = "Secrets are named <prefix>/<key>."
  }

  assert {
    condition     = aws_secretsmanager_secret.this["claims/database"].recovery_window_in_days == 7
    error_message = "Deleted secrets can be restored for 7 days by default."
  }
}

run "rejects_an_invalid_recovery_window" {
  command = plan

  variables {
    recovery_window_in_days = 3
  }

  expect_failures = [var.recovery_window_in_days]
}
