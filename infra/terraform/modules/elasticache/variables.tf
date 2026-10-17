variable "name" {
  description = "Identifier of the replication group and prefix of its resources (at most 40 characters)."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,39}$", var.name))
    error_message = "name must start with a letter and have at most 40 lowercase letters, digits and dashes."
  }
}

variable "vpc_id" {
  description = "VPC of the cache."
  type        = string
}

variable "subnet_ids" {
  description = "Private subnets of the cache, in two availability zones."
  type        = list(string)
}

variable "allowed_security_group_ids" {
  description = "Security groups allowed to reach Redis, by a static key (for example { tasks = <sg id> }; the key keeps for_each known at plan time)."
  type        = map(string)
}

variable "engine_version" {
  description = "Redis OSS version."
  type        = string
  default     = "7.1"
}

variable "parameter_group_name" {
  description = "Parameter group matching engine_version."
  type        = string
  default     = "default.redis7"
}

variable "node_type" {
  description = "Node type (cache.t4g.micro is the smallest Graviton type)."
  type        = string
  default     = "cache.t4g.micro"
}

variable "multi_az" {
  description = "Replica in the second zone with automatic failover. false by default (cost); true for the final demo or a fault tolerance test."
  type        = bool
  default     = false
}

variable "snapshot_retention_days" {
  description = "Days of automatic snapshots (0 disables them; the cache is rebuilt from the sources)."
  type        = number
  default     = 0
}

variable "tags" {
  description = "Tags added to the taggable resources (the provider default_tags also apply)."
  type        = map(string)
  default     = {}
}
