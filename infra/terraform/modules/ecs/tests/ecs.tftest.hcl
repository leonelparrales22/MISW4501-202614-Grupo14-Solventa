# Configuration tests of the ECS cluster (T4). They run with a mocked AWS
# provider: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{}"
    }
  }

  mock_data "aws_region" {
    defaults = {
      region = "us-east-2"
    }
  }
}

variables {
  name                  = "solventa-test"
  vpc_id                = "vpc-12345678"
  subnet_ids            = ["subnet-aaaa1111", "subnet-bbbb2222"]
  assign_public_ip      = true
  alb_security_group_id = "sg-alb11111"

  services = {
    origination = {
      image            = "123456789012.dkr.ecr.us-east-2.amazonaws.com/solventa/origination:abc123"
      port             = 8080
      cpu              = 512
      memory           = 1024
      desired_count    = 3
      max_count        = 6
      load_balanced    = true
      target_group_arn = "arn:aws:elasticloadbalancing:us-east-2:123456789012:targetgroup/solventa-test-origination/0123456789abcdef"
      environment      = { PORT = "8080" }
      secrets          = { DATABASE_URL = "arn:aws:secretsmanager:us-east-2:123456789012:secret:solventa-test/origination/database-AbCdEf:DATABASE_URL::" }
      task_role_statements = [
        { sid = "Documents", actions = ["s3:GetObject"], resources = ["arn:aws:s3:::bucket/*"] },
      ]
    }
    openfinance = {
      image          = "123456789012.dkr.ecr.us-east-2.amazonaws.com/solventa/openfinance:abc123"
      port           = 9000
      cpu            = 256
      memory         = 512
      desired_count  = 1
      max_count      = 1
      discovery_name = "openfinance"
    }
  }

  tasks = {
    db-init = {
      image   = "public.ecr.aws/docker/library/postgres:16-alpine"
      command = ["echo"]
      secrets = { PGPASSWORD = "arn:aws:secretsmanager:us-east-2:123456789012:secret:rds!db-1234-AbCdEf:password::" }
    }
  }
}

run "services_scale_on_cpu_at_60_percent" {
  command = plan

  assert {
    condition     = length(aws_appautoscaling_target.this) == 1 && aws_appautoscaling_target.this["origination"].min_capacity == 3 && aws_appautoscaling_target.this["origination"].max_capacity == 6
    error_message = "Only services with max_count above desired_count scale, between those two values."
  }

  assert {
    condition     = aws_appautoscaling_policy.cpu["origination"].target_tracking_scaling_policy_configuration[0].target_value == 60
    error_message = "The target tracking must keep the average CPU at 60 % (ADR-02)."
  }

  assert {
    condition     = aws_ecs_service.this["origination"].desired_count == 3 && aws_ecs_service.this["origination"].launch_type == "FARGATE"
    error_message = "Services run on Fargate with their desired_count."
  }
}

run "services_are_reached_through_the_alb_or_cloud_map" {
  command = plan

  assert {
    condition     = length(aws_ecs_service.this["origination"].load_balancer) == 1 && length(aws_ecs_service.this["origination"].service_registries) == 0
    error_message = "origination is attached to its target group only."
  }

  assert {
    condition     = length(aws_ecs_service.this["openfinance"].load_balancer) == 0 && length(aws_service_discovery_service.this) == 1
    error_message = "The stub is registered in Cloud Map and has no target group."
  }

  assert {
    condition     = keys(aws_vpc_security_group_ingress_rule.from_alb) == ["8080"] && keys(aws_vpc_security_group_ingress_rule.discovery) == ["9000"]
    error_message = "The ALB reaches the service ports and the tasks reach the Cloud Map ports."
  }
}

run "execution_role_reads_only_the_referenced_secrets" {
  command = plan

  assert {
    condition = toset(local.secret_arns) == toset([
      "arn:aws:secretsmanager:us-east-2:123456789012:secret:solventa-test/origination/database-AbCdEf",
      "arn:aws:secretsmanager:us-east-2:123456789012:secret:rds!db-1234-AbCdEf",
    ])
    error_message = "The policy must list the secret ARNs without the JSON key suffix."
  }

  assert {
    condition     = length(aws_iam_role_policy.read_secrets) == 1
    error_message = "The execution role needs the read-secrets policy."
  }
}

run "task_roles_only_for_services_with_permissions" {
  command = plan

  assert {
    condition     = keys(aws_iam_role.task) == ["origination"]
    error_message = "Only services with task_role_statements get a task role."
  }

  assert {
    condition     = length(aws_ecs_task_definition.task) == 1 && aws_ecs_task_definition.task["db-init"].family == "solventa-test-db-init"
    error_message = "One-off tasks get a task definition and no service."
  }
}

run "rejects_an_unreachable_service" {
  command = plan

  variables {
    services = {
      orphan = { image = "x", port = 8080, cpu = 256, memory = 512, desired_count = 1, max_count = 1 }
    }
  }

  expect_failures = [var.services]
}
