# Infraestructura efímera de EXP-L01 en AWS.
#
# Se levanta, se corren las combinaciones, se destruye. Nada de esto queda
# encendido entre sesiones. Ver README.md de deploy/terraform para el flujo.

terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
  }
}

provider "aws" {
  region = var.region
  default_tags {
    tags = {
      Proyecto    = "MISW4501-Solventa"
      Experimento = "EXP-L01"
      Efimero     = "si"
    }
  }
}

data "aws_caller_identity" "actual" {}
data "aws_availability_zones" "disponibles" {
  state = "available"
}

locals {
  nombre = "expl01"
  # Una sola AZ a propósito: el experimento es de latencia, no de disponibilidad,
  # y así todo el tráfico queda dentro de la misma zona.
  az = data.aws_availability_zones.disponibles.names[0]
}
