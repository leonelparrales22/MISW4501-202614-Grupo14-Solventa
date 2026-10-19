# ECS Fargate cluster of an environment (T4).
#
# - services: long-running services. Behind the ALB (load_balanced) and/or
#   registered in Cloud Map (discovery_name, used by the stubs). Target
#   tracking on CPU at 60 % between desired_count and max_count (ADR-02).
# - tasks: one-off task definitions without a service (for example the
#   database bootstrap), started with aws ecs run-task.
#
# Every task shares one security group: the ALB reaches the service ports and
# the tasks reach the Cloud Map services among themselves. Secrets reach the
# containers through the ECS secrets field; only the execution role can read
# them, and only the ones referenced.

data "aws_region" "current" {}

locals {
  region = data.aws_region.current.region

  lb_services        = { for k, s in var.services : k => s if s.load_balanced }
  discovery_services = { for k, s in var.services : k => s if s.discovery_name != null }
  scaled_services    = { for k, s in var.services : k => s if s.desired_count > 0 && s.max_count > s.desired_count }
  task_roles         = { for k, s in var.services : k => s if length(s.task_role_statements) > 0 }

  # "arn:aws:secretsmanager:<region>:<account>:secret:<name>[:<json key>::]"
  # -> the secret ARN, which is what the IAM policy needs.
  secret_arns = distinct(flatten([
    for c in concat(values(var.services), values(var.tasks)) : [
      for v in values(c.secrets) : join(":", slice(split(":", v), 0, 7))
    ]
  ]))

  # Known at plan time even when the ARNs are not (they come from resources).
  reads_secrets = length(flatten([for c in concat(values(var.services), values(var.tasks)) : keys(c.secrets)])) > 0

  log_options = {
    "awslogs-group"  = aws_cloudwatch_log_group.this.name
    "awslogs-region" = local.region
  }
}

resource "aws_ecs_cluster" "this" {
  name = var.name
  tags = var.tags

  setting {
    name  = "containerInsights"
    value = var.container_insights ? "enabled" : "disabled"
  }
}

resource "aws_cloudwatch_log_group" "this" {
  name              = "/ecs/${var.name}"
  retention_in_days = var.log_retention_days
  tags              = var.tags
}

resource "aws_service_discovery_private_dns_namespace" "this" {
  count = length(local.discovery_services) > 0 ? 1 : 0

  name = var.discovery_namespace
  vpc  = var.vpc_id
  tags = var.tags
}

resource "aws_service_discovery_service" "this" {
  for_each = local.discovery_services

  name = each.value.discovery_name
  tags = var.tags

  dns_config {
    namespace_id   = aws_service_discovery_private_dns_namespace.this[0].id
    routing_policy = "MULTIVALUE"

    dns_records {
      type = "A"
      ttl  = 10
    }
  }
}

# --- Security group of the tasks ---------------------------------------------------

resource "aws_security_group" "tasks" {
  name        = "${var.name}-tasks"
  description = "Fargate tasks: service ports from the ALB, Cloud Map ports among tasks"
  vpc_id      = var.vpc_id
  tags        = merge(var.tags, { Name = "${var.name}-tasks" })
}

resource "aws_vpc_security_group_ingress_rule" "from_alb" {
  for_each = toset([for s in values(local.lb_services) : tostring(s.port)])

  security_group_id            = aws_security_group.tasks.id
  referenced_security_group_id = var.alb_security_group_id
  ip_protocol                  = "tcp"
  from_port                    = tonumber(each.value)
  to_port                      = tonumber(each.value)
}

resource "aws_vpc_security_group_ingress_rule" "discovery" {
  for_each = toset([for s in values(local.discovery_services) : tostring(s.port)])

  security_group_id            = aws_security_group.tasks.id
  referenced_security_group_id = aws_security_group.tasks.id
  ip_protocol                  = "tcp"
  from_port                    = tonumber(each.value)
  to_port                      = tonumber(each.value)
}

# Egress to the internet (ECR, Secrets Manager, CloudWatch, Cognito JWKS),
# directly or through the NAT, and to the data stores in the VPC.
resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.tasks.id
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

# --- IAM -------------------------------------------------------------------------------

data "aws_iam_policy_document" "ecs_tasks_assume" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "execution" {
  name               = "${var.name}-ecs-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "read_secrets" {
  count = local.reads_secrets ? 1 : 0

  statement {
    sid       = "ReadReferencedSecrets"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = local.secret_arns
  }
}

resource "aws_iam_role_policy" "read_secrets" {
  count = local.reads_secrets ? 1 : 0

  name   = "read-secrets"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.read_secrets[0].json
}

