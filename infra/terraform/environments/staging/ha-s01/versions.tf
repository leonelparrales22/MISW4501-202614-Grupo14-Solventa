terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # Ephemeral verification window: local state, applied and destroyed in the
  # same session. Move it to the team's remote state (S3 with locking) once it
  # exists.
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
