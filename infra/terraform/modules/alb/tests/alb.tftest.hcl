# Configuration tests of the internal ALB (T4). They run with a mocked AWS
# provider: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {}

variables {
  name                       = "solventa-test"
  vpc_id                     = "vpc-12345678"
  vpc_cidr_block             = "10.50.0.0/16"
  subnet_ids                 = ["subnet-aaaa1111", "subnet-bbbb2222"]
  ingress_security_group_ids = { vpc_link = "sg-11112222" }
  targets = {
    origination = {
      port     = 8080
      priority = 1
      path_patterns = [
        "/v1/risk-profiles*", "/v1/offers*", "/v1/acceptances*", "/v1/assisted-quotes*",
        "/v1/customers*", "/v1/policies*", "/v1/documents*",
      ]
    }
    claims = { port = 8080, priority = 2, path_patterns = ["/v1/claims*"] }
  }
}

run "exp_d03_deployment_parameters" {
  command = plan

  assert {
    condition     = aws_lb.this.internal && aws_lb.this.idle_timeout == 10
    error_message = "The ALB must be internal with a 10 s idle timeout (EXP-D03)."
  }

  assert {
    condition     = alltrue([for tg in aws_lb_target_group.this : tg.deregistration_delay == "30"])
    error_message = "Every target group must drain for 30 s (EXP-D03)."
  }

  assert {
    condition = alltrue([
      for tg in aws_lb_target_group.this :
      tg.health_check[0].path == "/health" && tg.health_check[0].interval == 5 && tg.health_check[0].timeout == 3 && tg.health_check[0].unhealthy_threshold == 2
    ])
    error_message = "The health check must be GET /health every 5 s, 3 s timeout, unhealthy after 2 failures (EXP-D03)."
  }

  assert {
    condition     = alltrue([for tg in aws_lb_target_group.this : tg.target_type == "ip"])
    error_message = "Fargate (awsvpc) targets are registered by IP."
  }
}

run "path_patterns_are_split_into_rules_of_five" {
  command = plan

  assert {
    condition     = length(aws_lb_listener_rule.this) == 3
    error_message = "Seven origination patterns need two rules and claims one."
  }

  assert {
    condition     = length(local.rules["origination-0"].paths) == 5 && length(local.rules["origination-1"].paths) == 2
    error_message = "A rule holds at most five path patterns."
  }

  assert {
    condition     = aws_lb_listener_rule.this["origination-1"].priority == 11 && aws_lb_listener_rule.this["claims-0"].priority == 20
    error_message = "Rule priorities derive from the target priority and the chunk index."
  }

  assert {
    condition     = aws_lb_listener.http.default_action[0].fixed_response[0].status_code == "404"
    error_message = "Paths no service owns must get a 404."
  }
}

run "listener_only_admits_the_vpc_link" {
  command = plan

  assert {
    condition     = length(aws_vpc_security_group_ingress_rule.listener) == 1 && aws_vpc_security_group_ingress_rule.listener["vpc_link"].from_port == 80
    error_message = "Only the VPC link security group may reach the listener."
  }
}

run "rejects_a_name_too_long_for_the_target_groups" {
  command = plan

  variables {
    name = "solventa-production-demo"
  }

  expect_failures = [var.name]
}

run "rejects_duplicated_priorities" {
  command = plan

  variables {
    targets = {
      origination = { port = 8080, priority = 1, path_patterns = ["/v1/offers*"] }
      claims      = { port = 8080, priority = 1, path_patterns = ["/v1/claims*"] }
    }
  }

  expect_failures = [var.targets]
}