# One task role per service that needs AWS permissions (S3, SNS, SQS).
resource "aws_iam_role" "task" {
  for_each = local.task_roles

  name               = "${var.name}-${each.key}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
  tags               = var.tags
}

data "aws_iam_policy_document" "task" {
  for_each = local.task_roles

  dynamic "statement" {
    for_each = each.value.task_role_statements

    content {
      sid       = statement.value.sid
      actions   = statement.value.actions
      resources = statement.value.resources
    }
  }
}

resource "aws_iam_role_policy" "task" {
  for_each = local.task_roles

  name   = "service"
  role   = aws_iam_role.task[each.key].id
  policy = data.aws_iam_policy_document.task[each.key].json
}

# --- Services ----------------------------------------------------------------------------

resource "aws_ecs_task_definition" "service" {
  for_each = var.services

  family                   = "${var.name}-${each.key}"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = each.value.cpu
  memory                   = each.value.memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = contains(keys(local.task_roles), each.key) ? aws_iam_role.task[each.key].arn : null
  tags                     = var.tags

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([
    {
      name         = each.key
      image        = each.value.image
      essential    = true
      portMappings = [{ containerPort = each.value.port, protocol = "tcp" }]
      environment  = [for k in sort(keys(each.value.environment)) : { name = k, value = each.value.environment[k] }]
      secrets      = [for k in sort(keys(each.value.secrets)) : { name = k, valueFrom = each.value.secrets[k] }]
      logConfiguration = {
        logDriver = "awslogs"
        options   = merge(local.log_options, { "awslogs-stream-prefix" = each.key })
      }
    },
  ])
}

resource "aws_ecs_service" "this" {
  for_each = var.services

  name                              = each.key
  cluster                           = aws_ecs_cluster.this.id
  task_definition                   = aws_ecs_task_definition.service[each.key].arn
  desired_count                     = each.value.desired_count
  launch_type                       = "FARGATE"
  health_check_grace_period_seconds = each.value.load_balanced ? var.health_check_grace_period_seconds : null
  propagate_tags                    = "SERVICE"
  tags                              = var.tags

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = var.subnet_ids
    security_groups  = [aws_security_group.tasks.id]
    assign_public_ip = var.assign_public_ip
  }

  dynamic "load_balancer" {
    for_each = each.value.load_balanced ? [each.value.target_group_arn] : []

    content {
      target_group_arn = load_balancer.value
      container_name   = each.key
      container_port   = each.value.port
    }
  }

  dynamic "service_registries" {
    for_each = each.value.discovery_name != null ? [aws_service_discovery_service.this[each.key].arn] : []

    content {
      registry_arn = service_registries.value
    }
  }
}

# Target tracking on CPU (ADR-02: 60 %). An apply resets desired_count to the
# configured value; autoscaling moves it again within [desired, max].
resource "aws_appautoscaling_target" "this" {
  for_each = local.scaled_services

  service_namespace  = "ecs"
  resource_id        = "service/${aws_ecs_cluster.this.name}/${aws_ecs_service.this[each.key].name}"
  scalable_dimension = "ecs:service:DesiredCount"
  min_capacity       = each.value.desired_count
  max_capacity       = each.value.max_count
}

resource "aws_appautoscaling_policy" "cpu" {
  for_each = local.scaled_services

  name               = "${var.name}-${each.key}-cpu"
  policy_type        = "TargetTrackingScaling"
  service_namespace  = aws_appautoscaling_target.this[each.key].service_namespace
  resource_id        = aws_appautoscaling_target.this[each.key].resource_id
  scalable_dimension = aws_appautoscaling_target.this[each.key].scalable_dimension

  target_tracking_scaling_policy_configuration {
    target_value       = var.cpu_target_percent
    scale_in_cooldown  = 60
    scale_out_cooldown = 60

    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
  }
}

# --- One-off tasks -------------------------------------------------------------------------

resource "aws_ecs_task_definition" "task" {
  for_each = var.tasks

  family                   = "${var.name}-${each.key}"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = each.value.cpu
  memory                   = each.value.memory
  execution_role_arn       = aws_iam_role.execution.arn
  tags                     = var.tags

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([
    {
      name        = each.key
      image       = each.value.image
      essential   = true
      entryPoint  = each.value.entry_point
      command     = each.value.command
      environment = [for k in sort(keys(each.value.environment)) : { name = k, value = each.value.environment[k] }]
      secrets     = [for k in sort(keys(each.value.secrets)) : { name = k, valueFrom = each.value.secrets[k] }]
      logConfiguration = {
        logDriver = "awslogs"
        options   = merge(local.log_options, { "awslogs-stream-prefix" = each.key })
      }
    },
  ])
}
