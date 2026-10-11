terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # Ephemeral measurement window (decision D-8): local state, applied and
  # destroyed in the same session. Move it to the team's remote state (S3 with
  # locking) once it exists.
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project = "solventa"
      Owner   = "nicolas"
      Env     = "staging"
      Story   = "HA-L01"
      TTL     = var.ttl
    }
  }
}
