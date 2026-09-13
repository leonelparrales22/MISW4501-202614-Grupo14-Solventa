# Red mínima: una VPC, una subred pública, salida a internet por IGW.
#
# Las tareas de Fargate reciben IP pública para poder bajar la imagen de ECR
# y mandar logs a CloudWatch. Así evitamos el NAT Gateway, que cuesta ~1 USD
# por día aunque no haga nada. El grupo de seguridad no deja entrar nada
# desde afuera, solo tráfico entre las propias tareas.

resource "aws_vpc" "exp" {
  cidr_block           = "10.90.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = "${local.nombre}-vpc" }
}

resource "aws_internet_gateway" "exp" {
  vpc_id = aws_vpc.exp.id
  tags   = { Name = "${local.nombre}-igw" }
}

resource "aws_subnet" "publica" {
  vpc_id                  = aws_vpc.exp.id
  cidr_block              = "10.90.1.0/24"
  availability_zone       = local.az
  map_public_ip_on_launch = true
  tags                    = { Name = "${local.nombre}-publica" }
}

resource "aws_route_table" "publica" {
  vpc_id = aws_vpc.exp.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.exp.id
  }
  tags = { Name = "${local.nombre}-rt" }
}

resource "aws_route_table_association" "publica" {
  subnet_id      = aws_subnet.publica.id
  route_table_id = aws_route_table.publica.id
}

# Un solo grupo de seguridad para todo: las tareas se hablan entre ellas y
# con Redis; desde internet no entra nada.
resource "aws_security_group" "exp" {
  name        = "${local.nombre}-sg"
  description = "EXP-L01: solo trafico interno entre tareas y Redis"
  vpc_id      = aws_vpc.exp.id

  ingress {
    description = "todo el trafico entre miembros del mismo grupo"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    self        = true
  }

  egress {
    description = "salida libre (ECR, CloudWatch)"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "${local.nombre}-sg" }
}
