# Internal Application Load Balancer of the services (T4), reached only from
# the API Gateway VPC link. Deployment parameters fixed by experiment EXP-D03
# (ADR-02): health check every 5 s with a 3 s timeout and 2 failures to mark a
# target unhealthy, 30 s deregistration delay and 10 s idle timeout.
#
# One target group per service (targets of type ip, Fargate awsvpc) and
# path-based listener rules; a path that no service owns gets a 404.

locals {
  # An ALB rule accepts at most 5 values per condition, so the path patterns
  # of each target are split into chunks with one rule per chunk.
  rules = merge([
    for key, t in var.targets : {
      for i, paths in chunklist(t.path_patterns, 5) : "${key}-${i}" => {
        target   = key
        priority = t.priority * 10 + i
        paths    = paths
      }
    }
  ]...)
}

resource "aws_security_group" "this" {
  name        = "${var.name}-alb"
  description = "Internal ALB, reachable only from the API Gateway VPC link"
  vpc_id      = var.vpc_id
  tags        = merge(var.tags, { Name = "${var.name}-alb" })
}

resource "aws_vpc_security_group_ingress_rule" "listener" {
  for_each = var.ingress_security_group_ids

  security_group_id            = aws_security_group.this.id
  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = var.listener_port
  to_port                      = var.listener_port
}

resource "aws_vpc_security_group_egress_rule" "vpc" {
  security_group_id = aws_security_group.this.id
  ip_protocol       = "tcp"
  from_port         = 0
  to_port           = 65535
  cidr_ipv4         = var.vpc_cidr_block
}

resource "aws_lb" "this" {
  name                       = var.name
  internal                   = true
  load_balancer_type         = "application"
  subnets                    = var.subnet_ids
  security_groups            = [aws_security_group.this.id]
  idle_timeout               = var.idle_timeout
  drop_invalid_header_fields = true
  enable_deletion_protection = var.deletion_protection
  tags                       = var.tags
}

resource "aws_lb_target_group" "this" {
  for_each = var.targets

  name                 = "${var.name}-${each.key}"
  port                 = each.value.port
  protocol             = "HTTP"
  target_type          = "ip"
  vpc_id               = var.vpc_id
  deregistration_delay = var.deregistration_delay
  tags                 = var.tags

  health_check {
    path                = var.health_check.path
    interval            = var.health_check.interval
    timeout             = var.health_check.timeout
    healthy_threshold   = var.health_check.healthy_threshold
    unhealthy_threshold = var.health_check.unhealthy_threshold
    matcher             = var.health_check.matcher
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = var.listener_port
  protocol          = "HTTP"
  tags              = var.tags

  default_action {
    type = "fixed-response"

    fixed_response {
      content_type = "application/json"
      message_body = "{\"message\":\"Not Found\"}"
      status_code  = "404"
    }
  }
}

resource "aws_lb_listener_rule" "this" {
  for_each = local.rules

  listener_arn = aws_lb_listener.http.arn
  priority     = each.value.priority
  tags         = var.tags

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.this[each.value.target].arn
  }

  condition {
    path_pattern {
      values = each.value.paths
    }
  }
}
