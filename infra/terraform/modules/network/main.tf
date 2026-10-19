# VPC in two availability zones (T4): one public and one private subnet per
# zone. The data stores (RDS, ElastiCache) always live in the private subnets;
# where the Fargate tasks run depends on nat_mode:
#
# - none (default): no NAT Gateway; the tasks run in the public subnets with a
#   public IP, only to reach ECR, Secrets Manager, CloudWatch and the Cognito
#   JWKS. Their security groups admit no traffic from the internet.
# - single: one NAT Gateway in the first zone shared by both private subnets.
#   If that zone fails, the tasks of the other lose their egress.
# - per_az: one NAT Gateway per zone (fault tolerance of the final demo).
#
# The S3 gateway endpoint is free, so the private subnets always get it.

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, 2)

  nat_count = { none = 0, single = 1, per_az = 2 }[var.nat_mode]
  use_nat   = local.nat_count > 0
}

resource "aws_vpc" "this" {
  cidr_block           = var.cidr_block
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = merge(var.tags, { Name = var.name })
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = var.name })
}

resource "aws_subnet" "public" {
  count = 2

  vpc_id                  = aws_vpc.this.id
  availability_zone       = local.azs[count.index]
  cidr_block              = cidrsubnet(var.cidr_block, 8, count.index)
  map_public_ip_on_launch = !local.use_nat
  tags                    = merge(var.tags, { Name = "${var.name}-public-${local.azs[count.index]}", Tier = "public" })
}

resource "aws_subnet" "private" {
  count = 2

  vpc_id            = aws_vpc.this.id
  availability_zone = local.azs[count.index]
  cidr_block        = cidrsubnet(var.cidr_block, 8, 10 + count.index)
  tags              = merge(var.tags, { Name = "${var.name}-private-${local.azs[count.index]}", Tier = "private" })
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = "${var.name}-public" })

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

# --- NAT Gateways (nat_mode) -------------------------------------------------------

resource "aws_eip" "nat" {
  count = local.nat_count

  domain = "vpc"
  tags   = merge(var.tags, { Name = "${var.name}-nat-${local.azs[count.index]}" })
}

resource "aws_nat_gateway" "this" {
  count = local.nat_count

  allocation_id = aws_eip.nat[count.index].id
  subnet_id     = aws_subnet.public[count.index].id
  tags          = merge(var.tags, { Name = "${var.name}-nat-${local.azs[count.index]}" })

  depends_on = [aws_internet_gateway.this]
}

# One route table per private subnet, so per_az keeps each zone on its own NAT.
# With single both point to the first NAT; with none there is no default route.
resource "aws_route_table" "private" {
  count = 2

  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = "${var.name}-private-${local.azs[count.index]}" })
}

resource "aws_route" "private_nat" {
  count = local.use_nat ? 2 : 0

  route_table_id         = aws_route_table.private[count.index].id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.this[min(count.index, local.nat_count - 1)].id
}

resource "aws_route_table_association" "private" {
  count = 2

  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private[count.index].id
}

data "aws_region" "current" {}

resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.this.id
  service_name      = "com.amazonaws.${data.aws_region.current.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = aws_route_table.private[*].id
  tags              = merge(var.tags, { Name = "${var.name}-s3" })
}
