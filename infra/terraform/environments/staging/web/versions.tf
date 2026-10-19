terraform {
  required_version = ">= 1.11"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # Remote state in the project bucket (T4, infra/terraform/bootstrap/remote-state):
  # one key per composition and S3 native locking (use_lockfile, Terraform >= 1.11).
  backend "s3" {
    bucket       = "solventa-tfstate-368083581678"
    key          = "staging/web/terraform.tfstate"
    region       = "us-east-2"
    use_lockfile = true
    encrypt      = true
  }
}

provider "aws" {
  region = var.region

  # Refuse to plan or apply against any other account.
  allowed_account_ids = ["368083581678"]

  # extra_tags (Owner, Story...) are added; the four default tags always win.
  default_tags {
    tags = merge(var.extra_tags, {
      Project = "solventa"
      Env     = "staging"
      Task    = "T5"
      TTL     = var.ttl
    })
  }
}
