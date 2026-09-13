# Clúster ECS con dos servicios (orquestador y fuentes) y una definición de
# tarea para k6 que se lanza a mano por cada combinación.
#
# Los servicios se encuentran por nombre con Cloud Map: orquestador.exp.local
# y fuentes.exp.local. No hay balanceador porque no hace falta: k6 le pega
# directo al orquestador y el orquestador directo a las fuentes.

resource "aws_ecs_cluster" "exp" {
  name = local.nombre
}

resource "aws_ecs_cluster_capacity_providers" "exp" {
  cluster_name       = aws_ecs_cluster.exp.name
  capacity_providers = ["FARGATE"]
  default_capacity_provider_strategy {
    capacity_provider = "FARGATE"
    weight            = 1
  }
}

# --- Descubrimiento de servicios ---

resource "aws_service_discovery_private_dns_namespace" "exp" {
  name = "exp.local"
  vpc  = aws_vpc.exp.id
}

resource "aws_service_discovery_service" "orquestador" {
  name = "orquestador"
  dns_config {
    namespace_id = aws_service_discovery_private_dns_namespace.exp.id
    dns_records {
      ttl  = 5
      type = "A"
    }
  }
}

resource "aws_service_discovery_service" "fuentes" {
  name = "fuentes"
  dns_config {
    namespace_id = aws_service_discovery_private_dns_namespace.exp.id
    dns_records {
      ttl  = 5
      type = "A"
    }
  }
}

# --- Fuentes simuladas ---

resource "aws_ecs_task_definition" "fuentes" {
  family                   = "${local.nombre}-fuentes"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.ejecucion.arn
  task_role_arn            = aws_iam_role.tarea.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name      = "fuentes"
    image     = "${aws_ecr_repository.exp.repository_url}:${var.imagen_tag}"
    command   = ["--mode=fuentes"]
    essential = true
    portMappings = [{ containerPort = 8081, protocol = "tcp" }]
    environment = [
      { name = "ADDR", value = ":8081" },
      { name = "LAT_P50_MS", value = tostring(var.lat_p50_ms) },
      { name = "LAT_P95_MS", value = tostring(var.lat_p95_ms) },
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.fuentes.name
        awslogs-region        = var.region
        awslogs-stream-prefix = "fuentes"
      }
    }
  }])
}

resource "aws_ecs_service" "fuentes" {
  name            = "fuentes"
  cluster         = aws_ecs_cluster.exp.id
  task_definition = aws_ecs_task_definition.fuentes.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = [aws_subnet.publica.id]
    security_groups  = [aws_security_group.exp.id]
    assign_public_ip = true
  }

  service_registries {
    registry_arn = aws_service_discovery_service.fuentes.arn
  }

  # Un despliegue nuevo reemplaza la tarea vieja sin dejar dos corriendo:
  # con dos tareas de fuentes la latencia se repartiría y ensuciaría la medición.
  deployment_minimum_healthy_percent = 0
  deployment_maximum_percent         = 100
}

# --- Orquestador (lo que se mide) ---

resource "aws_ecs_task_definition" "orquestador" {
  family                   = "${local.nombre}-orquestador"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.orq_cpu
  memory                   = var.orq_memoria
  execution_role_arn       = aws_iam_role.ejecucion.arn
  task_role_arn            = aws_iam_role.tarea.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name      = "orquestador"
    image     = "${aws_ecr_repository.exp.repository_url}:${var.imagen_tag}"
    command   = ["--mode=orquestador"]
    essential = true
    portMappings = [{ containerPort = 8080, protocol = "tcp" }]
    environment = [
      { name = "ADDR", value = ":8080" },
      { name = "FUENTES", value = tostring(var.n_fuentes) },
      { name = "FUENTES_URL", value = "http://fuentes.exp.local:8081" },
      { name = "CORTE_DEP_MS", value = tostring(var.corte_dep_ms) },
      { name = "DEADLINE_MS", value = tostring(var.deadline_ms) },
      { name = "REDIS_ADDR", value = "${aws_elasticache_cluster.redis.cache_nodes[0].address}:6379" },
      { name = "REDIS_TTL_S", value = "3600" },
      { name = "MAX_CONEX_HOST", value = tostring(var.max_conex_host) },
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.orquestador.name
        awslogs-region        = var.region
        awslogs-stream-prefix = "orquestador"
      }
    }
  }])
}

resource "aws_ecs_service" "orquestador" {
  name            = "orquestador"
  cluster         = aws_ecs_cluster.exp.id
  task_definition = aws_ecs_task_definition.orquestador.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = [aws_subnet.publica.id]
    security_groups  = [aws_security_group.exp.id]
    assign_public_ip = true
  }

  service_registries {
    registry_arn = aws_service_discovery_service.orquestador.arn
  }

  deployment_minimum_healthy_percent = 0
  deployment_maximum_percent         = 100

  depends_on = [aws_ecs_service.fuentes]
}

# --- k6: no es un servicio, es una tarea que se lanza por combinación ---

resource "aws_ecs_task_definition" "k6" {
  family                   = "${local.nombre}-k6"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 1024
  memory                   = 2048
  execution_role_arn       = aws_iam_role.ejecucion.arn
  task_role_arn            = aws_iam_role.tarea.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name      = "k6"
    image     = "${aws_ecr_repository.exp.repository_url}:k6"
    command   = ["run", "/matriz.js"]
    essential = true
    # Estos valores se sobreescriben en cada run-task con los de la combinación.
    environment = [
      { name = "BASE_URL", value = "http://orquestador.exp.local:8080" },
      { name = "N_FUENTES", value = "3" },
      { name = "CACHE_HIT", value = "0" },
      { name = "RPS_MAX", value = "83" },
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.k6.name
        awslogs-region        = var.region
        awslogs-stream-prefix = "k6"
      }
    }
  }])
}
