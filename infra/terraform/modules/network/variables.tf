variable "name" {
  description = "Prefix of the resource names (for example solventa-stg)."
  type        = string
}

variable "cidr_block" {
  description = "CIDR of the VPC (a /16; public subnets take the first two /24 and private subnets the /24 from index 10)."
  type        = string

  validation {
    condition     = can(cidrhost(var.cidr_block, 0)) && tonumber(split("/", var.cidr_block)[1]) <= 16
    error_message = "cidr_block must be a valid CIDR of size /16 or larger."
  }
}

variable "nat_mode" {
  description = "Internet egress of the tasks: none (tasks in public subnets with a public IP, no NAT cost), single (one shared NAT Gateway) or per_az (one NAT Gateway per zone, for the final demo)."
  type        = string
  default     = "none"

  validation {
    condition     = contains(["none", "single", "per_az"], var.nat_mode)
    error_message = "nat_mode must be none, single or per_az."
  }
}

variable "tags" {
  description = "Tags added to the taggable resources (the provider default_tags also apply)."
  type        = map(string)
  default     = {}
}
