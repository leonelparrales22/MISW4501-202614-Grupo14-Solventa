# Red del staging: VPC con dos subredes públicas en dos zonas de disponibilidad.
# Las tareas Fargate reciben IP pública para descargar la imagen y leer el
# secreto sin NAT Gateway (decisión D5). El acceso queda restringido por
# security groups.

data "aws_availability_zones" "disponibles" {
  state = "available"

  # Solo zonas donde VPC Link V2 está soportado en us-east-1.
  dynamic "filter" {
    for_each = var.region == "us-east-1" ? [1] : []
    content {
      name   = "zone-id"
      values = ["use1-az1", "use1-az2", "use1-az4", "use1-az5", "use1-az6"]
    }
  }
}

data "http" "ip_operador" {
  count = var.operador_cidr == "" ? 1 : 0
  url   = "https://checkip.amazonaws.com"
}

locals {
  zonas         = slice(data.aws_availability_zones.disponibles.names, 0, 2)
  operador_cidr = var.operador_cidr != "" ? var.operador_cidr : "${chomp(data.http.ip_operador[0].response_body)}/32"
}

resource "aws_vpc" "principal" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = "${var.prefijo}-vpc" }
}

resource "aws_internet_gateway" "igw" {
  vpc_id = aws_vpc.principal.id
  tags   = { Name = "${var.prefijo}-igw" }
}

resource "aws_subnet" "publica" {
  count                   = 2
  vpc_id                  = aws_vpc.principal.id
  cidr_block              = cidrsubnet(aws_vpc.principal.cidr_block, 8, count.index)
  availability_zone       = local.zonas[count.index]
  map_public_ip_on_launch = true
  tags                    = { Name = "${var.prefijo}-publica-${count.index}" }
}

resource "aws_route_table" "publica" {
  vpc_id = aws_vpc.principal.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.igw.id
  }

  tags = { Name = "${var.prefijo}-rt-publica" }
}

resource "aws_route_table_association" "publica" {
  count          = 2
  subnet_id      = aws_subnet.publica[count.index].id
  route_table_id = aws_route_table.publica.id
}

# --- Security groups ---

resource "aws_security_group" "vpc_link" {
  name        = "${var.prefijo}-sg-vpclink"
  description = "Interfaces del VPC Link V2 de API Gateway"
  vpc_id      = aws_vpc.principal.id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "alb" {
  name        = "${var.prefijo}-sg-alb"
  description = "ALB: solo desde el VPC Link y desde el operador"
  vpc_id      = aws_vpc.principal.id

  ingress {
    from_port       = 80
    to_port         = 80
    protocol        = "tcp"
    security_groups = [aws_security_group.vpc_link.id]
  }

  ingress {
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = [local.operador_cidr]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "tarea" {
  name        = "${var.prefijo}-sg-tarea"
  description = "Tareas del stub: solo desde el ALB y desde el operador (inyeccion de fallas)"
  vpc_id      = aws_vpc.principal.id

  ingress {
    from_port       = 8080
    to_port         = 8080
    protocol        = "tcp"
    security_groups = [aws_security_group.alb.id]
  }

  ingress {
    from_port   = 8080
    to_port     = 8080
    protocol    = "tcp"
    cidr_blocks = [local.operador_cidr]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "rds" {
  name        = "${var.prefijo}-sg-rds"
  description = "RDS: solo desde las tareas"
  vpc_id      = aws_vpc.principal.id

  ingress {
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_security_group.tarea.id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}
