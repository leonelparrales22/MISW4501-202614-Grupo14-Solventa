variable "name" {
  description = "Identifier of the instance and prefix of its resources (for example solventa-stg)."
  type        = string
}

variable "vpc_id" {
  description = "VPC of the instance."
  type        = string
}

variable "subnet_ids" {
  description = "Private subnets of the instance, in two availability zones."
  type        = list(string)

  validation {
    condition     = length(var.subnet_ids) >= 2
    error_message = "A DB subnet group needs subnets in at least two availability zones."
  }
}

variable "allowed_security_group_ids" {
  description = "Security groups allowed to reach PostgreSQL, by a static key (for example { tasks = <sg id> }; the key keeps for_each known at plan time)."
  type        = map(string)
}

variable "databases" {
  description = "One database and one login role (<name>_app) per service."
  type        = set(string)

  validation {
    condition     = alltrue([for d in var.databases : can(regex("^[a-z][a-z0-9_]{0,40}$", d))])
    error_message = "Database names must be lowercase identifiers (letters, digits and underscores)."
  }
}

variable "engine_version" {
  description = "PostgreSQL major version (RDS picks the default minor and upgrades it)."
  type        = string
  default     = "16"
}

variable "instance_class" {
  description = "Instance class (db.t4g.micro is the smallest Graviton class)."
  type        = string
  default     = "db.t4g.micro"
}

variable "allocated_storage" {
  description = "Initial storage in GB (gp3)."
  type        = number
  default     = 20
}

variable "max_allocated_storage" {
  description = "Storage autoscaling limit in GB (0 disables it)."
  type        = number
  default     = 0
}

variable "master_username" {
  description = "Administrator user; its password is managed by RDS in Secrets Manager."
  type        = string
  default     = "solventa_admin"
}

variable "multi_az" {
  description = "Standby in the second zone with automatic failover. false by default (cost); true for the final demo or a fault tolerance test. Switching keeps the data."
  type        = bool
  default     = false
}

variable "apply_immediately" {
  description = "Apply modifications (for example multi_az) now instead of in the next maintenance window."
  type        = bool
  default     = true
}

variable "backup_retention_days" {
  description = "Days of automated backups (1-35)."
  type        = number
  default     = 1

  validation {
    condition     = var.backup_retention_days >= 1 && var.backup_retention_days <= 35
    error_message = "backup_retention_days must be between 1 and 35."
  }
}

variable "deletion_protection" {
  description = "Protect the instance against deletion."
  type        = bool
  default     = true
}

variable "skip_final_snapshot" {
  description = "Destroy without a final snapshot (only ephemeral environments)."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags added to the taggable resources (the provider default_tags also apply)."
  type        = map(string)
  default     = {}
}
