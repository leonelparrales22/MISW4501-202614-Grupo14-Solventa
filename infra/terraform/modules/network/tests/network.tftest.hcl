# Configuration tests of the network (T4). They run with a mocked AWS provider:
# no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {
  mock_data "aws_availability_zones" {
    defaults = {
      names = ["us-east-2a", "us-east-2b", "us-east-2c"]
    }
  }

  mock_data "aws_region" {
    defaults = {
      region = "us-east-2"
    }
  }
}

variables {
  name       = "solventa-test"
  cidr_block = "10.50.0.0/16"
}

run "two_zones_with_a_public_and_a_private_subnet_each" {
  command = plan

  assert {
    condition     = length(aws_subnet.public) == 2 && length(aws_subnet.private) == 2
    error_message = "There must be one public and one private subnet per zone."
  }

  assert {
    condition     = aws_subnet.public[0].availability_zone == "us-east-2a" && aws_subnet.public[1].availability_zone == "us-east-2b"
    error_message = "The public subnets must sit in two different zones."
  }

  assert {
    condition     = aws_subnet.private[0].availability_zone == "us-east-2a" && aws_subnet.private[1].availability_zone == "us-east-2b"
    error_message = "The private subnets must sit in two different zones."
  }

  assert {
    condition     = aws_subnet.private[0].cidr_block == "10.50.10.0/24" && aws_subnet.public[0].cidr_block == "10.50.0.0/24"
    error_message = "Public subnets take the first /24 blocks and private subnets start at index 10."
  }
}

run "none_runs_the_tasks_in_public_subnets_without_nat" {
  command = plan

  assert {
    condition     = length(aws_nat_gateway.this) == 0 && length(aws_eip.nat) == 0 && length(aws_route.private_nat) == 0
    error_message = "nat_mode none must not create NAT Gateways, EIPs or default routes in the private subnets."
  }

  assert {
    condition     = output.app_assign_public_ip == true
    error_message = "Without NAT the tasks need a public IP to pull images."
  }

  assert {
    condition     = alltrue([for s in aws_subnet.public : s.map_public_ip_on_launch])
    error_message = "Without NAT the public subnets assign public IPs."
  }
}

run "single_shares_one_nat_between_both_zones" {
  command = plan

  variables {
    nat_mode = "single"
  }

  assert {
    condition     = length(aws_nat_gateway.this) == 1
    error_message = "nat_mode single must create exactly one NAT Gateway."
  }

  assert {
    condition     = length(aws_route.private_nat) == 2
    error_message = "Both private route tables need a default route to the NAT."
  }

  assert {
    condition     = output.app_assign_public_ip == false
    error_message = "With NAT the tasks run in the private subnets without a public IP."
  }
}

run "per_az_creates_one_nat_per_zone" {
  command = plan

  variables {
    nat_mode = "per_az"
  }

  assert {
    condition     = length(aws_nat_gateway.this) == 2 && length(aws_eip.nat) == 2
    error_message = "nat_mode per_az must create one NAT Gateway (and EIP) per zone."
  }

  assert {
    condition     = length(aws_route.private_nat) == 2
    error_message = "Each private route table needs a default route to the NAT of its zone."
  }
}

run "rejects_an_unknown_nat_mode" {
  command = plan

  variables {
    nat_mode = "two"
  }

  expect_failures = [var.nat_mode]
}
