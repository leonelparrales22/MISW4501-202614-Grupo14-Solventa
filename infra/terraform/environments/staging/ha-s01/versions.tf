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
  # The state never stays on disk; the window is applied and destroyed in the
  # same session.
  backend "s3" {
    bucket       = "solventa-tfstate-368083581678"
    key          = "staging/ha-s01/terraform.tfstate"
    region       = "us-east-2"
    use_lockfile = true
    encrypt      = true
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project = "solventa"
      Owner   = "nicolas"
      Env     = "staging"
      Story   = "HA-S01"
      TTL     = var.ttl
    }
  }
}
