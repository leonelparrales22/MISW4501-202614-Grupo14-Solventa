terraform {
  # S3 native state locking (use_lockfile) is generally available since 1.11.
  required_version = ">= 1.11"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # Bootstrap: the bucket that stores every state cannot hold its own state
  # before it exists. The first apply runs with local state, which is then
  # migrated into the bucket under bootstrap/remote-state/terraform.tfstate
  # (see README.md).
}

provider "aws" {
  region = var.region

  # Refuse to plan or apply against any other account (the default profile of
  # some machines points to another account).
  allowed_account_ids = [local.account_id]

  default_tags {
    tags = local.default_tags
  }
}
