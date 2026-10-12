terraform {
  # S3 native state locking (use_lockfile) is generally available since 1.11.
  required_version = ">= 1.11"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  backend "s3" {
    bucket       = "solventa-tfstate-368083581678"
    key          = "bootstrap/cd-ecr/terraform.tfstate"
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
