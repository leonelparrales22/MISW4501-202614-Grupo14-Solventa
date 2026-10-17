output "vpc_id" {
  description = "ID of the VPC."
  value       = aws_vpc.this.id
}

output "vpc_cidr_block" {
  description = "CIDR of the VPC."
  value       = aws_vpc.this.cidr_block
}

output "availability_zones" {
  description = "The two availability zones of the subnets."
  value       = local.azs
}

output "public_subnet_ids" {
  description = "Public subnets, one per zone."
  value       = aws_subnet.public[*].id
}

output "private_subnet_ids" {
  description = "Private subnets, one per zone (RDS, ElastiCache, internal ALB and VPC link)."
  value       = aws_subnet.private[*].id
}

output "app_subnet_ids" {
  description = "Subnets of the Fargate tasks: public without NAT, private with NAT."
  value       = local.use_nat ? aws_subnet.private[*].id : aws_subnet.public[*].id
}

output "app_assign_public_ip" {
  description = "Whether the Fargate tasks need a public IP (only when nat_mode is none)."
  value       = !local.use_nat
}

output "nat_gateway_ids" {
  description = "NAT Gateways created by nat_mode (empty with none)."
  value       = aws_nat_gateway.this[*].id
}
