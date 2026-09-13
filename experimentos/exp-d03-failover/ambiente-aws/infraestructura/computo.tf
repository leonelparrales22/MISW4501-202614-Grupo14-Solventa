# Cómputo: ALB (monitor de salud bajo prueba) + ECS Fargate con dos tareas
# repartidas en dos zonas de disponibilidad.

resource "random_password" "admin_token" {
  length  = 24
  special = false
}

resource "aws_ecr_repository" "stub" {
  name                 = "${var.prefijo}/stub-originacion"
  image_tag_mutability = "MUTABLE"
  force_delete         = true
}

resource "aws_cloudwatch_log_group" "stub" {
  name              = "/${var.prefijo}/stub-originacion"
  retention_in_days = 1
}

# --- IAM del plano de ejecución de ECS ---

data "aws_iam_policy_document" "asume_ecs" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "ejecucion" {
  name               = "${var.prefijo}-ecs-ejecucion"
  assume_role_policy = data.aws_iam_policy_document.asume_ecs.json
}

resource "aws_iam_role_policy_attachment" "ejecucion_base" {
  role       = aws_iam_role.ejecucion.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "leer_secreto" {
  statement {
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_secretsmanager_secret.db.arn]
  }
}

resource "aws_iam_role_policy" "leer_secreto" {
  name   = "leer-secreto-db"
  role   = aws_iam_role.ejecucion.id
  policy = data.aws_iam_policy_document.leer_secreto.json
}

# --- ALB y target group (parámetros del monitor como variables) ---

resource "aws_lb" "principal" {
  name               = "${var.prefijo}-alb"
  internal           = var.alb_interno
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = aws_subnet.publica[*].id
  idle_timeout       = var.alb_idle_timeout
}

resource "aws_lb_target_group" "stub" {
  name                 = "${var.prefijo}-tg"
  port                 = 8080
  protocol             = "HTTP"
  target_type          = "ip"
  vpc_id               = aws_vpc.principal.id
  deregistration_delay = var.deregistration_delay

  health_check {
    path                = "/health"
    protocol            = "HTTP"
    matcher             = "200"
    interval            = var.hc_intervalo
    timeout             = var.hc_timeout
    healthy_threshold   = var.hc_umbral_healthy
    unhealthy_threshold = var.hc_umbral_unhealthy
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.principal.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.stub.arn
  }
}

# --- ECS Fargate ---

resource "aws_ecs_cluster" "principal" {
  name = "${var.prefijo}-cluster"

  setting {
    name  = "containerInsights"
    value = "disabled"
  }
}

resource "aws_ecs_task_definition" "stub" {
  family                   = "${var.prefijo}-stub-originacion"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.tarea_cpu
  memory                   = var.tarea_memoria
  execution_role_arn       = aws_iam_role.ejecucion.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([merge({
    name         = "stub"
    image        = "${aws_ecr_repository.stub.repository_url}:${var.imagen_tag}"
    essential    = true
    stopTimeout  = 30
    portMappings = [{ containerPort = 8080, protocol = "tcp" }]
    environment = [
      { name = "PORT", value = "8080" },
      { name = "DB_HOST", value = aws_db_instance.originacion.address },
      { name = "DB_PORT", value = "5432" },
      { name = "DB_USER", value = "solventa" },
      { name = "DB_NAME", value = "originacion" },
      { name = "DB_SSLMODE", value = "require" },
      { name = "POOL_MAX_LIFETIME", value = var.pool_max_lifetime },
      { name = "POOL_MAX_IDLE_TIME", value = var.pool_max_idle_time },
      { name = "POOL_MAX_OPEN", value = tostring(var.pool_max_open) },
      { name = "POOL_MAX_IDLE", value = tostring(var.pool_max_idle) },
      { name = "HEALTH_MODE", value = var.health_mode },
      { name = "CPU_TRABAJO_MS", value = var.cpu_trabajo },
      { name = "ADMIN_TOKEN", value = random_password.admin_token.result },
    ]
    secrets = [
      { name = "DB_PASSWORD", valueFrom = aws_secretsmanager_secret.db.arn },
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.stub.name
        "awslogs-region"        = var.region
        "awslogs-stream-prefix" = "stub"
      }
    }
    }, var.container_health_check ? {
    healthCheck = {
      command     = ["CMD-SHELL", "wget -q -O /dev/null -T 3 http://localhost:8080/health || exit 1"]
      interval    = 5
      timeout     = 3
      retries     = 2
      startPeriod = 15
    }
  } : {})])
}

resource "aws_ecs_service" "stub" {
  name                               = "${var.prefijo}-originacion"
  cluster                            = aws_ecs_cluster.principal.id
  task_definition                    = aws_ecs_task_definition.stub.arn
  desired_count                      = var.tareas_deseadas
  launch_type                        = "FARGATE"
  health_check_grace_period_seconds  = 30
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200

  network_configuration {
    subnets          = aws_subnet.publica[*].id
    security_groups  = [aws_security_group.tarea.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.stub.arn
    container_name   = "stub"
    container_port   = 8080
  }

  depends_on = [aws_lb_listener.http, aws_iam_role_policy_attachment.ejecucion_base]
}
