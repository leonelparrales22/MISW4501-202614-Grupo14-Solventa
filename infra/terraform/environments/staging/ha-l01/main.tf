# Ephemeral staging window to measure HA-L01 (CA-1, CA-4, CA-5) through the
# API Gateway. Topology A of decision D-8, aligned with ADR-02 but without NAT:
#
#   k6 task --(internet)--> API Gateway REST (cache + usage plans)
#     --VPC link V2--> internal ALB --> origination task (+ Redis sidecar)
#     --Cloud Map--> sources task (Open Finance, Open Data and KYC stubs behind Toxiproxy)
#
# Two public subnets (the ALB needs two AZs; the tasks run in the first one),
# public IPs on the tasks to pull images, no NAT Gateway. Everything is
# destroyed in the same session (R8/R12 of Nicolás's workflow).

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs           = slice(data.aws_availability_zones.available.names, 0, 2)
  desired_tasks = var.services_enabled ? 1 : 0
  namespace     = "ha-l01.local"
  sources_host  = "fuentes.${local.namespace}"
  images = {
    for repo in ["origination", "openfinance", "opendata", "kyc", "k6"] :
    repo => "${aws_ecr_repository.this[repo].repository_url}:${var.image_tag}"
  }
  latency = {
    normal    = { latency = 85, jitter = 55 }
    degradado = { latency = 150, jitter = 120 }
  }[var.source_latency_profile]
  # name:listen:upstream of each Toxiproxy proxy (same ports as tests/load-k6/ha-l01).
  proxies = ["openfinance:9001:9000", "opendata:9011:9010", "kyc:9021:9020"]
}

# --- Network --------------------------------------------------------------------------

resource "aws_vpc" "this" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = var.name }
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = var.name }
}

resource "aws_subnet" "public" {
  count = 2

  vpc_id                  = aws_vpc.this.id
  availability_zone       = local.azs[count.index]
  cidr_block              = cidrsubnet(var.vpc_cidr, 8, count.index)
  map_public_ip_on_launch = true
  tags                    = { Name = "${var.name}-public-${count.index}" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = "${var.name}-public" }

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
}

