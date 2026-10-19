terraform {
  # S3 native state locking (use_lockfile) is generally available since 1.11.
  required_version = ">= 1.11"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # Remote state in the project bucket (T4, bootstrap/remote-state) from the
  # first init. production-demo/platform and staging/platform read the outputs
  # of this key with terraform_remote_state.
  backend "s3" {
    bucket       = "solventa-tfstate-368083581678"
    key          = "bootstrap/identity/terraform.tfstate"
    region       = "us-east-2"
    use_lockfile = true
    encrypt      = true
  }
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