resource "aws_route_table_association" "public" {
  count = 2

  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# --- Security groups ----------------------------------------------------------------

resource "aws_security_group" "vpc_link" {
  name        = "${var.name}-vpc-link"
  description = "VPC link V2 of the API Gateway"
  vpc_id      = aws_vpc.this.id
}

resource "aws_security_group" "alb" {
  name        = "${var.name}-alb"
  description = "Internal ALB, reachable only from the VPC link"
  vpc_id      = aws_vpc.this.id
}

resource "aws_security_group" "origination" {
  name        = "${var.name}-origination"
  description = "Origination task, reachable only from the ALB"
  vpc_id      = aws_vpc.this.id
}

resource "aws_security_group" "sources" {
  name        = "${var.name}-sources"
  description = "Stubs behind Toxiproxy, reachable only from origination"
  vpc_id      = aws_vpc.this.id
}

resource "aws_security_group" "k6" {
  name        = "${var.name}-k6"
  description = "k6 task, egress only"
  vpc_id      = aws_vpc.this.id
}

resource "aws_vpc_security_group_ingress_rule" "alb_from_vpc_link" {
  security_group_id            = aws_security_group.alb.id
  referenced_security_group_id = aws_security_group.vpc_link.id
  ip_protocol                  = "tcp"
  from_port                    = 80
  to_port                      = 80
}

resource "aws_vpc_security_group_ingress_rule" "origination_from_alb" {
  security_group_id            = aws_security_group.origination.id
  referenced_security_group_id = aws_security_group.alb.id
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
}

resource "aws_vpc_security_group_ingress_rule" "sources_from_origination" {
  for_each = toset(["9001", "9011", "9021"])

  security_group_id            = aws_security_group.sources.id
  referenced_security_group_id = aws_security_group.origination.id
  ip_protocol                  = "tcp"
  from_port                    = tonumber(each.value)
  to_port                      = tonumber(each.value)
}

resource "aws_vpc_security_group_egress_rule" "all" {
  for_each = {
    vpc_link    = aws_security_group.vpc_link.id
    alb         = aws_security_group.alb.id
    origination = aws_security_group.origination.id
    sources     = aws_security_group.sources.id
    k6          = aws_security_group.k6.id
  }

  security_group_id = each.value
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

# --- Images and logs ------------------------------------------------------------------

resource "aws_ecr_repository" "this" {
  for_each = toset(["origination", "openfinance", "opendata", "kyc", "k6"])

  name                 = "${var.name}/${each.key}"
  image_tag_mutability = "MUTABLE"
  force_delete         = true # ephemeral window: destroy removes the images too
}

resource "aws_cloudwatch_log_group" "this" {
  name              = "/ecs/${var.name}"
  retention_in_days = 1
}

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
  name               = "${var.name}-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# --- ECS ---------------------------------------------------------------------------------

resource "aws_ecs_cluster" "this" {
  name = var.name
}

resource "aws_service_discovery_private_dns_namespace" "this" {
  name = local.namespace
  vpc  = aws_vpc.this.id
}

resource "aws_service_discovery_service" "sources" {
  name = "fuentes"

  dns_config {
    namespace_id   = aws_service_discovery_private_dns_namespace.this.id
    routing_policy = "MULTIVALUE"

    dns_records {
      type = "A"
      ttl  = 10
    }
  }
}

locals {
  log_options = {
    "awslogs-group"         = aws_cloudwatch_log_group.this.name
    "awslogs-region"        = var.region
    "awslogs-stream-prefix" = "ha-l01"
  }

  # Creates the three proxies through the Toxiproxy API and applies the latency
  # profile (the Toxiproxy image is distroless, like in docker compose).
  toxiproxy_init = join(" ", [
    "until curl -sf http://localhost:8474/version; do sleep 1; done;",
    "for p in ${join(" ", local.proxies)}; do",
    "name=$${p%%:*}; rest=$${p#*:}; listen=$${rest%%:*}; upstream=$${rest#*:};",
    "curl -sf -X POST http://localhost:8474/proxies -H 'Content-Type: application/json'",
    "-d \"{\\\"name\\\":\\\"$name\\\",\\\"listen\\\":\\\"0.0.0.0:$listen\\\",\\\"upstream\\\":\\\"localhost:$upstream\\\",\\\"enabled\\\":true}\" || exit 1;",
    "curl -sf -X POST http://localhost:8474/proxies/$name/toxics -H 'Content-Type: application/json'",
    "-d \"{\\\"name\\\":\\\"profile_latency\\\",\\\"type\\\":\\\"latency\\\",\\\"stream\\\":\\\"downstream\\\",\\\"attributes\\\":{\\\"latency\\\":${local.latency.latency},\\\"jitter\\\":${local.latency.jitter}}}\" || exit 1;",
    "done",
  ])
}

resource "aws_ecs_task_definition" "sources" {
  family                   = "${var.name}-sources"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 512
  memory                   = 1024
  execution_role_arn       = aws_iam_role.execution.arn

  container_definitions = jsonencode(concat(
    [
      for c in [
        { name = "openfinance", image = local.images.openfinance, port = 9000 },
        { name = "opendata", image = local.images.opendata, port = 9010 },
        { name = "kyc", image = local.images.kyc, port = 9020 },
        ] : {
        name             = c.name
        image            = c.image
        essential        = true
        environment      = [{ name = "PORT", value = tostring(c.port) }]
        portMappings     = [{ containerPort = c.port, protocol = "tcp" }]
        logConfiguration = { logDriver = "awslogs", options = local.log_options }
      }
    ],
    [
      {
        name             = "toxiproxy"
        image            = "ghcr.io/shopify/toxiproxy:2.9.0"
        essential        = true
        command          = ["-host=0.0.0.0"]
        portMappings     = [for port in [9001, 9011, 9021] : { containerPort = port, protocol = "tcp" }]
        logConfiguration = { logDriver = "awslogs", options = local.log_options }
      },
      {
        name             = "toxiproxy-init"
        image            = "curlimages/curl:8.10.1"
        essential        = false
        entryPoint       = ["sh", "-c"]
        command          = [local.toxiproxy_init]
        dependsOn        = [{ containerName = "toxiproxy", condition = "START" }]
        logConfiguration = { logDriver = "awslogs", options = local.log_options }
      },
    ],
  ))
}

resource "aws_ecs_task_definition" "origination" {
  family                   = "${var.name}-origination"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 512 # EXP-L01: 0.5 vCPU / 1 GB
  memory                   = 1024
  execution_role_arn       = aws_iam_role.execution.arn

  container_definitions = jsonencode([
    {
      name         = "origination"
      image        = local.images.origination
      essential    = true
      portMappings = [{ containerPort = 8080, protocol = "tcp" }]
      environment = [
        { name = "PORT", value = "8080" },
        { name = "REDIS_ADDR", value = "localhost:6379" },
        { name = "OPEN_FINANCE_URL", value = "http://${local.sources_host}:9001" },
        { name = "OPEN_DATA_URL", value = "http://${local.sources_host}:9011" },
        { name = "KYC_URL", value = "http://${local.sources_host}:9021" },
        { name = "SOURCE_BUDGET_MS", value = "120" },
        { name = "PROFILING_DEADLINE_MS", value = "700" },
        { name = "HTTP_MAX_CONNS_PER_HOST", value = "200" },
        { name = "OPEN_FINANCE_REQUIRED", value = tostring(var.open_finance_required) },
      ]
      dependsOn        = [{ containerName = "redis", condition = "START" }]
      logConfiguration = { logDriver = "awslogs", options = local.log_options }
    },
    {
      name             = "redis"
      image            = "redis:7-alpine"
      essential        = true
      portMappings     = [{ containerPort = 6379, protocol = "tcp" }]
      logConfiguration = { logDriver = "awslogs", options = local.log_options }
    },
  ])
}

resource "aws_ecs_service" "sources" {
  name            = "sources"
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.sources.arn
  desired_count   = local.desired_tasks
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = [aws_subnet.public[0].id]
    security_groups  = [aws_security_group.sources.id]
    assign_public_ip = true
  }

  service_registries {
    registry_arn = aws_service_discovery_service.sources.arn
  }
}

resource "aws_ecs_service" "origination" {
  name                              = "origination"
  cluster                           = aws_ecs_cluster.this.id
  task_definition                   = aws_ecs_task_definition.origination.arn
  desired_count                     = local.desired_tasks
  launch_type                       = "FARGATE"
  health_check_grace_period_seconds = 30

  network_configuration {
    subnets          = [aws_subnet.public[0].id]
    security_groups  = [aws_security_group.origination.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.origination.arn
    container_name   = "origination"
    container_port   = 8080
  }

  depends_on = [aws_lb_listener.http]
}

# --- Internal ALB (health check of ADR-02) -----------------------------------------------

resource "aws_lb" "internal" {
  name               = "stg-ha-l01-internal"
  internal           = true
  load_balancer_type = "application"
  subnets            = aws_subnet.public[*].id
  security_groups    = [aws_security_group.alb.id]
  idle_timeout       = 10
}

resource "aws_lb_target_group" "origination" {
  name                 = "stg-ha-l01-origination"
  port                 = 8080
  protocol             = "HTTP"
  target_type          = "ip"
  vpc_id               = aws_vpc.this.id
  deregistration_delay = 30

  health_check {
    path                = "/health"
    interval            = 5
    timeout             = 3
    healthy_threshold   = 2
    unhealthy_threshold = 2
    matcher             = "200"
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.internal.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.origination.arn
  }
}

# --- API Gateway REST through a VPC link V2 ------------------------------------------------

resource "aws_apigatewayv2_vpc_link" "this" {
  name               = var.name
  subnet_ids         = aws_subnet.public[*].id
  security_group_ids = [aws_security_group.vpc_link.id]
}

module "apigateway" {
  source = "../../../modules/apigateway"

  name                   = var.name
  stage_name             = "staging"
  backend_base_url       = "http://${aws_lb.internal.dns_name}"
  vpc_link_v2_id         = aws_apigatewayv2_vpc_link.this.id
  integration_target_arn = aws_lb.internal.arn
  cache_ttl_seconds      = var.cache_ttl_seconds
  partners               = var.partners
}

# --- k6 task: runs the measurement sequence once and leaves the summaries in the logs -----

resource "aws_ecs_task_definition" "k6" {
  family                   = "${var.name}-k6"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 1024
  memory                   = 2048
  execution_role_arn       = aws_iam_role.execution.arn

  # Test API keys of the window's partners; they are destroyed with it.
  container_definitions = jsonencode([
    {
      name      = "k6"
      image     = local.images.k6
      essential = true
      environment = [
        { name = "BASE_URL", value = module.apigateway.invoke_url },
        { name = "API_KEY_A", value = module.apigateway.api_key_values["socio-a"] },
        { name = "API_KEY_B", value = module.apigateway.api_key_values["socio-b"] },
      ]
      logConfiguration = { logDriver = "awslogs", options = local.log_options }
    },
  ])
}

resource "aws_ecs_service" "k6" {
  name            = "k6"
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.k6.arn
  desired_count   = var.services_enabled && var.k6_enabled ? 1 : 0
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = [aws_subnet.public[0].id]
    security_groups  = [aws_security_group.k6.id]
    assign_public_ip = true
  }

  depends_on = [aws_ecs_service.origination]
}
